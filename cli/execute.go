package main

import (
	"context"
	"strings"
)

func strongETag(s string) bool {
	if len(s) < 2 || len(s) > 1024 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	for _, c := range []byte(s[1 : len(s)-1]) {
		if c < 0x21 || c == 0x22 || c >= 0x7f {
			return false
		}
	}
	return true
}
func execute(ctx context.Context, r request, options backendOptions) (any, *fault) {
	switch r.Operation {
	case "build":
		return buildCard(r.Contact, r.Version)
	case "parse":
		return parseCard(r.VCard)
	case "discover", "query", "get", "create", "update", "delete":
	default:
		return nil, invalid()
	}
	if r.Version != "" && r.Version != "3.0" && r.Version != "4.0" {
		return nil, failure("unsupported_version", "unsupported vCard version")
	}
	if r.Operation == "update" || r.Operation == "delete" {
		if !strongETag(r.ETag) {
			return nil, invalid()
		}
	}
	if r.Operation == "create" || r.Operation == "update" {
		if r.Contact == nil && r.Version != "" {
			return nil, invalid()
		}
		if r.Contact != nil {
			if r.VCard != "" || r.Operation != "create" {
				return nil, invalid()
			}
			built, f := buildCard(r.Contact, r.Version)
			if f != nil {
				return nil, f
			}
			// The builder's raw output is the only representation sent.
			r.VCard = built.(builtCard).VCard
		}
		if _, f := parseCard(r.VCard); f != nil {
			return nil, f
		}
	}
	if r.Operation == "query" {
		if r.BookURL == "" || r.Limit < 0 || r.Limit > maxResources {
			return nil, invalid()
		}
		if _, f := filterXML(r.Filter); f != nil {
			return nil, f
		}
	}
	b, f := newBackend(r, options)
	if f != nil {
		return nil, f
	}
	defer b.client.CloseIdleConnections()
	if r.Operation == "discover" {
		return b.discover(ctx)
	}
	target := b.origin
	if r.URL != "" {
		u, f := checkedURL(r.URL)
		if f != nil || !sameOrigin(b.origin, u) {
			return nil, failure("unsafe_url", "unsafe server URL")
		}
		target = u
	}
	book := resourceBook(target)
	if r.BookURL != "" {
		u, f := checkedURL(r.BookURL)
		if f != nil || !sameOrigin(b.origin, u) || !strings.HasSuffix(u.Path, "/") {
			return nil, failure("unsafe_url", "unsafe server URL")
		}
		book = u
	}
	if r.Operation == "query" {
		return b.query(ctx, book, r)
	}
	if strings.HasSuffix(target.Path, "/") || !childOf(book, target) {
		return nil, invalid()
	}
	switch r.Operation {
	case "get":
		resp, f := b.do(ctx, "GET", target, "", map[string]string{"Accept": "text/vcard"}, book, false)
		if f != nil {
			return nil, f
		}
		if resp.status != 200 {
			return nil, invalidResponse()
		}
		return resourceFrom(resp.url, resp.header.Get("ETag"), string(resp.body))
	case "create", "update":
		card, _ := parseCard(r.VCard)
		if f := b.requireVersion(ctx, book, card.Version); f != nil {
			return nil, f
		}
		headers := map[string]string{"Content-Type": "text/vcard; charset=utf-8"}
		if r.Operation == "create" {
			headers["If-None-Match"] = "*"
		} else {
			headers["If-Match"] = r.ETag
		}
		resp, f := b.do(ctx, "PUT", target, r.VCard, headers, book, false)
		if f != nil {
			return nil, f
		}
		if (r.Operation == "create" && resp.status != 201) || (r.Operation == "update" && resp.status != 200 && resp.status != 204) {
			return nil, invalidResponse()
		}
		return struct {
			URL  string `json:"url"`
			ETag string `json:"etag"`
		}{resp.url.String(), resp.header.Get("ETag")}, nil
	case "delete":
		resp, f := b.do(ctx, "DELETE", target, "", map[string]string{"If-Match": r.ETag}, book, false)
		if f != nil {
			return nil, f
		}
		if resp.status != 200 && resp.status != 204 {
			return nil, invalidResponse()
		}
		return struct {
			Deleted bool `json:"deleted"`
		}{true}, nil
	}
	return nil, invalid()
}
