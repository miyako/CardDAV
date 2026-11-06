package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	vcard "github.com/emersion/go-vcard"
)

const sample = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:test-uid\r\nFN:世界 café\r\nitem1.X-UNKNOWN;TYPE=home:one\\,two\r\nitem1.X-UNKNOWN:again\r\nEND:VCARD\r\n"

func TestInputProtocol(t *testing.T) {
	bad := []string{
		``, `[]`, `null`, `{}`, `{"protocol":2,"operation":"parse"}`,
		`{"protocol":1,"operation":"build","contact":null}`,
		`{"protocol":1,"protocol":1,"operation":"build"}`,
		`{"protocol":1,"operation":"build","contact":{"formattedName":"a","uid":"1","uid":"2"}}`,
		`{"protocol":1,"operation":"build","contact":{"formattedName":"a","extra":"secret"}}`,
		`{"protocol":1,"operation":"build","contact":{"FormattedName":"a"}}`,
		`{"protocol":1,"operation":"parse","vcard":"x"} {}`,
		`{"protocol":1,"operation":"parse","vcard":"x","insecure":true}`,
		`{"protocol":1,"operation":"build","endpoint":"https://example.com","contact":{"formattedName":"a"}}`,
		`{"protocol":1.0,"operation":"build"}`,
		`{"protocol":1,"operation":"build","contact":{"formattedName":"\ud800"}}`,
		`{"protocol":1,"operation":"build","contact":{"formattedName":"\udc00"}}`,
		"{\"protocol\":1,\"operation\":\"parse\",\"vcard\":\"\xff\"}",
	}
	for _, s := range bad {
		if _, f := decode([]byte(s)); f == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if _, f := decode(bytes.Repeat([]byte(" "), maxInput+1)); f == nil || f.Code != "input_limit" {
		t.Fatal(f)
	}
	_, f := decode([]byte(`{"protocol":1,"operation":"build","contact":{"formattedName":"A","uid":"fixed"}}`))
	if f != nil {
		t.Fatal(f)
	}
	for _, s := range []string{`"\ud83d\ude00"`, `"\\ud800"`} {
		if _, f := decode([]byte(`{"protocol":1,"operation":"build","contact":{"formattedName":` + s + `}}`)); f != nil {
			t.Fatal(s, f)
		}
	}
}

func TestCLIStreamsAndDeadline(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, strings.NewReader("ignored"), &out, &diagnostic); code != 0 || out.String() != `{"protocol":1,"version":"0.1.0"}`+"\n" || diagnostic.Len() != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	out.Reset()
	diagnostic.Reset()
	if code := run(context.Background(), []string{"request", "--password", "SECRET"}, strings.NewReader(""), &out, &diagnostic); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "SECRET") {
		t.Fatal(code, diagnostic.String())
	}
	out.Reset()
	diagnostic.Reset()
	in, writer := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if code := run(ctx, []string{"request"}, in, &out, &diagnostic); code != 1 || !strings.Contains(diagnostic.String(), `"timeout"`) {
		t.Fatal(code, diagnostic.String())
	}
	_ = writer.Close()
	out.Reset()
	diagnostic.Reset()
	req := `{"protocol":1,"operation":"build","contact":{"uid":"fixed","formattedName":"世界"}}`
	if code := run(context.Background(), []string{"request"}, strings.NewReader(req), &out, &diagnostic); code != 0 || diagnostic.Len() != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var envelope struct {
		Protocol int
		Result   map[string]any
	}
	if json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.Protocol != 1 || envelope.Result["uid"] != "fixed" || !strings.HasSuffix(out.String(), "\n") {
		t.Fatal(out.String())
	}
	out.Reset()
	diagnostic.Reset()
	if code := run(context.Background(), []string{"request"}, strings.NewReader(req+req), &out, &diagnostic); code != 1 || out.Len() != 0 {
		t.Fatal(code, out.String())
	}
}

func TestCardBuildParse(t *testing.T) {
	for _, ver := range []string{"3.0", "4.0"} {
		t.Run(ver, func(t *testing.T) {
			c := &contact{UID: "stable-uid", FormattedName: "世界 café, name;\\\nnext", GivenName: "A;B", FamilyName: "C,D", Organization: "Example; Inc", Title: "Dr", Note: strings.Repeat("😀", 90) + ";\\\nline", Emails: []typedValue{{"one@example.org", []string{"home", "internet"}}, {"two@example.org", []string{"work"}}}, Phones: []typedValue{{"+81 123", []string{"cell"}}}, Addresses: []address{{Types: []string{"home"}, Street: "1; Main, Street", Locality: "東京", Country: "日本"}}, URLs: []string{"https://example.org/a;b?q=one,two"}}
			value, f := buildCard(c, ver)
			if f != nil {
				t.Fatal(f)
			}
			built := value.(builtCard)
			parsed, f := parseCard(built.VCard)
			if f != nil || parsed.UID != c.UID || parsed.Version != ver || parsed.FormattedName != c.FormattedName {
				t.Fatal(parsed, f)
			}
			for _, line := range strings.Split(built.VCard, "\r\n") {
				if len(line) > 75 || !utf8.ValidString(line) {
					t.Fatalf("invalid fold %q", line)
				}
			}
			if !strings.Contains(built.VCard, `A\;B`) || !strings.Contains(built.VCard, `C\,D`) || !strings.Contains(built.VCard, `URL:https://example.org/a;b?q=one,two`) {
				t.Fatal(built.VCard)
			}
			count := 0
			for _, p := range parsed.Properties {
				if p.Name == "EMAIL" {
					count++
					if len(p.Parameters["TYPE"]) == 0 {
						t.Fatal(p)
					}
				}
				if p.Name == "NOTE" && p.Value != c.Note {
					t.Fatal("note round trip")
				}
			}
			if count != 2 {
				t.Fatal(count)
			}
		})
	}
	a, f := buildCard(&contact{FormattedName: "Name"}, "")
	if f != nil || !strings.HasPrefix(a.(builtCard).UID, "urn:uuid:") {
		t.Fatal(a, f)
	}
	b, _ := buildCard(&contact{FormattedName: "Name"}, "")
	if a.(builtCard).UID == b.(builtCard).UID {
		t.Fatal("generated identical UID")
	}
	for _, c := range []*contact{nil, {}, {FormattedName: "A\rB"}, {FormattedName: "A", Emails: []typedValue{{"x", []string{"x\r\nBAD"}}}}} {
		if _, f := buildCard(c, "3.0"); f == nil {
			t.Fatal("accepted invalid contact")
		}
	}
	if _, f := buildCard(&contact{FormattedName: "a"}, "2.1"); f == nil || f.Code != "unsupported_version" {
		t.Fatal(f)
	}
	if _, f := buildCard(&contact{FormattedName: "a", Note: strings.Repeat("a", maxCard)}, "3.0"); f == nil {
		t.Fatal("oversize card")
	}
}

func TestRawPreservationAndDependencyBehavior(t *testing.T) {
	for _, raw := range []string{sample, strings.ReplaceAll(sample, "\r\n", "\n")} {
		card, f := parseCard(raw)
		if f != nil || card.VCard != raw {
			t.Fatal(card, f)
		}
		unknown := []property{}
		for _, p := range card.Properties {
			if p.Name == "X-UNKNOWN" {
				unknown = append(unknown, p)
			}
		}
		if len(unknown) != 2 || unknown[0].Group != "item1" || unknown[0].Value != "one,two" {
			t.Fatal(unknown)
		}
	}
	// Review regression: upstream escapes commas/backslashes but not semicolons
	// and does not fold. The builder intentionally supplies those RFC behaviors.
	var b bytes.Buffer
	card := vcard.Card{}
	card.SetValue("VERSION", "4.0")
	card.SetValue("FN", strings.Repeat("界", 40)+";,\n")
	if err := vcard.NewEncoder(&b).Encode(card); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `;\,\n`) || strings.Contains(b.String(), "\r\n ") {
		t.Fatal("upstream behavior changed")
	}
	decoded, e := vcard.NewDecoder(strings.NewReader(sample)).Decode()
	if e != nil || len(decoded["X-UNKNOWN"]) != 2 || decoded["X-UNKNOWN"][0].Group != "item1" {
		t.Fatal(decoded, e)
	}
	extra := strings.Replace(sample, "FN:", `X-PARAM;TYPE="home","voice";X-LABEL="a^'b^^c^nd":ok`+"\r\nFN:", 1)
	parsed, f := parseCard(extra)
	if f != nil {
		t.Fatal(f)
	}
	for _, p := range parsed.Properties {
		if p.Name == "X-PARAM" && (len(p.Parameters["TYPE"]) != 2 || p.Parameters.Get("X-LABEL") != "a\"b^c\nd") {
			t.Fatal(p)
		}
	}
	for _, raw := range []string{
		"", sample + sample, strings.TrimSuffix(sample, "\r\n"), strings.Replace(sample, "UID:test-uid\r\n", "", 1),
		strings.Replace(sample, "VERSION:3.0", "VERSION:2.1", 1), strings.Replace(sample, "FN:", "VERSION:3.0\r\nFN:", 1),
		strings.Replace(sample, "FN:", "bad line\r\nFN:", 1), strings.Replace(sample, "FN:", "END:WRONG\r\nFN:", 1),
		strings.Replace(sample, "FN:", "NUL\x00\r\nFN:", 1), strings.Replace(sample, "VERSION:3.0", "VERSION:3.0\r\nBEGIN:VCARD", 1),
		strings.Replace(sample, "FN:", "FN;BROKEN:", 1),
		strings.Replace(sample, "FN:", `FN;TYPE="unclosed:`, 1),
	} {
		if _, f := parseCard(raw); f == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func multi(inner string) string {
	return `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">` + inner + `</d:multistatus>`
}
func item(href, props string) string {
	return `<d:response><d:href>` + xmlText(href) + `</d:href><d:propstat><d:prop>` + props + `</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
}
func bookProps(versions ...string) string {
	s := `<d:resourcetype><d:collection/><c:addressbook/></d:resourcetype><d:displayname>My Contacts</d:displayname><c:supported-address-data>`
	for _, v := range versions {
		s += `<c:address-data-type content-type="text/vcard" version="` + v + `"/>`
	}
	return s + `</c:supported-address-data>`
}
func serverOptions(s *httptest.Server) backendOptions {
	return backendOptions{transport: s.Client().Transport, allowPrivate: true}
}
func TestDiscoveryQueryGetCRUD(t *testing.T) {
	var putCount atomic.Int32
	var lastBody string
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "user" || pass != "secret" {
			t.Error("missing auth")
			w.WriteHeader(401)
			return
		}
		data, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			if r.Header.Get("Depth") != "0" {
				t.Error("principal depth")
			}
			w.WriteHeader(207)
			io.WriteString(w, multi(item("/", `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`)))
		case r.Method == "PROPFIND" && r.URL.Path == "/principal/":
			w.WriteHeader(207)
			io.WriteString(w, multi(item("/principal/", `<c:addressbook-home-set><d:href>/home/</d:href></c:addressbook-home-set>`)))
		case r.Method == "PROPFIND" && r.URL.Path == "/home/":
			if r.Header.Get("Depth") != "1" {
				t.Error("collection depth")
			}
			w.WriteHeader(207)
			io.WriteString(w, multi(item("/home/", `<d:resourcetype><d:collection/></d:resourcetype>`)+item("/home/book/", bookProps("3.0", "4.0"))))
		case r.Method == "PROPFIND" && r.URL.Path == "/home/book/":
			w.WriteHeader(207)
			io.WriteString(w, multi(item("/home/book/", bookProps("3.0", "4.0"))))
		case r.Method == "REPORT":
			if r.Header.Get("Depth") != "1" || !strings.Contains(string(data), `match-type="starts-with"`) || !strings.Contains(string(data), "&amp;") || !strings.Contains(string(data), `version="3.0"`) || !strings.Contains(string(data), "<c:nresults>3</c:nresults>") {
				t.Errorf("REPORT: %s", data)
			}
			w.WriteHeader(207)
			io.WriteString(w, multi(item("/home/book/a.vcf", `<d:getetag>&quot;one&quot;</d:getetag><c:address-data>`+xmlText(sample)+`</c:address-data>`)))
		case r.Method == "GET":
			w.Header().Set("ETag", `"one"`)
			io.WriteString(w, sample)
		case r.Method == "PUT":
			if string(data) != sample {
				t.Error("raw PUT changed")
			}
			lastBody = string(data)
			w.Header().Set("ETag", `"two"`)
			if putCount.Add(1) == 1 {
				if r.Header.Get("If-None-Match") != "*" || r.Header.Get("If-Match") != "" {
					t.Error("create condition")
				}
				w.WriteHeader(201)
			} else {
				if r.Header.Get("If-Match") != `"one"` || r.Header.Get("If-None-Match") != "" {
					t.Error("update condition")
				}
				w.WriteHeader(204)
			}
		case r.Method == "DELETE":
			if r.Header.Get("If-Match") != `"two"` {
				t.Error("delete condition")
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer s.Close()
	base := request{Protocol: 1, Endpoint: s.URL + "/", Username: "user", Password: "secret"}
	r := base
	r.Operation = "discover"
	v, f := execute(context.Background(), r, serverOptions(s))
	if f != nil {
		t.Fatal(f)
	}
	encoded, _ := json.Marshal(v)
	if !bytes.Contains(encoded, []byte(`"supportedVersions":["3.0","4.0"]`)) || !bytes.Contains(encoded, []byte(`"displayName":"My Contacts"`)) {
		t.Fatal(string(encoded))
	}
	r = base
	r.Operation = "query"
	r.BookURL = s.URL + "/home/book/"
	r.Limit = 2
	r.Version = "3.0"
	r.Filter = &filter{"FN", "世&", "starts-with"}
	v, f = execute(context.Background(), r, serverOptions(s))
	if f != nil {
		t.Fatal(f)
	}
	encoded, _ = json.Marshal(v)
	if !bytes.Contains(encoded, []byte(`"complete":true`)) {
		t.Fatal(string(encoded))
	}
	for _, op := range []string{"get", "create", "update", "delete"} {
		r = base
		r.Operation = op
		r.URL = s.URL + "/home/book/a.vcf"
		r.BookURL = s.URL + "/home/book/"
		switch op {
		case "create":
			r.VCard = sample
		case "update":
			r.VCard = sample
			r.ETag = `"one"`
		case "delete":
			r.ETag = `"two"`
		}
		v, f = execute(context.Background(), r, serverOptions(s))
		if f != nil {
			t.Fatal(op, f)
		}
		if op == "get" && v.(*resource).VCard != sample {
			t.Fatal("GET raw changed")
		}
	}
	if putCount.Load() != 2 || lastBody != sample {
		t.Fatal("mutations not reached")
	}
}

func TestNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, props, version string
		want                 bool
	}{
		{"three", bookProps("3.0"), "3.0", true}, {"four", bookProps("3.0", "4.0"), "4.0", true},
		{"no-four", bookProps("3.0"), "4.0", false}, {"explicit-four-only", bookProps("4.0"), "3.0", false},
		{"missing-three", `<d:resourcetype><c:addressbook/></d:resourcetype>`, "3.0", true},
		{"missing-four", `<d:resourcetype><c:addressbook/></d:resourcetype>`, "4.0", false},
		{"empty", bookProps(), "3.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PROPFIND" {
					w.WriteHeader(207)
					io.WriteString(w, multi(item("/book/", tc.props)))
					return
				}
				writes.Add(1)
				w.WriteHeader(201)
			}))
			defer s.Close()
			raw := strings.Replace(sample, "VERSION:3.0", "VERSION:"+tc.version, 1)
			_, f := execute(context.Background(), request{Operation: "create", Endpoint: s.URL + "/book/a.vcf", VCard: raw}, serverOptions(s))
			if (f == nil) != tc.want || (writes.Load() == 1) != tc.want {
				t.Fatal(f, writes.Load())
			}
		})
	}
}

func TestMultistatusValidation(t *testing.T) {
	valid := multi(item("/book/a.vcf", `<d:getetag>"x"</d:getetag><c:address-data>`+xmlText(sample)+`</c:address-data>`))
	for _, s := range []string{
		strings.Replace(valid, `xmlns:d="DAV:"`, `xmlns:d="wrong"`, 1),
		`<!DOCTYPE x>` + valid, valid + valid,
		strings.Replace(valid, "HTTP/1.1 200 OK", "bogus", 1),
		strings.Replace(valid, "HTTP/1.1 200 OK", "HTTP/1.1 412 Conflict", 1),
		strings.Replace(valid, "<d:getetag>", "<d:getetag/> <d:getetag>", 1),
		strings.Replace(valid, "<d:href>", "<d:href/> <d:href>", 1),
		strings.Replace(valid, "<d:status>", "<wrong:status xmlns:wrong=\"wrong\">", 1),
		strings.Replace(valid, "<d:propstat>", "<d:propstat><d:error/>", 1),
		multi(`<d:response><d:href>/x</d:href><d:status>HTTP/1.1 507 Insufficient Storage</d:status></d:response>`),
		multi(`<d:error><c:number-of-matches-within-limits/></d:error>`),
		strings.Replace(valid, "<d:getetag>", `<d:getetag x="a" x="b">`, 1),
	} {
		if _, f := parseMulti(&response{status: 207, body: []byte(s)}); f == nil {
			t.Errorf("accepted %s", s)
		}
	}
	if _, f := parseMulti(&response{status: 200, body: []byte(valid)}); f == nil {
		t.Fatal("non207 accepted")
	}
	optional := multi(`<d:response><d:href>/book/</d:href><d:propstat><d:prop><d:displayname/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat></d:response>`)
	if _, f := parseMulti(&response{status: 207, body: []byte(optional)}); f != nil {
		t.Fatal(f)
	}
}

func TestQueryFailuresAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limit      int
		want       string
	}{
		{"limit", multi(item("/book/a", `<d:getetag>"x"</d:getetag><c:address-data>`+xmlText(sample)+`</c:address-data>`) + item("/book/b", `<d:getetag>"x"</d:getetag><c:address-data>`+xmlText(sample)+`</c:address-data>`)), 1, "response_limit"},
		{"missing-data", multi(item("/book/a", `<d:getetag>"x"</d:getetag>`)), 2, "invalid_response"},
		{"bad-card", multi(item("/book/a", `<d:getetag>"x"</d:getetag><c:address-data>secret invalid</c:address-data>`)), 2, "invalid_response"},
		{"outside", multi(item("/outside/a", `<d:getetag>"x"</d:getetag><c:address-data>`+xmlText(sample)+`</c:address-data>`)), 2, "unsafe_url"},
		{"cross", multi(item("https://evil.example/a", `<d:getetag>"x"</d:getetag><c:address-data>`+xmlText(sample)+`</c:address-data>`)), 2, "unsafe_url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(207); io.WriteString(w, tc.body) }))
			defer s.Close()
			_, f := execute(context.Background(), request{Operation: "query", Endpoint: s.URL + "/", BookURL: s.URL + "/book/", Limit: tc.limit}, serverOptions(s))
			if f == nil || f.Code != tc.want || strings.Contains(f.Message, "secret") {
				t.Fatal(f)
			}
		})
	}
	for _, f := range []*filter{{"X-UNSUPPORTED", "a", "contains"}, {"FN", "", "contains"}, {"FN", "a", "regex"}, {"FN", strings.Repeat("a", 1025), "equals"}} {
		if _, err := filterXML(f); err == nil {
			t.Fatal("accepted bad filter", f)
		}
	}
}

func TestStatusAndETagFailures(t *testing.T) {
	for status, code := range map[int]string{401: "authentication_failed", 403: "authentication_failed", 404: "not_found", 409: "conflict", 412: "conflict", 429: "rate_limited", 500: "server_error", 507: "response_limit"} {
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, "PRIVATE BODY secret https://secret")
		}))
		_, f := execute(context.Background(), request{Operation: "get", Endpoint: s.URL + "/book/a"}, serverOptions(s))
		s.Close()
		if f == nil || f.Code != code || strings.Contains(f.Message, "PRIVATE") {
			t.Fatal(status, f)
		}
	}
	for _, etag := range []string{"", "*", "W/\"weak\"", "bare", "\"one\"\r\nHeader:x", "\"a\x7fb\""} {
		for _, op := range []string{"update", "delete"} {
			_, f := execute(context.Background(), request{Operation: op, Endpoint: "https://example.org/a", VCard: sample, ETag: etag}, backendOptions{})
			if f == nil || f.Code != "invalid_request" {
				t.Fatal(op, etag, f)
			}
		}
	}
	if !strongETag(`"opaque"`) {
		t.Fatal("strong etag")
	}
}

func TestURLTrustAndSSRF(t *testing.T) {
	bad := []string{"http://example.org/", "https://user:pass@example.org/", "https://example.org/?q=secret", "https://example.org/#x", "https://example.org./", "https://example.org:0/", "https://example.org:99999/", "https://example.org/a/../b", "https://example.org/a%2fb", "https://example.org/a%252fb", "https://example.org/a%5cb", "https://example.org/a%0ab", "https://example.org\\evil/", "file:///etc/passwd"}
	for _, s := range bad {
		if _, f := checkedURL(s); f == nil {
			t.Errorf("accepted %s", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1", "100.64.0.1", "192.0.2.1"} {
		u := "https://" + s + "/a"
		if strings.Contains(s, ":") {
			u = "https://[" + s + "]/a"
		}
		if _, f := newBackend(request{Endpoint: u}, backendOptions{}); f == nil {
			t.Fatal("private accepted", s)
		}
	}
	b, f := newBackend(request{Endpoint: "https://icloud.com/"}, backendOptions{})
	if f != nil {
		t.Fatal(f)
	}
	for _, s := range []string{"https://p1-contacts.icloud.com/a", "https://p123-contacts.icloud.com/a"} {
		u, _ := url.Parse(s)
		if !b.allowed(u, true) || b.allowed(u, false) {
			t.Fatal("bootstrap rule", s)
		}
	}
	for _, s := range []string{"https://p01-caldav.icloud.com/a", "https://p01-contacts.icloud.com.evil/a", "https://p1234-contacts.icloud.com/a", "https://contacts.icloud.com/a", "https://p01-contacts.icloud.com:444/a", "http://p01-contacts.icloud.com/a", "https://icloud.com.evil/a"} {
		u, _ := url.Parse(s)
		if b.allowed(u, true) {
			t.Fatal("unsafe bootstrap", s)
		}
	}
	b.shard = "p01-contacts.icloud.com"
	for _, s := range []string{"https://p02-contacts.icloud.com/a", "https://icloud.com/a"} {
		u, _ := url.Parse(s)
		if b.allowed(u, true) {
			t.Fatal("shard switch", s)
		}
	}
	for _, origin := range []string{"https://contacts.icloud.com/", "https://www.icloud.com/", "https://icloud.com:444/", "https://example.org/"} {
		other, _ := newBackend(request{Endpoint: origin}, backendOptions{})
		u, _ := url.Parse("https://p01-contacts.icloud.com/a")
		if other.allowed(u, true) {
			t.Fatal("bootstrap from nonexact origin", origin)
		}
	}
}

func TestScopedRedirectsTLSAndBodyLimits(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { evilHits.Add(1) }))
	defer evil.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/book/same":
			http.Redirect(w, r, "/book/final", 307)
		case "/book/cross":
			http.Redirect(w, r, evil.URL+"/stolen", 302)
		case "/book/outside":
			http.Redirect(w, r, "/outside/final", 302)
		case "/book/loop":
			http.Redirect(w, r, "/book/loop", 308)
		case "/book/mutate":
			http.Redirect(w, r, "/book/final", 307)
		case "/book/":
			http.Redirect(w, r, "/other/", 307)
		case "/book/large":
			io.Copy(w, io.LimitReader(repeatingReader{}, maxResponse+1))
		case "/book/encoded":
			w.Header().Set("Content-Encoding", "gzip")
			io.WriteString(w, "private")
		default:
			io.WriteString(w, sample)
		}
	}))
	defer s.Close()
	for _, tc := range []struct{ path, want string }{
		{"same", ""}, {"cross", "unsafe_url"}, {"outside", "unsafe_redirect"}, {"loop", "unsafe_redirect"}, {"large", "response_limit"}, {"encoded", "invalid_response"},
	} {
		v, f := execute(context.Background(), request{Operation: "get", Endpoint: s.URL + "/book/" + tc.path}, serverOptions(s))
		if tc.want == "" {
			if f != nil || v.(*resource).VCard != sample {
				t.Fatal(tc, f)
			}
		} else if f == nil || f.Code != tc.want {
			t.Fatal(tc, f)
		}
	}
	if evilHits.Load() != 0 {
		t.Fatal("cross-origin credentials escaped")
	}
	_, f := execute(context.Background(), request{Operation: "delete", Endpoint: s.URL + "/book/mutate", ETag: `"x"`}, serverOptions(s))
	if f == nil || f.Code != "unsafe_redirect" {
		t.Fatal(f)
	}
	_, f = execute(context.Background(), request{Operation: "query", Endpoint: s.URL + "/", BookURL: s.URL + "/book/"}, serverOptions(s))
	if f == nil || f.Code != "unsafe_redirect" {
		t.Fatal(f)
	}
	// No injected roots: the self-signed TLS server must not be trusted.
	b, _ := newBackend(request{Endpoint: s.URL + "/book/final"}, backendOptions{allowPrivate: true})
	// Use a normal verified transport without the production private-IP dial guard.
	b.client.Transport = &http.Transport{}
	_, f = b.do(context.Background(), "GET", b.origin, "", nil, resourceBook(b.origin), false)
	b.client.CloseIdleConnections()
	if f == nil || f.Code != "network_error" {
		t.Fatal("untrusted TLS accepted", f)
	}
}

type repeatingReader struct{}

func (repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
