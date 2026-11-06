package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var icloudShard = regexp.MustCompile(`^p[0-9]{1,3}-contacts\.icloud\.com$`)

type backendOptions struct {
	transport    http.RoundTripper
	allowPrivate bool // Internal test injection only; never part of the JSON protocol.
}
type backend struct {
	client             *http.Client
	origin             *url.URL
	username, password string
	shard              string
	budget             int64
	calls              int
}

func checkedURL(raw string) (*url.URL, *fault) {
	if strings.ContainsAny(raw, "\\") || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return nil, invalid()
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return nil, invalid()
	}
	if u.Hostname() == "" || strings.HasSuffix(u.Hostname(), ".") || strings.Contains(u.Hostname(), "%") {
		return nil, invalid()
	}
	for _, c := range u.Hostname() {
		if c > 127 {
			return nil, invalid()
		}
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, invalid()
		}
	}
	if strings.Contains(u.Path, "\\") || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 || strings.Contains(u.Path, "//") {
		return nil, invalid()
	}
	// Encoded separators/dot segments defeat collection containment comparisons.
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(u.EscapedPath()), "%5c") || strings.Contains(u.Path, "%") {
		return nil, invalid()
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, invalid()
		}
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}
func port(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	return "443"
}
func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Hostname() == b.Hostname() && port(a) == port(b)
}
func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16"} {
		if netip.MustParsePrefix(block).Contains(a) {
			return false
		}
	}
	return true
}
func safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, p, e := net.SplitHostPort(addr)
	if e != nil {
		return nil, e
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil {
		return nil, e
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, &net.AddrError{Err: "blocked address", Addr: "redacted"}
		}
	}
	if len(ips) == 0 {
		return nil, &net.AddrError{Err: "no address", Addr: "redacted"}
	}
	// Dial the validated IP, not the hostname (DNS rebinding protection).
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), p))
}
func newBackend(r request, options backendOptions) (*backend, *fault) {
	u, f := checkedURL(r.Endpoint)
	if f != nil {
		return nil, f
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !options.allowPrivate && !publicIP(ip) {
		return nil, failure("unsafe_url", "unsafe server URL")
	}
	if strings.ContainsAny(r.Username, ":\r\n") || strings.ContainsAny(r.Password, "\r\n") || ((r.Username == "") != (r.Password == "")) {
		return nil, invalid()
	}
	tr := options.transport
	if tr == nil {
		tr = &http.Transport{Proxy: nil, DialContext: safeDial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableCompression: true}
	}
	return &backend{client: &http.Client{Transport: tr, Timeout: requestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, origin: u, username: r.Username, password: r.Password, budget: maxResponse}, nil
}
func (b *backend) allowed(u *url.URL, bootstrap bool) bool {
	if sameOrigin(b.origin, u) {
		return b.shard == "" || u.Hostname() == b.shard
	}
	return bootstrap && b.origin.Hostname() == "icloud.com" && port(b.origin) == "443" && u.Scheme == "https" && port(u) == "443" && icloudShard.MatchString(u.Hostname()) && (b.shard == "" || b.shard == u.Hostname())
}
func (b *backend) resolve(base *url.URL, raw string, bootstrap bool) (*url.URL, *fault) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return nil, failure("unsafe_url", "unsafe server URL")
	}
	ref, e := url.Parse(raw)
	if e != nil || ref.User != nil || ref.Fragment != "" {
		return nil, failure("unsafe_url", "unsafe server URL")
	}
	u, f := checkedURL(base.ResolveReference(ref).String())
	if f != nil || !b.allowed(u, bootstrap) {
		return nil, failure("unsafe_url", "unsafe server URL")
	}
	return u, nil
}
func childOf(book, u *url.URL) bool {
	return sameOrigin(book, u) && strings.HasSuffix(book.Path, "/") && strings.HasPrefix(u.Path, book.Path) && u.Path != book.Path && !strings.Contains(strings.TrimPrefix(u.Path, book.Path), "/")
}

type response struct {
	url    *url.URL
	status int
	header http.Header
	body   []byte
}

func statusFault(status int) *fault {
	code := "server_error"
	switch status {
	case 401, 403:
		code = "authentication_failed"
	case 404:
		code = "not_found"
	case 409, 412:
		code = "conflict"
	case 429:
		code = "rate_limited"
	case 507:
		code = "response_limit"
	}
	return failure(code, "server rejected request")
}
func (b *backend) do(ctx context.Context, method string, u *url.URL, body string, headers map[string]string, scope *url.URL, bootstrap bool) (*response, *fault) {
	for redirects := 0; ; redirects++ {
		if !b.allowed(u, bootstrap) {
			return nil, failure("unsafe_url", "unsafe server URL")
		}
		if u.Hostname() != b.origin.Hostname() {
			b.shard = u.Hostname()
		}
		b.calls++
		if b.calls > 16 {
			return nil, limitError()
		}
		req, e := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
		if e != nil {
			return nil, invalid()
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("User-Agent", "CardDAV/"+version)
		if b.username != "" {
			req.SetBasicAuth(b.username, b.password)
		}
		resp, e := b.client.Do(req)
		if e != nil {
			if ctx.Err() != nil {
				return nil, failure("timeout", "request deadline exceeded")
			}
			return nil, failure("network_error", "network request failed")
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			resp.Body.Close()
			if redirects >= 5 || method == "PUT" || method == "DELETE" || resp.StatusCode == 303 || (resp.StatusCode != 301 && resp.StatusCode != 302 && resp.StatusCode != 307 && resp.StatusCode != 308) {
				return nil, failure("unsafe_redirect", "redirect rejected")
			}
			next, f := b.resolve(u, resp.Header.Get("Location"), bootstrap)
			if f != nil {
				return nil, f
			}
			if scope != nil {
				if method == "REPORT" && (next.Path != scope.Path || !sameOrigin(next, scope)) {
					return nil, failure("unsafe_redirect", "redirect rejected")
				}
				if method == "GET" && !childOf(scope, next) {
					return nil, failure("unsafe_redirect", "redirect rejected")
				}
			}
			u = next
			continue
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, statusFault(resp.StatusCode)
		}
		if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
			resp.Body.Close()
			return nil, invalidResponse()
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, b.budget+1))
		resp.Body.Close()
		if e != nil {
			if ctx.Err() != nil {
				return nil, failure("timeout", "request deadline exceeded")
			}
			return nil, failure("network_error", "network request failed")
		}
		b.budget -= int64(len(data))
		if b.budget < 0 {
			return nil, limitError()
		}
		return &response{u, resp.StatusCode, resp.Header, data}, nil
	}
}
func resourceBook(u *url.URL) *url.URL {
	copy := *u
	copy.Path = path.Dir(u.Path) + "/"
	if copy.Path == "//" {
		copy.Path = "/"
	}
	copy.RawPath = ""
	return &copy
}
