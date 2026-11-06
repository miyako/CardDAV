# CardDAV

**CardDAV** is a Go CLI-backed 4D component for address-book discovery and
conditional contact CRUD. The namespace is `CardDAV`, public class is `Client`,
and bundled executable is `carddav`. It runs through `4D.SystemWorker`, not a
native host plug-in. Credentials travel only in UTF-8 JSON stdin; the component
does not persist credentials or contacts.

## 4D API

```4d
var $client : cs.CardDAV.Client
var $credentials; $card; $result : Object
var $target : Text
$target:="macOS-universal"
If (Is Windows)
  $target:="Windows-x64"
End if
// Obtain these values from caller-managed secure storage, not source code.
$credentials:=New object("endpoint"; $endpoint; "username"; $username; "password"; $password)
$client:=cs.CardDAV.Client.new($target; $credentials)
$result:=$client.discover()
If (Not($result.ok))
  ALERT($result.error.code+": "+$result.error.message)
End if

// Keep this UID when editing the same contact.
$card:=New object("uid"; "application-owned-stable-id"; "formattedName"; "Miyako")
$card.emails:=New collection(New object("value"; "miyako@example.invalid"; "types"; New collection("work")))
$result:=$client.build($card; "3.0")
```

`discover()`, `query(bookURL; options)`, `get(url)`, `create(url; vcard)`,
`update(url; vcard; etag)`, `delete(url; etag)`, `build(contact; version)`,
`parse(vcard)` and `version()` return explicit success/error envelopes.
See the [class contract](component/CardDAV/Documentation/Classes/Client.md) and
[versioned protocol](component/CardDAV/Documentation/Protocol.md) for exact
fields, limits, supported filters, return values and status codes.

## Contact fidelity and concurrency

Discovery follows `current-user-principal`, `addressbook-home-set`, and
address-book collections. Queries use CardDAV REPORT, not directory scraping.
Server-reported errors, unsupported formats, oversized results and incomplete
responses are failures; limits are not silent pagination or background sync.

The basic builder supports names, organization, title, notes, multiple emails,
phones, structured postal addresses and URLs, with UTF-8 escaping/folding.
vCard 3.0 and 4.0 are explicit formats, not interchangeable labels. Discovery's
supported address-data describes server capabilities; choose a supported format.
Raw vCards are accepted and returned without reserializing: grouped, repeated,
unknown and vendor properties remain intact. Always update the entire retrieved
raw resource after deliberate edits. Building a new card from selected fields
is **not** a lossless update of an existing card.

Creates send `If-None-Match: *`. Updates/deletes require a supplied strong
`If-Match` ETag; HTTP 412 is an explicit conflict. Do not retry by dropping the
condition. Preserve UID across updates. No merging, deduplication, automatic
background/directory sync, LDAP, local macOS Contacts access or contact-group
management is implemented. Groups/vendor extensions can be preserved as raw
data. Photos are raw-only: no upload helper, fetching external photo URLs,
decoding, resizing or media management.

## Security and boundaries

HTTPS certificate verification is always enabled. Network requests, response
bodies, input size, redirects and runtime are bounded. DAV hrefs and redirect
destinations are scoped; arbitrary cross-origin credentials are never forwarded.
Mutating redirects fail closed without replay. iCloud bootstrap support is
limited to validated CardDAV discovery hosts, not calendar-host guesses.
Server bodies, credentials and private URLs are not reflected in errors.
The caller supplies credentials per client, optionally retrieved from its own
secure storage. There is no plaintext persistence or native host plug-in.

No private account or Keychain item is needed for mock/local acceptance.
Live iCloud access requires a separately authorized test account and setup.

## CLI

One request object is read from stdin through EOF, with protocol version 1.
Only `request` or `--version` is passed as an argument. Never put secrets in
argv, shell scripts, reports or logs.

| Invocation | Response |
| --- | --- |
| `carddav --version` | `{"protocol":1,"version":"0.1.2"}` followed by LF |
| `carddav request` | `{"protocol":1,"result":{...}}` on stdout; structured errors on stderr |

```sh
printf '%s' '{"protocol":1,"operation":"build","version":"3.0","contact":{"uid":"example-id","formattedName":"Example"}}' \
  | ./build/macos-arm64/carddav request
```

## Build and local acceptance

Go is the only implementation. Dependencies are pinned in `cli/go.mod` and
`cli/go.sum`; redistribution notices ship in `Resources/licenses/`.
The 4D project uses compatibility version 2060. Local compilation uses the pinned
free tool4d 20.7 build 101905; it is not redistributed. Go source minimum is 1.23;
CI pins Go 1.26.10. Report the actual local compiler rather than the CI pin.

```sh
python3 scripts/doctor.py --root .
python3 -m unittest discover -s tests -v
(cd cli && go test -race ./... && go vet ./... && go mod verify)
python3 scripts/generate_icons.py --check
python3 scripts/ci_config.py download-tool4d --root .
python3 scripts/accept_local.py --root . --repository miyako/CardDAV \
  --tool4d .tools/tool4d.app/Contents/MacOS/tool4d --cross
```

The local pipeline builds macOS arm64, macOS x86_64 and Windows x86_64 from
identical inputs with manifests, assembles the universal macOS executable,
source-host tests, actually compiles, packs/unpacks and tests the final compiled
component. The ZIP strips 4D source and retains generated completion metadata
and compiled Libraries. Cross-builds are **not native execution**; Rosetta CLI
execution, if available, is translated evidence, not native Intel 4D acceptance.
The approved ARM64 compiler is not a Windows or Intel host runtime.

Outputs are `dist/CardDAV.zip` and `dist/CardDAV.acceptance.json`; detailed
reports are under `build/`. See
[local acceptance](component/CardDAV/Documentation/Acceptance.md) for observed
checks and remaining gates. A locally accepted unsigned package is **not fully
release-ready**: native foreign-platform execution, live-account behavior and
published Dependency Manager installation remain independent gates.

## Installation and publication policy

For local use, unpack `dist/CardDAV.zip` into a host's `Components/` folder.
The outer root is `CardDAV.4dbase`, containing `CardDAV.4DZ`, compiled Libraries,
Resources and Documentation. This installs a component, not a native plug-in.

Future authorized GitHub releases must use bare SemVer tags such as `0.1.0`,
**never `v0.1.0`**, and asset `CardDAV.zip`. Namespace/repository case is
significant. Ordinary SemVer does not claim 4D Follow4D host-version matching.
There is no CardDAV release migration to perform.

Exactly two workflows are retained: **CI** and **Publish**. CI builds/tests the
selected language on native macOS Intel/ARM and Windows runners. Hosted compiled
acceptance requires `FOURD_ENABLED=true`; CLI-only green is not 4D acceptance.
Manual Publish on main plans patch/minor/major, synchronizes version surfaces,
builds the exact revision and verifies compiler, package, final compiled-host
and downloaded asset proofs before creating a tag/release. Tag-push publication
is not enabled. Signing is opt-in via `release-policy.json`; unsigned local
acceptance needs no signing secrets. See [AGENTS.md](AGENTS.md) for unchanged
pipeline gates and tool pins.

No push, PR, merge, tag, release, signing provisioning or publication is part of
this local implementation.
