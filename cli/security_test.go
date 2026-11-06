package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestICloudBootstrapCredentialsAndShardPin(t *testing.T) {
	for _, host := range []string{"p01-contacts.icloud.com", "p02-caldav.icloud.com", "p01-contacts.icloud.com.evil.example"} {
		t.Run(host, func(t *testing.T) {
			hosts := []string{}
			options := backendOptions{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hosts = append(hosts, r.URL.Hostname())
				u, p, ok := r.BasicAuth()
				if !ok || u != "test-user" || p != "test-password" {
					t.Fatal("credentials missing")
				}
				if r.URL.Hostname() == "icloud.com" {
					resp := fakeResponse(r, 301, "")
					resp.Header.Set("Location", "https://"+host+"/")
					return resp, nil
				}
				if r.URL.Hostname() != "p01-contacts.icloud.com" {
					t.Fatal("credential forwarding to untrusted host")
				}
				switch r.URL.Path {
				case "/":
					return fakeResponse(r, 207, multi(item("/", `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`))), nil
				case "/principal/":
					return fakeResponse(r, 207, multi(item("/principal/", `<c:addressbook-home-set><d:href>/home/</d:href></c:addressbook-home-set>`))), nil
				case "/home/":
					return fakeResponse(r, 207, multi(item("/home/book/", bookProps("3.0")))), nil
				default:
					t.Fatal("unexpected bootstrap path")
					return nil, nil
				}
			})}
			v, f := execute(context.Background(), request{Operation: "discover", Endpoint: "https://icloud.com/", Username: "test-user", Password: "test-password"}, options)
			if host == "p01-contacts.icloud.com" {
				if f != nil || v == nil || len(hosts) != 4 {
					t.Fatal(v, f, hosts)
				}
			} else if f == nil || f.Code != "unsafe_url" || len(hosts) != 1 {
				t.Fatal(f, hosts)
			}
		})
	}
	b, _ := newBackend(request{Endpoint: "https://icloud.com/"}, backendOptions{})
	b.shard = "p01-contacts.icloud.com"
	base, _ := url.Parse("https://p01-contacts.icloud.com/")
	for _, href := range []string{"https://icloud.com/", "https://p02-contacts.icloud.com/"} {
		if _, f := b.resolve(base, href, true); f == nil {
			t.Fatal("shard unpinned")
		}
	}
}

func TestDiscoveryHrefFailuresAndOptionalMetadata(t *testing.T) {
	for _, tc := range []struct{ name, principal, home, listing, want string }{
		{"foreign-principal", `<d:current-user-principal><d:href>https://evil.example/principal/</d:href></d:current-user-principal>`, "", "", "unsafe_url"},
		{"missing-principal", `<d:displayname>unknown</d:displayname>`, "", "", "invalid_response"},
		{"duplicate-principal-href", `<d:current-user-principal><d:href>/p/</d:href><d:href>/p/</d:href></d:current-user-principal>`, "", "", "invalid_response"},
		{"foreign-home", `<d:current-user-principal><d:href>/p/</d:href></d:current-user-principal>`, `<c:addressbook-home-set><d:href>https://evil.example/home/</d:href></c:addressbook-home-set>`, "", "unsafe_url"},
		{"duplicate-home", `<d:current-user-principal><d:href>/p/</d:href></d:current-user-principal>`, `<c:addressbook-home-set><d:href>/home/</d:href><d:href>/home/</d:href></c:addressbook-home-set>`, multi(item("/home/", `<d:resourcetype/>`)), "invalid_response"},
		{"foreign-book", `<d:current-user-principal><d:href>/p/</d:href></d:current-user-principal>`, `<c:addressbook-home-set><d:href>/home/</d:href></c:addressbook-home-set>`, multi(item("https://evil.example/book/", bookProps("3.0"))), "unsafe_url"},
		{"outside-book", `<d:current-user-principal><d:href>/p/</d:href></d:current-user-principal>`, `<c:addressbook-home-set><d:href>/home/</d:href></c:addressbook-home-set>`, multi(item("/outside/book/", bookProps("3.0"))), "unsafe_url"},
		{"missing-type", `<d:current-user-principal><d:href>/p/</d:href></d:current-user-principal>`, `<c:addressbook-home-set><d:href>/home/</d:href></c:addressbook-home-set>`, multi(item("/home/book/", `<d:displayname>Book</d:displayname>`)), "invalid_response"},
		{"metadata-404", `<d:current-user-principal><d:href>/p/</d:href></d:current-user-principal>`, `<c:addressbook-home-set><d:href>/home/</d:href></c:addressbook-home-set>`,
			multi(`<d:response><d:href>/home/book/</d:href><d:propstat><d:prop><d:resourcetype><c:addressbook/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat><d:propstat><d:prop><d:displayname/><c:supported-address-data/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat></d:response>`), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := backendOptions{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/":
					return fakeResponse(r, 207, multi(item("/", tc.principal))), nil
				case "/p/":
					return fakeResponse(r, 207, multi(item("/p/", tc.home))), nil
				case "/home/":
					return fakeResponse(r, 207, tc.listing), nil
				default:
					t.Fatal("unexpected URL", r.URL)
					return nil, nil
				}
			})}
			v, f := execute(context.Background(), request{Operation: "discover", Endpoint: "https://example.org/"}, options)
			if tc.want == "" {
				if f != nil || v == nil {
					t.Fatal(v, f)
				}
			} else if f == nil || f.Code != tc.want {
				t.Fatal(f)
			}
		})
	}
}

func TestXMLResourceAndAggregateLimits(t *testing.T) {
	for _, raw := range []string{
		`<d:multistatus xmlns:d="DAV:">` + strings.Repeat("<a>", 25) + strings.Repeat("</a>", 25) + `</d:multistatus>`,
		multi(strings.Repeat(item("/a", `<d:getetag>"x"</d:getetag>`), maxResources+1)),
		`<d:multistatus xmlns:d="DAV:">` + strings.Repeat("<a/>", 100000) + `</d:multistatus>`,
	} {
		if _, f := parseMulti(&response{status: 207, body: []byte(raw)}); f == nil || f.Code != "response_limit" {
			t.Fatal(f)
		}
	}
	b, _ := newBackend(request{Endpoint: "https://example.org/"}, backendOptions{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return fakeResponse(r, 200, "12345"), nil })})
	b.budget = 9
	if _, f := b.do(context.Background(), "GET", b.origin, "", nil, nil, false); f != nil {
		t.Fatal(f)
	}
	if _, f := b.do(context.Background(), "GET", b.origin, "", nil, nil, false); f == nil || f.Code != "response_limit" {
		t.Fatal(f)
	}
	b.budget = maxResponse
	b.calls = 16
	if _, f := b.do(context.Background(), "GET", b.origin, "", nil, nil, false); f == nil || f.Code != "response_limit" {
		t.Fatal(f)
	}
}

func TestNetworkDeadlineAndPrivateDNS(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, f := execute(ctx, request{Operation: "get", Endpoint: s.URL + "/book/a"}, serverOptions(s))
	if f == nil || f.Code != "timeout" {
		t.Fatal(f)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, e := safeDial(ctx, "tcp", "localhost:443"); e == nil {
		conn.Close()
		t.Fatal("private DNS reached network")
	}
	if !publicIP(net.ParseIP("8.8.8.8")) || !publicIP(net.ParseIP("2606:4700:4700::1111")) {
		t.Fatal("public IP rejected")
	}
	for _, ip := range []string{"64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1", "2001::1", "::ffff:127.0.0.1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Fatal(ip)
		}
	}
}

func TestBuiltContactCreateAndAllMutationRedirects(t *testing.T) {
	var puts atomic.Int32
	for _, status := range []int{301, 302, 303, 307, 308} {
		options := backendOptions{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == "PROPFIND" {
				return fakeResponse(r, 207, multi(item("/book/", bookProps("3.0", "4.0")))), nil
			}

			if r.Method == "PUT" {
				puts.Add(1)
				raw, _ := io.ReadAll(r.Body)
				card, f := parseCard(string(raw))
				if f != nil || card.Version != "4.0" || card.UID != "stable" {
					t.Fatal(card, f)
				}
				resp := fakeResponse(r, status, "")
				resp.Header.Set("Location", "/book/redirected")
				return resp, nil
			}
			if r.Method == "DELETE" {
				resp := fakeResponse(r, status, "")
				resp.Header.Set("Location", "/book/redirected")
				return resp, nil
			}
			t.Fatal("redirect followed")
			return nil, nil
		})}
		for _, op := range []string{"create", "delete"} {
			r := request{Operation: op, Endpoint: "https://example.org/book/a"}
			if op == "create" {
				r.Contact = &contact{UID: "stable", FormattedName: "Name"}
				r.Version = "4.0"
			} else {
				r.ETag = `"x"`
			}
			if _, f := execute(context.Background(), r, options); f == nil || f.Code != "unsafe_redirect" {
				t.Fatal(op, status, f)
			}
		}
	}
	if puts.Load() != 5 {
		t.Fatal(puts.Load())
	}
}

func TestMutationConflictIsNotRetried(t *testing.T) {
	for _, op := range []string{"create", "update", "delete"} {
		var attempts int
		options := backendOptions{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == "PROPFIND" {
				return fakeResponse(r, 207, multi(item("/book/", bookProps("3.0")))), nil
			}
			attempts++
			if op == "create" && r.Header.Get("If-None-Match") != "*" {
				t.Fatal("missing create guard")
			}
			if op != "create" && r.Header.Get("If-Match") != `"old"` {
				t.Fatal("missing mutation guard")
			}
			return fakeResponse(r, 412, "PRIVATE CONTACT"), nil
		})}
		r := request{Operation: op, Endpoint: "https://example.org/book/a"}
		if op != "delete" {
			r.VCard = sample
		}
		if op != "create" {
			r.ETag = `"old"`
		}
		_, f := execute(context.Background(), r, options)
		if f == nil || f.Code != "conflict" || attempts != 1 || strings.Contains(f.Message, "PRIVATE") {
			t.Fatal(op, f, attempts)
		}
	}

}

func TestRelativeHrefScopeAndEncodedTraversal(t *testing.T) {
	b, f := newBackend(request{Endpoint: "https://example.org/book/"}, backendOptions{})
	if f != nil {
		t.Fatal(f)
	}
	for _, origin := range []string{"https://example.org/", "https://p01-contacts.icloud.com/"} {
		direct, f := newBackend(request{Endpoint: origin}, backendOptions{})
		if f != nil {
			t.Fatal(f)
		}
		for _, href := range []string{"https://p02-contacts.icloud.com/book/a.vcf", "https://icloud.com/book/a.vcf"} {
			if _, f := direct.resolve(direct.origin, href, false); f == nil {
				t.Fatal("cross-origin href accepted without bootstrap", origin, href)
			}
		}
	}
	u, f := b.resolve(b.origin, "a.vcf", false)
	if f != nil || u.String() != "https://example.org/book/a.vcf" || !childOf(b.origin, u) {
		t.Fatal(u, f)
	}
	for _, href := range []string{"%2e%2e/outside/a.vcf", "%252e%252e/a.vcf", "a%2fb.vcf", "a%5cb.vcf", "?private=value", "//evil.example/a.vcf"} {
		if u, f := b.resolve(b.origin, href, false); f == nil {
			t.Fatal("unsafe href accepted", href, u)
		}
	}
	for _, href := range []string{"../outside/a.vcf", "../other-book/a.vcf", "nested/a.vcf"} {
		u, f := b.resolve(b.origin, href, false)
		if f == nil && childOf(b.origin, u) {
			t.Fatal("outside href accepted", href, u)
		}
	}
}
