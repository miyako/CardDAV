package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"time"
	"unicode/utf8"
)

const version = "0.1.2"
const maxInput = 4 << 20
const maxCard = 1 << 20
const maxResponse = 16 << 20
const maxResources = 1000
const requestTimeout = 25 * time.Second
const jsonKeys = " protocol operation endpoint username password bookURL url vcard etag contact version filter limit uid formattedName givenName familyName organization title note emails phones addresses urls value types poBox extended street locality region postalCode country property text matchType "

type fault struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func failure(code, message string) *fault { return &fault{code, message} }
func invalid() *fault                     { return failure("invalid_request", "invalid request") }
func invalidResponse() *fault             { return failure("invalid_response", "invalid server response") }
func limitError() *fault                  { return failure("response_limit", "server response limit exceeded") }

type request struct {
	Protocol  int      `json:"protocol"`
	Operation string   `json:"operation"`
	Endpoint  string   `json:"endpoint"`
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	BookURL   string   `json:"bookURL"`
	URL       string   `json:"url"`
	VCard     string   `json:"vcard"`
	ETag      string   `json:"etag"`
	Contact   *contact `json:"contact"`
	Version   string   `json:"version"`
	Filter    *filter  `json:"filter"`
	Limit     int      `json:"limit"`
}

// Check duplicate keys, explicit nulls and nesting before typed decoding.
func jsonValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return io.ErrUnexpectedEOF
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	if t == nil {
		return io.ErrUnexpectedEOF
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] || !bytes.Contains([]byte(jsonKeys), []byte(" "+s+" ")) {
					return io.ErrUnexpectedEOF
				}
				seen[s] = true
				if e = jsonValue(d, depth+1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := jsonValue(d, depth+1); e != nil {
					return e
				}
			}
		default:
			return io.ErrUnexpectedEOF
		}
		_, e = d.Token()
		return e
	}
	return nil
}

func validUnicodeEscapes(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, e := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func decode(data []byte) (request, *fault) {
	var r request
	if len(data) > maxInput {
		return r, failure("input_limit", "input limit exceeded")
	}
	if !utf8.Valid(data) || !validUnicodeEscapes(data) || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return r, invalid()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if jsonValue(d, 0) != nil {
		return r, invalid()
	}
	if _, err := d.Token(); err != io.EOF {
		return r, invalid()
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || r.Protocol != 1 {
		return r, invalid()
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	allowed := " protocol operation "
	switch r.Operation {
	case "build":
		allowed += "contact version "
	case "parse":
		allowed += "vcard "
	case "discover":
		allowed += "endpoint username password "
	case "query":
		allowed += "endpoint username password bookURL filter limit version "
	case "get":
		allowed += "endpoint username password bookURL url "
	case "create":
		allowed += "endpoint username password bookURL url vcard contact version "
	case "update":
		allowed += "endpoint username password bookURL url vcard etag "
	case "delete":
		allowed += "endpoint username password bookURL url etag "
	default:
		return r, invalid()
	}
	for k := range fields {
		if !bytes.Contains([]byte(allowed), []byte(" "+k+" ")) {
			return r, invalid()
		}
	}
	if r.Operation == "create" {
		_, hasCard := fields["vcard"]
		_, hasContact := fields["contact"]
		_, hasVersion := fields["version"]
		if hasCard == hasContact || (hasCard && hasVersion) {
			return r, invalid()
		}
	}
	return r, nil
}

func emit(w io.Writer, value any) {
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)
	_ = e.Encode(value)
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		emit(out, struct {
			Protocol int    `json:"protocol"`
			Version  string `json:"version"`
		}{1, version})
		return 0
	}
	if len(args) != 1 || args[0] != "request" {
		emit(errOut, struct {
			Protocol int    `json:"protocol"`
			Error    *fault `json:"error"`
		}{1, failure("invalid_arguments", "expected request or --version")})
		return 2
	}
	type outcome struct {
		value any
		err   *fault
	}
	done := make(chan outcome, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(in, maxInput+1))
		if err != nil {
			done <- outcome{nil, invalid()}
			return
		}
		r, f := decode(data)
		if f != nil {
			done <- outcome{nil, f}
			return
		}
		v, f := execute(ctx, r, backendOptions{})
		done <- outcome{v, f}
	}()
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		result.err = failure("timeout", "request deadline exceeded")
	}
	if result.err != nil {
		emit(errOut, struct {
			Protocol int    `json:"protocol"`
			Error    *fault `json:"error"`
		}{1, result.err})
		return 1
	}
	emit(out, struct {
		Protocol int `json:"protocol"`
		Result   any `json:"result"`
	}{1, result.value})
	return 0
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
