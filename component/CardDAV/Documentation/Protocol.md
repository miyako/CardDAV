# CardDAV CLI protocol 1

The bundled Go executable is `carddav`. It does not read accounts, environment
credentials, browser state, keychains, configuration files, or `.netrc`.
No live-account access is required by its tests.

## Process and framing

* `carddav --version` emits exactly `{"protocol":1,"version":"0.1.0"}` plus LF,
  exits 0, and does not read stdin.
* `carddav request` reads **one** UTF-8 JSON object through EOF on stdin.
  Close stdin after writing it. Whitespace before/after that object is allowed;
  JSON Lines, a second object, a BOM, duplicate keys (including nested keys),
  unknown or incorrectly cased fields, explicit null, invalid UTF-8, unpaired
  UTF-16 escapes, incorrect JSON types, and excessive nesting are rejected.
* Success: `{"protocol":1,"result":{...}}` plus LF on stdout, empty stderr,
  exit 0. Only `--version` uses the top-level `version` envelope.
* Failure: `{"protocol":1,"error":{"code":"...","message":"..."}}` plus LF
  on stderr, empty stdout, exit 1. Invalid argv exits 2 with
  `invalid_arguments` / `expected request or --version`.
* JSON string escaping is conventional JSON escaping, not shell escaping.
  Output is compact UTF-8; JSON object key order is not a semantic guarantee.
* There are no command-line options for passwords, tokens, cookies, proxies,
  tracing, insecure TLS, or redirect policies. Do not put credentials in argv.
  Errors never include supplied values, URLs, credentials, response bodies,
  raw parser/transport errors, or private contact data. Successful results
  intentionally contain the requested contact data and resource URLs.

Every request has integer `protocol:1` and `operation`. The following table
lists all other permitted fields. Fields belonging to another operation are
errors even if supplied as empty strings.

| Operation | Fields |
|---|---|
| `discover` | required `endpoint`; optional paired `username`, `password` |
| `query` | required `endpoint`, `bookURL`; optional paired credentials, `filter`, `limit`, `version` |
| `get` | required `endpoint`; optional paired credentials, `url`, `bookURL` |
| `create` | required `endpoint` and exactly one of `vcard`, `contact`; optional paired credentials, `url`, `bookURL`, `version` (for a built contact only) |
| `update` | required `endpoint`, raw `vcard`, strong `etag`; optional paired credentials, `url`, `bookURL` |
| `delete` | required `endpoint`, strong `etag`; optional paired credentials, `url`, `bookURL` |
| `build` | required `contact`; optional `version` |
| `parse` | required `vcard` |

`endpoint` is the explicit HTTPS trust anchor. For single-resource operations,
`url` defaults to `endpoint`; when `url` is supplied it must be absolute and
same-origin as `endpoint`. `bookURL` defaults to the resource's parent directory;
when supplied the resource must be an immediate child of that collection.
For `query`, `bookURL` is required, absolute, same-origin, and ends in `/`.
Single-resource URLs must not end in `/`.

Credentials use HTTP Basic over verified TLS. Omit both for an anonymous server;
otherwise both must be nonempty strings. Usernames cannot contain `:`, CR or LF;
passwords cannot contain CR or LF. No authentication negotiation, OAuth flow,
password storage, cookie jar, or automatic credential lookup is performed.

## Operations and result objects

### Discover

```json
{"protocol":1,"operation":"discover","endpoint":"https://dav.example.org/","username":"user","password":"application-password"}
```

Three read-only DAV stages are used: Depth-0 `current-user-principal`, Depth-0
CardDAV `addressbook-home-set` at that principal, then Depth-1 enumeration of
each home. Multiple home sets are supported, bounded at eight.

```json
{"addressbooks":[{"url":"https://dav.example.org/home/book/","displayName":"Contacts","supportedVersions":["3.0","4.0"]}],"principalURL":"https://dav.example.org/principal/","homeURLs":["https://dav.example.org/home/"]}
```

Only resources with an exact CardDAV `addressbook` resourcetype are returned.
`supportedVersions` reflects `supported-address-data` entries whose content type
is `text/vcard`, sorted and deduplicated. It is `[]` if the server does not
advertise that property, not an invented assertion of vCard 4 support. Other
advertised version strings are preserved, though the engine only implements
3.0 and 4.0. Missing display name becomes `""`. Empty address-book enumeration
is a valid empty result. Missing principal/home properties fail.

### Query

```json
{"protocol":1,"operation":"query","endpoint":"https://dav.example.org/","bookURL":"https://dav.example.org/home/book/","filter":{"property":"FN","text":"café","matchType":"contains"},"limit":100}
```

Results:

```json
{"contacts":[{"url":"https://dav.example.org/home/book/a.vcf","etag":"\"revision\"","vcard":"BEGIN:VCARD\r\n...","uid":"contact-id","version":"3.0","formattedName":"Name","properties":[]}],"complete":true}
```

This is a standard CardDAV `addressbook-query` REPORT with Depth 1, requesting
`DAV:getetag` and CardDAV `address-data`, with no property projection or photo
stripping. Omitting `version` requests the stored representation, preserving its
data without client-side conversion. Explicit `"3.0"` or `"4.0"` first checks
the book's capabilities and requests that server representation; conversion
may therefore occur **on the server**, not by this CLI. A returned card with a
different version fails.

Filter semantics are deliberately bounded:

* Omit `filter` to match all resources.
* A filter has exactly `property`, nonempty `text`, and optional `matchType`.
  Supported property names, case-insensitive, are `FN`, `N`, `EMAIL`, `TEL`,
  `ORG`, `UID`, `NOTE`, `TITLE`, and `URL`.
* `matchType` is `contains` (default), `equals`, `starts-with`, or `ends-with`,
  case-sensitive. Text is at most 1,024 UTF-8 bytes.
* One `prop-filter` / `text-match` is sent, using standard
  `i;unicode-casemap` collation and filter `test="allof"`. Matching is the
  server's CardDAV Unicode text matching, not local regex, fuzzy search,
  phone-digit normalization, or a full-text index. Matching repeated
  properties follows CardDAV server semantics.
* Arrays of filters, arbitrary XML, parameter filters, negation, `is-not-defined`,
  regex, other collations, paging, offsets, and unsupported properties are
  rejected, not ignored or approximated.

`limit` defaults to 1,000 and must be 1..1,000 if nonzero. An explicit zero
selects the default. The REPORT asks for `limit + 1`; receiving more than
`limit` is an error, never a successful truncated result. DAV resource or
property errors (including the server's 507 limit indicator), unexpected
multistatus children, missing data/ETag, duplicate resources, malformed cards,
foreign/out-of-book hrefs, and response-budget exhaustion all fail the **entire**
query. No partial contacts are printed. `complete:true` means the complete
validated REPORT within these bounds, not a synchronization token, pagination
cursor, or a guarantee against a nonconforming server silently omitting data.

### Get

`get` performs GET with `Accept: text/vcard`. Only HTTP 200 is accepted. Result
is one resource object with `url`, `etag`, and the `parse` result fields below.
An absent ETag is returned as `""`; a weak server ETag can be read but cannot
be supplied to a subsequent mutation. vCard bytes are never normalized,
unfolded, reserialized, or stripped in the returned `vcard`.

### Create, update, delete

`create` PUT uses `If-None-Match: *` and requires HTTP 201.
`update` PUT uses the caller's `If-Match` and requires HTTP 200 or 204.
`delete` uses the caller's `If-Match` and requires HTTP 200 or 204.
There is no unconditional overwrite/delete mode, generated resource path,
automatic conflict retry, or read-modify-write fallback.

Both `update` and `delete` require a quoted strong ETag: `"revision"` in
decoded text (JSON: `"\"revision\""`). Wildcard, `W/"weak"`, unquoted strings,
CR/LF, controls, non-ASCII, embedded quotes, and values longer than 1,024 bytes
are rejected before any network request. HTTP 409 and 412 map to `conflict`;
the caller must explicitly fetch and decide how to reconcile.

Create accepts a raw `vcard` or a builder `contact`, never both.
Update accepts raw `vcard` only: using a summary to rebuild a server contact
would lose unknown properties. For raw creates `version` must be omitted;
the card's VERSION is authoritative. A caller supplies the target `url`
(or makes `endpoint` that URL), normally a new `.vcf` resource.

Before every PUT, Depth-0 PROPFIND checks the target parent address book and
its `supported-address-data`. An explicit capability list must contain the
card version. A missing/404 capability property permits only the RFC 6352
mandatory vCard 3.0 baseline; it is never evidence for 4.0. An explicit empty
list permits neither. No conversion/downgrade is attempted.

PUT Content-Type is `text/vcard; charset=utf-8`; a supplied raw card is sent
byte-for-byte, including original line endings, folding, order, groups,
repeated properties, and extensions. The whole raw card is validated first.
All mutation redirects fail closed; even a same-origin 307 is not followed.
Returned `Location` is neither followed nor substituted.

Create/update result: `{"url":"https://dav.example.org/book/a.vcf","etag":"\"new\""}`.
The response ETag is preserved, or `""` if not supplied; no unsafe fabricated
revision is substituted. Delete result: `{"deleted":true}`.

### Build

```json
{"protocol":1,"operation":"build","version":"3.0","contact":{"uid":"stable-id","formattedName":"世界 café","givenName":"Jane","familyName":"Doe","emails":[{"value":"jane@example.org","types":["home"]}],"phones":[{"value":"+81 123","types":["cell"]}],"addresses":[{"types":["home"],"street":"1 Main Street","locality":"東京","country":"日本"}],"urls":["https://example.org/"]}}
```

`version` defaults to `"3.0"`; only `"3.0"` and `"4.0"` are implemented.
The contact object supports:

| Field | Type / behavior |
|---|---|
| `uid` | Optional nonempty stable string; retained exactly if supplied |
| `formattedName` | Required nonempty string (`FN`) |
| `givenName`, `familyName` | Optional strings; structured `N` is always emitted, including empty components |
| `organization`, `title`, `note` | Optional strings (`ORG`, `TITLE`, `NOTE`) |
| `emails`, `phones` | Arrays of `{value: nonempty string, types: [string,...]}` |
| `addresses` | Array of `{types:[],poBox,extended,street,locality,region,postalCode,country}`; omitted string components are empty |
| `urls` | Array of absolute URI strings; no whitespace/control characters |

All types are optional arrays when omitted; explicit null is not permitted.
TYPE tokens contain ASCII letters, digits or hyphen only. Text supports
Unicode, tab and LF; CR, NUL and other controls are rejected. LF is escaped
as `\n`, and backslash, comma, semicolon are escaped as vCard TEXT.
Structured component delimiters are emitted separately. URI URL values are
not TEXT-escaped. vCard 4 phones are explicitly `VALUE=text`; non-URI stable
UIDs are explicitly `VALUE=text` in 4.0. No user text can inject a content line.

The builder emits CRLF, including after END, and folds lines at no more than
75 octets, with a space continuation, never splitting a UTF-8 character.
A supplied UID and the same inputs produce stable output. An absent/empty UID
generates a cryptographically random UUIDv4 in `urn:uuid:` form, with no
cross-process ordering or collision-proof guarantee.

Result: `{"vcard":"BEGIN:VCARD\r\n...","uid":"stable-id","version":"3.0"}`.
This intentionally builds basic contacts, not every vCard property, photo,
group or vendor extension. Preserve server contacts through raw update.

### Parse

```json
{"protocol":1,"operation":"parse","vcard":"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:id\r\nFN:Name\r\nEND:VCARD\r\n"}
```

Result fields:

* `vcard`: the exact supplied string, unchanged.
* `uid`, `version`, `formattedName`: summary strings.
* `properties`: every property between BEGIN/END, in source order, including
  VERSION, repeated properties, grouped names and unknown extensions:
  `{name,group,parameters,value}`. Names/parameter names are uppercase, groups
  retain spelling, parameters map to string arrays, missing parameters are `{}`.
  TEXT values decode escaped newline/backslash/comma/semicolon; URL and explicit
  `VALUE=uri` retain URI text. Parameter caret escapes follow RFC 6868.

Parsing requires exactly one card, BEGIN/END, VERSION immediately after BEGIN,
one supported VERSION, one nonempty UID, and a nonempty FN. CRLF and LF source
line endings are accepted; a final newline is required. Bare CR, malformed
content lines/parameters, unclosed quotes, nested/multiple cards, invalid
UTF-8, and duplicate UID/VERSION fail. Folding is interpreted for the summary
only. This is a bounded structural parser, not exhaustive validation of every
extension or property-specific RFC semantic constraint. Structured/list
properties' `value` strings are descriptive, not a lossless editable model.
The authoritative lossless representation is always `vcard`.

**XML distinction:** REPORT `address-data` is XML-decoded content. XML character
and entity rules apply before vCard parsing; a conforming server must encode
CR as a character reference if CR preservation matters. The CLI preserves the
decoded card exactly and never serializes it through a vCard encoder. GET/raw
stdin/PUT are byte-for-byte, without XML line-ending normalization.

## Trust and resource bounds

* Overall deadline: 25 seconds, starting before stdin is read. It covers waiting
  for EOF, validation and the entire network operation, not 25 seconds per hop.
* Input: 4 MiB including whitespace; JSON nesting at most 16 levels.
* Each vCard: 1 MiB, at most 10,000 unfolded content lines.
* Network bodies: 16 MiB aggregate across all responses, counting actual bytes
  read. Header limit 64 KiB; dial/TLS handshake 5 seconds; response-header
  timeout 10 seconds. Maximum 16 network requests including redirects.
* XML: 24 nesting levels, 100,000 elements; at most 1,000 DAV responses in each
  multistatus. DTD/directives, extra documents, invalid UTF-8, wrong namespaces,
  duplicated critical properties/statuses/hrefs and invalid status lines fail.
* DAV namespaces are exact `DAV:` and
  `urn:ietf:params:xml:ns:carddav`; local-name matches are insufficient.
  Resource/property non-2xx statuses fail. Only 404 for optional display name
  and supported-address-data is tolerated; required data is never dropped.
* Read-only redirects: maximum five per request; only 301/302/307/308.
  303 and other 3xx fail. REPORT redirects stay at the same book path.
  GET redirects remain immediate children of the same book. Discovery and its
  principal/home links may move within the trusted origin.
* HTTPS only, TLS 1.2+, normal certificate/hostname verification.
  Tests use `httptest.NewTLSServer` with injected trusted roots internally.
  There is **no** protocol field or flag to disable verification.
* Proxy environment variables are not honored. Every resolved dial IP is
  checked before connecting; the validated numeric IP is dialed to prevent a
  second DNS lookup/rebinding. Loopback/private/link-local/multicast,
  unspecified, shared-address space, documentation/benchmark addresses, and
  non-public IPv6/transition ranges are rejected. A DNS set containing any
  rejected address fails closed. Private-network DAV deployment is outside
  this CLI's public-server security profile.
* URLs cannot contain credentials, query, fragment, opaque syntax, controls,
  backslashes, trailing-dot hosts, ambiguous encoded separators, dot segments,
  double separators, or recursively encoded path escapes. Origin includes
  scheme, hostname and effective port (default 443). Arbitrary cross-origin
  redirects and hrefs are rejected before sending credentials.

### Narrow iCloud bootstrap

Only discovery whose original endpoint is exactly the HTTPS `icloud.com`
authority on port 443 may follow a server redirect/principal/home href to
`p[0-9]{1,3}-contacts.icloud.com` on HTTPS port 443. After the first shard
request, that single shard is pinned for the rest of the operation; returning
to the bootstrap origin or switching shards fails. No `caldav` host patterns,
suffix-only checks, `www.icloud.com`, regional domains, custom ports,
`contacts.icloud.com` alias bootstrap, or general iCloud credential forwarding
are allowed. Direct shard endpoints work as ordinary same-origin endpoints.
Subsequent separate CRUD/query requests should use a discovered book/shard as
their explicit endpoint. No live endpoint is contacted for testing.
**Live iCloud compatibility is unverified:** no authenticated Apple endpoint
has been exercised. The offline tests establish the trust policy, not account
interoperability. This CLI does not implement group management, directory sync,
or photo extraction; photos are preserved only within raw vCards.

Evidence and boundary: Apple documents CardDAV account host/principal settings
and SSL in [its CardDAV payload reference](https://developer.apple.com/documentation/devicemanagement/carddav).
The Contacts-specific public interoperability reference
[icloud-mcp CardDAV compatibility](https://github.com/ThomasCrouzet/icloud-mcp/blob/main/docs/carddav-compatibility.md)
records `p[0-9]{1,3}-contacts.icloud.com` and verified TLS, distinctly from
CalDAV. Apple does not promise that shard grammar in its public account
documentation. The exact `icloud.com` bootstrap authority is this protocol's
deliberately narrow allowed entry point, not a claim that every account will
redirect successfully from it. Unsafe or unsupported discovery fails.

## Errors

All error messages are fixed, sanitized descriptions. The code is the stable
machine-readable discriminator. No partial result is emitted.

| Code | Meaning |
|---|---|
| `invalid_arguments` | Expected exactly `request` or `--version`; exit 2 |
| `invalid_request` | Invalid JSON/schema/field combination/URL/filter/contact/ETag |
| `input_limit` | Stdin exceeded 4 MiB or built card exceeded 1 MiB |
| `invalid_vcard` | Supplied card failed bounded structural validation (including card size) |
| `unsupported_version` | Version other than 3.0/4.0, or server did not support requested write/query version |
| `unsafe_url` | URL/href crossed trust or address boundaries |
| `unsafe_redirect` | Mutation redirect, excessive hop count, unsafe scoped redirect, or unsupported 3xx |
| `network_error` | Sanitized DNS/connect/TLS/I/O failure |
| `timeout` | Overall request deadline expired |
| `invalid_response` | Invalid success status, DAV/XML/card structure, required property, or content encoding |
| `multistatus_error` | Resource/property error or unexpected multistatus error indicator |
| `response_limit` | Network/XML/resource/query bound exceeded, or server 507 |
| `authentication_failed` | Server HTTP 401/403 (includes authorization failure) |
| `not_found` | Server HTTP 404 |
| `conflict` | Server HTTP 409/412; no retry |
| `rate_limited` | Server HTTP 429; no retry |
| `server_error` | Other non-success HTTP status |
| `entropy_error` | Random UID generation failed |

Unexpected encoding, remote invalid card, unsupported remote card version, and
malformed multistatus are errors, never converted into empty/success results.

## Dependency and offline verification

`github.com/emersion/go-vcard v0.1.0` is pinned in `cli/go.mod`/`go.sum`,
tag commit `3f59d540edef42a9cdc14301fae19a6f48a3aa61`; MIT license is redistributed
next to the retained Go license in component Resources and CLI licenses.
The module has no third-party transitive dependencies.

Upstream's decoder is permissive and its encoder neither escapes semicolons
nor folds Unicode lines. Tests explicitly pin those observed behaviors.
The engine adds strict framing/header checks, TEXT unescaping, parameter
handling and its own deterministic builder/folder; the dependency decoder
is never used as a lossless transport serializer.

```sh
cd cli
go test ./...
go test -race ./...
go vet ./...
go mod verify
cd ..
python3 -m unittest tests/test_cli.py -v
```

Native contract vectors in `tests/cli-contract.json` use optional `stdin`
as literal Text (UTF-8), containing compact JSON plus LF for requests.
An omitted field means empty stdin. The native runner must pass those bytes
as `subprocess.run(..., input=stdin_bytes, capture_output=True, timeout=30)`.
It must validate `stdin` is Text; do not put JSON payloads in argv.
The Python CLI tests independently perform this framing and verify exact
success/failure/version vectors from paths with spaces.
