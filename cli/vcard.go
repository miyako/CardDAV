package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	vcard "github.com/emersion/go-vcard"
)

type typedValue struct {
	Value string   `json:"value"`
	Types []string `json:"types"`
}
type address struct {
	Types      []string `json:"types"`
	POBox      string   `json:"poBox"`
	Extended   string   `json:"extended"`
	Street     string   `json:"street"`
	Locality   string   `json:"locality"`
	Region     string   `json:"region"`
	PostalCode string   `json:"postalCode"`
	Country    string   `json:"country"`
}
type contact struct {
	UID           string       `json:"uid"`
	FormattedName string       `json:"formattedName"`
	GivenName     string       `json:"givenName"`
	FamilyName    string       `json:"familyName"`
	Organization  string       `json:"organization"`
	Title         string       `json:"title"`
	Note          string       `json:"note"`
	Emails        []typedValue `json:"emails"`
	Phones        []typedValue `json:"phones"`
	Addresses     []address    `json:"addresses"`
	URLs          []string     `json:"urls"`
}
type property struct {
	Name       string       `json:"name"`
	Group      string       `json:"group"`
	Parameters vcard.Params `json:"parameters"`
	Value      string       `json:"value"`
}
type cardResult struct {
	VCard         string     `json:"vcard"`
	UID           string     `json:"uid"`
	Version       string     `json:"version"`
	FormattedName string     `json:"formattedName"`
	Properties    []property `json:"properties"`
}
type builtCard struct {
	VCard   string `json:"vcard"`
	UID     string `json:"uid"`
	Version string `json:"version"`
}

var tokenName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func cleanText(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}
func escapeText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", ";", "\\;", ",", "\\,").Replace(s)
}
func unescapeText(s string) string {
	return strings.NewReplacer("\\\\", "\\", "\\n", "\n", "\\N", "\n", "\\;", ";", "\\,", ",").Replace(s)
}
func splitQuoted(s string, separator byte) ([]string, bool) {
	parts := []string{}
	start, quoted := 0, false
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			quoted = !quoted
		}
		if s[i] == separator && !quoted {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	if quoted {
		return nil, false
	}
	return append(parts, s[start:]), true
}

func parseParameters(header string) (vcard.Params, bool) {
	parts, ok := splitQuoted(header, ';')
	if !ok {
		return nil, false
	}
	params := vcard.Params{}
	for _, part := range parts[1:] {
		key, value, found := strings.Cut(part, "=")
		if !found || !tokenName.MatchString(key) {
			return nil, false
		}
		values, ok := splitQuoted(value, ',')
		if !ok {
			return nil, false
		}
		for _, v := range values {
			if strings.HasPrefix(v, `"`) {
				if len(v) < 2 || !strings.HasSuffix(v, `"`) {
					return nil, false
				}
				v = v[1 : len(v)-1]
				if strings.ContainsRune(v, '"') {
					return nil, false
				}
			} else if strings.ContainsAny(v, `":;`) {
				return nil, false
			}
			if !cleanText(v) || strings.ContainsAny(v, "\n\t") {
				return nil, false
			}
			// RFC 6868 parameter escaping supplements the dependency decoder.
			v = strings.NewReplacer("^^", "^", "^n", "\n", "^N", "\n", "^'", `"`).Replace(v)
			key = strings.ToUpper(key)
			params[key] = append(params[key], v)
		}
	}
	return params, true
}
func foldLine(line string) string {
	var b strings.Builder
	n := 0
	for _, r := range line {
		size := utf8.RuneLen(r)
		if n+size > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += size
	}
	b.WriteString("\r\n")
	return b.String()
}

func buildCard(c *contact, ver string) (any, *fault) {
	if c == nil || c.FormattedName == "" {
		return nil, invalid()
	}
	if ver == "" {
		ver = "3.0"
	}
	if ver != "3.0" && ver != "4.0" {
		return nil, failure("unsupported_version", "unsupported vCard version")
	}
	uid := c.UID
	if uid == "" {
		var id [16]byte
		if _, e := rand.Read(id[:]); e != nil {
			return nil, failure("entropy_error", "identifier generation failed")
		}
		id[6] = (id[6] & 15) | 64
		id[8] = (id[8] & 63) | 128
		s := hex.EncodeToString(id[:])
		uid = "urn:uuid:" + s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	}
	lines := []string{"BEGIN:VCARD", "VERSION:" + ver}
	add := func(k, value string) bool {
		if !cleanText(value) {
			return false
		}
		lines = append(lines, k+":"+escapeText(value))
		return true
	}
	uidKey := "UID"
	if ver == "4.0" {
		u, err := url.Parse(uid)
		if err != nil || u.Scheme == "" {
			uidKey += ";VALUE=text"
		}
	}
	if !add(uidKey, uid) || !add("FN", c.FormattedName) {
		return nil, invalid()
	}
	for _, s := range []string{c.FamilyName, c.GivenName} {
		if !cleanText(s) {
			return nil, invalid()
		}
	}
	lines = append(lines, "N:"+escapeText(c.FamilyName)+";"+escapeText(c.GivenName)+";;;")
	for _, pair := range [][2]string{{"ORG", c.Organization}, {"TITLE", c.Title}, {"NOTE", c.Note}} {
		if pair[1] != "" && !add(pair[0], pair[1]) {
			return nil, invalid()
		}
	}
	for _, group := range []struct {
		name   string
		values []typedValue
	}{{"EMAIL", c.Emails}, {"TEL", c.Phones}} {
		for _, v := range group.values {
			params, ok := typeParams(v.Types)
			if ver == "4.0" && group.name == "TEL" {
				params += ";VALUE=text"
			}
			if !ok || v.Value == "" || !add(group.name+params, v.Value) {
				return nil, invalid()
			}
		}
	}
	for _, a := range c.Addresses {
		params, ok := typeParams(a.Types)
		if !ok {
			return nil, invalid()
		}
		parts := []string{a.POBox, a.Extended, a.Street, a.Locality, a.Region, a.PostalCode, a.Country}
		for i, s := range parts {
			if !cleanText(s) {
				return nil, invalid()
			}
			parts[i] = escapeText(s)
		}
		lines = append(lines, "ADR"+params+":"+strings.Join(parts, ";"))
	}
	for _, s := range c.URLs {
		u, err := url.Parse(s)
		if !cleanText(s) || s == "" || strings.ContainsAny(s, "\n\t ") || err != nil || u.Scheme == "" {
			return nil, invalid()
		}
		// URI values are not vCard TEXT and must not be backslash escaped.
		lines = append(lines, "URL:"+s)
	}
	lines = append(lines, "END:VCARD")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(foldLine(line))
	}
	if b.Len() > maxCard {
		return nil, failure("input_limit", "input limit exceeded")
	}
	raw := b.String()
	if _, f := parseCard(raw); f != nil {
		return nil, f
	}
	return builtCard{raw, uid, ver}, nil
}

func typeParams(types []string) (string, bool) {
	if len(types) == 0 {
		return "", true
	}
	for _, t := range types {
		if !tokenName.MatchString(t) {
			return "", false
		}
	}
	return ";TYPE=" + strings.Join(types, ","), true
}

// The dependency's decoder is deliberately permissive. Validate every unfolded
// line first, then use its parameter/group parser without re-encoding raw data.
func parseCard(raw string) (*cardResult, *fault) {
	bad := func() (*cardResult, *fault) { return nil, failure("invalid_vcard", "invalid vCard") }
	if len(raw) == 0 || len(raw) > maxCard || !utf8.ValidString(raw) {
		return bad()
	}
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	if strings.ContainsRune(s, '\r') || !strings.HasSuffix(s, "\n") {
		return bad()
	}
	physical := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	lines := []string{}
	for _, l := range physical {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			if len(lines) == 0 {
				return bad()
			}
			lines[len(lines)-1] += l[1:]
		} else {
			lines = append(lines, l)
		}
	}
	if len(lines) < 4 || len(lines) > 10000 || !strings.EqualFold(lines[0], "BEGIN:VCARD") || !strings.EqualFold(lines[len(lines)-1], "END:VCARD") {
		return bad()
	}
	result := &cardResult{VCard: raw, Properties: []property{}}
	for i, l := range lines[1 : len(lines)-1] {
		if !cleanText(l) {
			return bad()
		}
		// Find the value separator outside quoted parameter values.
		quote, colon := false, -1
		for j, c := range l {
			if c == '"' {
				quote = !quote
			}
			if c == ':' && !quote {
				colon = j
				break
			}
		}
		if colon < 1 {
			return bad()
		}
		header := l[:colon]
		head := strings.Split(header, ";")[0]
		parts := strings.Split(head, ".")
		if len(parts) > 2 {
			return bad()
		}
		for _, p := range parts {
			if !tokenName.MatchString(p) {
				return bad()
			}
		}
		name := strings.ToUpper(parts[len(parts)-1])
		if name == "BEGIN" || name == "END" {
			return bad()
		}
		if i == 0 && name != "VERSION" {
			return bad()
		}
		params, ok := parseParameters(header)
		if !ok {
			return bad()
		}
		// Header grammar is checked independently. The upstream decoder does
		// not support all quoted parameter lists, so decode the bare property.
		dec := vcard.NewDecoder(strings.NewReader("BEGIN:VCARD\r\n" + head + ":" + l[colon+1:] + "\r\nEND:VCARD\r\n"))
		card, e := dec.Decode()
		if e != nil || len(card[name]) != 1 {
			return bad()
		}
		field := card[name][0]
		value := unescapeText(l[colon+1:])
		// URI and extension values remain descriptive; authoritative data is raw.
		if name == "URL" || strings.EqualFold(params.Get("VALUE"), "uri") {
			value = l[colon+1:]
		}
		for k := range params {
			if !tokenName.MatchString(k) {
				return bad()
			}
		}
		result.Properties = append(result.Properties, property{name, field.Group, params, value})
		switch name {
		case "VERSION":
			if result.Version != "" {
				return bad()
			}
			result.Version = value
		case "UID":
			if result.UID != "" || value == "" {
				return bad()
			}
			result.UID = value
		case "FN":
			if result.FormattedName == "" {
				result.FormattedName = value
			}
		}
	}
	if result.Version != "3.0" && result.Version != "4.0" {
		return nil, failure("unsupported_version", "unsupported vCard version")
	}
	if result.UID == "" || result.FormattedName == "" {
		return bad()
	}
	// Verify the full dependency decoder sees exactly one card.
	d := vcard.NewDecoder(bytes.NewBufferString(raw))
	if _, e := d.Decode(); e != nil {
		return bad()
	}
	if _, e := d.Decode(); e != io.EOF {
		return bad()
	}
	return result, nil
}

func sortedVersions(values map[string]bool) []string {
	out := []string{}
	for v := range values {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
