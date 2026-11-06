package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const dav = "DAV:"
const carddav = "urn:ietf:params:xml:ns:carddav"

// A small namespace-aware tree lets us reject duplicate and misnamespaced
// protocol elements rather than encoding/xml silently dropping them.
type node struct {
	name     xml.Name
	attrs    []xml.Attr
	text     string
	children []*node
}

func (n *node) all(space, name string) []*node {
	out := []*node{}
	for _, c := range n.children {
		if c.name == (xml.Name{Space: space, Local: name}) {
			out = append(out, c)
		}
	}
	return out
}
func (n *node) one(space, name string) *node {
	a := n.all(space, name)
	if len(a) == 1 {
		return a[0]
	}
	return nil
}
func (n *node) attr(name string) string {
	for _, a := range n.attrs {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func parseXML(raw []byte) (*node, *fault) {
	if !utf8.Valid(raw) {
		return nil, invalidResponse()
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	stack := []*node{}
	var root *node
	count := 0
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, invalidResponse()
		}
		switch v := t.(type) {
		case xml.StartElement:
			attrs := map[xml.Name]bool{}
			for _, a := range v.Attr {
				if attrs[a.Name] {
					return nil, invalidResponse()
				}
				attrs[a.Name] = true
			}
			count++
			if len(stack) >= 24 || count > 100000 {
				return nil, limitError()
			}
			n := &node{name: v.Name, attrs: v.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, invalidResponse()
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, invalidResponse()
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if len(bytes.TrimSpace(v)) != 0 {
					return nil, invalidResponse()
				}
			} else {
				stack[len(stack)-1].text += string(v)
			}
		case xml.Directive:
			return nil, invalidResponse()
		case xml.ProcInst:
			if v.Target != "xml" || root != nil {
				return nil, invalidResponse()
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, invalidResponse()
	}
	return root, nil
}
func parseStatus(s string) int {
	p := strings.Fields(s)
	if len(p) < 2 || (p[0] != "HTTP/1.1" && p[0] != "HTTP/1.0" && p[0] != "HTTP/2") || len(p[1]) != 3 {
		return 0
	}
	n, e := strconv.Atoi(p[1])
	if e != nil || n < 100 || n > 599 {
		return 0
	}
	return n
}

type davItem struct {
	href  string
	props map[xml.Name]*node
}

func parseMulti(resp *response) ([]davItem, *fault) {
	if resp.status != 207 {
		return nil, invalidResponse()
	}
	root, f := parseXML(resp.body)
	if f != nil {
		return nil, f
	}
	if root.name != (xml.Name{Space: dav, Local: "multistatus"}) || strings.TrimSpace(root.text) != "" {
		return nil, invalidResponse()
	}
	items := []davItem{}
	for _, n := range root.children {
		if n.name != (xml.Name{Space: dav, Local: "response"}) {
			return nil, failure("multistatus_error", "server reported resource failure")
		}
		if strings.TrimSpace(n.text) != "" {
			return nil, invalidResponse()
		}
		h := n.one(dav, "href")
		if h == nil || len(h.children) != 0 || h.text == "" || len(n.all(dav, "status")) > 1 {
			return nil, invalidResponse()
		}
		if s := n.one(dav, "status"); s != nil {
			if len(s.children) != 0 {
				return nil, invalidResponse()
			}
			status := parseStatus(s.text)
			if status == 0 {
				return nil, invalidResponse()
			}
			if status/100 != 2 {
				return nil, failure("multistatus_error", "server reported resource failure")
			}
		}
		item := davItem{href: h.text, props: map[xml.Name]*node{}}
		seen := map[xml.Name]bool{}
		ps := n.all(dav, "propstat")
		if len(ps) == 0 {
			return nil, invalidResponse()
		}
		for _, c := range n.children {
			if c.name.Space != dav || (c.name.Local != "href" && c.name.Local != "status" && c.name.Local != "propstat" && c.name.Local != "responsedescription") {
				return nil, invalidResponse()
			}
		}
		for _, p := range ps {
			if strings.TrimSpace(p.text) != "" {
				return nil, invalidResponse()
			}
			for _, c := range p.children {
				if c.name.Space != dav || (c.name.Local != "prop" && c.name.Local != "status" && c.name.Local != "responsedescription") {
					return nil, invalidResponse()
				}
			}
			prop, s := p.one(dav, "prop"), p.one(dav, "status")
			if prop == nil || s == nil {
				return nil, invalidResponse()
			}
			if len(s.children) != 0 || strings.TrimSpace(prop.text) != "" {
				return nil, invalidResponse()
			}
			status := parseStatus(s.text)
			if status == 0 {
				return nil, invalidResponse()
			}
			for _, v := range prop.children {
				if seen[v.name] {
					return nil, invalidResponse()
				}
				seen[v.name] = true
				if status/100 != 2 {
					optional := v.name == (xml.Name{Space: dav, Local: "displayname"}) || v.name == (xml.Name{Space: carddav, Local: "supported-address-data"})
					if status != 404 || !optional {
						return nil, failure("multistatus_error", "server reported property failure")
					}
				} else {
					item.props[v.name] = v
				}
			}
			if status/100 != 2 && len(prop.children) == 0 {
				return nil, failure("multistatus_error", "server reported property failure")
			}
		}
		items = append(items, item)
	}
	if len(items) > maxResources {
		return nil, limitError()
	}
	return items, nil
}
func (i davItem) prop(space, name string) *node { return i.props[xml.Name{Space: space, Local: name}] }
func (b *backend) propfind(ctx context.Context, u *url.URL, depth, props string, bootstrap bool) (*response, []davItem, *fault) {
	body := `<?xml version="1.0" encoding="UTF-8"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop>` + props + `</d:prop></d:propfind>`
	resp, f := b.do(ctx, "PROPFIND", u, body, map[string]string{"Depth": depth, "Content-Type": "application/xml; charset=utf-8"}, nil, bootstrap)
	if f != nil {
		return nil, nil, f
	}
	items, f := parseMulti(resp)
	return resp, items, f
}

type addressbook struct {
	URL               string   `json:"url"`
	DisplayName       string   `json:"displayName"`
	SupportedVersions []string `json:"supportedVersions"`
}

func versionsFrom(n *node) ([]string, *fault) {
	supported := map[string]bool{}
	if n != nil {
		if strings.TrimSpace(n.text) != "" {
			return nil, invalidResponse()
		}
		for _, a := range n.children {
			if a.name != (xml.Name{Space: carddav, Local: "address-data-type"}) || a.attr("content-type") == "" || a.attr("version") == "" || len(a.children) != 0 || strings.TrimSpace(a.text) != "" {
				return nil, invalidResponse()
			}
			if strings.EqualFold(a.attr("content-type"), "text/vcard") {
				supported[a.attr("version")] = true
			}
		}
	}
	return sortedVersions(supported), nil
}
func (b *backend) discover(ctx context.Context) (any, *fault) {
	resp, items, f := b.propfind(ctx, b.origin, "0", `<d:current-user-principal/>`, true)
	if f != nil {
		return nil, f
	}
	if len(items) != 1 {
		return nil, invalidResponse()
	}
	if _, f := b.resolve(resp.url, items[0].href, true); f != nil {
		return nil, f
	}
	p := items[0].prop(dav, "current-user-principal")
	if p == nil || p.one(dav, "href") == nil || len(p.children) != 1 || len(p.one(dav, "href").children) != 0 || strings.TrimSpace(p.text) != "" {
		return nil, invalidResponse()
	}
	principal, f := b.resolve(resp.url, p.one(dav, "href").text, true)
	if f != nil {
		return nil, f
	}
	resp, items, f = b.propfind(ctx, principal, "0", `<c:addressbook-home-set/>`, true)
	if f != nil {
		return nil, f
	}
	if len(items) != 1 {
		return nil, invalidResponse()
	}
	if _, f := b.resolve(resp.url, items[0].href, true); f != nil {
		return nil, f
	}
	home := items[0].prop(carddav, "addressbook-home-set")
	if home == nil || len(home.children) == 0 || len(home.children) > 8 || strings.TrimSpace(home.text) != "" {
		return nil, invalidResponse()
	}
	homes := []string{}
	books := []addressbook{}
	seen := map[string]bool{}
	for _, h := range home.children {
		if h.name != (xml.Name{Space: dav, Local: "href"}) || len(h.children) != 0 {
			return nil, invalidResponse()
		}
		u, f := b.resolve(resp.url, h.text, true)
		if f != nil {
			return nil, f
		}
		if !strings.HasSuffix(u.Path, "/") || seen[u.String()] {
			return nil, invalidResponse()
		}
		seen[u.String()] = true
		homes = append(homes, u.String())
		listResp, list, f := b.propfind(ctx, u, "1", `<d:resourcetype/><d:displayname/><c:supported-address-data/>`, true)
		if f != nil {
			return nil, f
		}
		for _, item := range list {
			target, f := b.resolve(listResp.url, item.href, true)
			if f != nil {
				return nil, f
			}
			if !sameOrigin(u, target) || !strings.HasPrefix(target.Path, u.Path) {
				return nil, failure("unsafe_url", "unsafe server URL")
			}
			rt := item.prop(dav, "resourcetype")
			if rt == nil {
				return nil, invalidResponse()
			}
			if len(rt.all(carddav, "addressbook")) == 0 {
				continue
			}
			if !strings.HasSuffix(target.Path, "/") || seen["book:"+target.String()] {
				return nil, invalidResponse()
			}
			seen["book:"+target.String()] = true
			versions, f := versionsFrom(item.prop(carddav, "supported-address-data"))
			if f != nil {
				return nil, f
			}
			name := ""
			if n := item.prop(dav, "displayname"); n != nil {
				if len(n.children) != 0 {
					return nil, invalidResponse()
				}
				name = n.text
			}
			books = append(books, addressbook{target.String(), name, versions})
		}
	}
	return struct {
		Addressbooks []addressbook `json:"addressbooks"`
		PrincipalURL string        `json:"principalURL"`
		HomeURLs     []string      `json:"homeURLs"`
	}{books, principal.String(), homes}, nil
}
func (b *backend) requireVersion(ctx context.Context, book *url.URL, ver string) *fault {
	resp, items, f := b.propfind(ctx, book, "0", `<d:resourcetype/><c:supported-address-data/>`, false)
	if f != nil {
		return f
	}
	if len(items) != 1 {
		return invalidResponse()
	}
	target, f := b.resolve(resp.url, items[0].href, false)
	if f != nil {
		return f
	}
	if target.String() != book.String() {
		return invalidResponse()
	}
	rt := items[0].prop(dav, "resourcetype")
	if rt == nil || len(rt.all(carddav, "addressbook")) != 1 {
		return invalidResponse()
	}
	n := items[0].prop(carddav, "supported-address-data")
	if n == nil {
		// RFC 6352 requires 3.0; absence is not evidence for 4.0 support.
		if ver == "3.0" {
			return nil
		}
		return failure("unsupported_version", "server does not advertise requested vCard version")
	}
	versions, f := versionsFrom(n)
	if f != nil {
		return f
	}
	for _, v := range versions {
		if v == ver {
			return nil
		}
	}
	return failure("unsupported_version", "server does not advertise requested vCard version")
}

type filter struct {
	Property  string `json:"property"`
	Text      string `json:"text"`
	MatchType string `json:"matchType"`
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func filterXML(f *filter) (string, *fault) {
	if f == nil {
		return `<c:filter/>`, nil
	}
	property := strings.ToUpper(f.Property)
	switch property {
	case "FN", "N", "EMAIL", "TEL", "ORG", "UID", "NOTE", "TITLE", "URL":
	default:
		return "", invalid()
	}
	if f.Text == "" || len(f.Text) > 1024 || !cleanText(f.Text) {
		return "", invalid()
	}
	match := f.MatchType
	if match == "" {
		match = "contains"
	}
	switch match {
	case "contains", "equals", "starts-with", "ends-with":
	default:
		return "", invalid()
	}
	return `<c:filter test="allof"><c:prop-filter name="` + property + `"><c:text-match collation="i;unicode-casemap" match-type="` + match + `">` + xmlText(f.Text) + `</c:text-match></c:prop-filter></c:filter>`, nil
}

type resource struct {
	URL  string `json:"url"`
	ETag string `json:"etag"`
	*cardResult
}

func resourceFrom(u *url.URL, etag, raw string) (*resource, *fault) {
	card, f := parseCard(raw)
	if f != nil {
		return nil, invalidResponse()
	}
	return &resource{u.String(), etag, card}, nil
}
func (b *backend) query(ctx context.Context, book *url.URL, r request) (any, *fault) {
	limit := r.Limit
	if limit == 0 {
		limit = maxResources
	}
	if limit < 1 || limit > maxResources {
		return nil, invalid()
	}
	filter, f := filterXML(r.Filter)
	if f != nil {
		return nil, f
	}
	data := `<c:address-data/>`
	if r.Version != "" {
		if f := b.requireVersion(ctx, book, r.Version); f != nil {
			return nil, f
		}
		data = `<c:address-data content-type="text/vcard" version="` + r.Version + `"/>`
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><c:addressbook-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/>` + data + `</d:prop>` + filter + `<c:limit><c:nresults>` + strconv.Itoa(limit+1) + `</c:nresults></c:limit></c:addressbook-query>`
	resp, f := b.do(ctx, "REPORT", book, body, map[string]string{"Depth": "1", "Content-Type": "application/xml; charset=utf-8"}, book, false)
	if f != nil {
		return nil, f
	}
	items, f := parseMulti(resp)
	if f != nil {
		return nil, f
	}
	if len(items) > limit {
		return nil, limitError()
	}
	contacts := []*resource{}
	seen := map[string]bool{}
	for _, item := range items {
		u, f := b.resolve(resp.url, item.href, false)
		if f != nil {
			return nil, f
		}
		if !childOf(book, u) {
			return nil, failure("unsafe_url", "unsafe server URL")
		}
		if seen[u.String()] {
			return nil, invalidResponse()
		}
		seen[u.String()] = true
		data, etag := item.prop(carddav, "address-data"), item.prop(dav, "getetag")
		if data == nil || etag == nil || len(data.children) != 0 || len(etag.children) != 0 {
			return nil, invalidResponse()
		}
		c, f := resourceFrom(u, etag.text, data.text)
		if f != nil {
			return nil, f
		}
		if r.Version != "" && c.Version != r.Version {
			return nil, invalidResponse()
		}
		contacts = append(contacts, c)
	}
	return struct {
		Contacts []*resource `json:"contacts"`
		Complete bool        `json:"complete"`
	}{contacts, true}, nil
}
