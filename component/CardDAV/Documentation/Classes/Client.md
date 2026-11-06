# Client

`cs.CardDAV.Client.new(target : Text; credentials : Object)` is the synchronous
CardDAV/vCard facade. Component-internal code uses `cs.Client`.

On macOS, omit `target` or use `"macOS-universal"`. Windows requires
`"Windows-x64"`; native Windows ARM64 is unsupported. Credentials are optional
for offline build/parse and contain `endpoint`, `username`, and `password` Text
properties. Only those properties are copied into memory. Nothing is written
to a credential file or keychain, and credentials never enter argv. The
underscore properties are implementation details, not an access-control boundary.
Do not log the client or outgoing requests.

| Method | Successful `value` |
| --- | --- |
| `discover()` | `{addressbooks:[{url,...}]}` |
| `query(bookURL : Text; options : Object)` | `{contacts:[{url,etag,vcard}],complete:true}` |
| `get(url : Text)` | `{url,etag,vcard}` |
| `create(url : Text; vcard : Text)` | `{url,etag}` |
| `update(url : Text; vcard : Text; etag : Text)` | `{url,etag}` |
| `delete(url : Text; etag : Text)` | `{deleted:true}` |
| `build(contact : Object; version : Text)` | `{vcard,uid,version}` |
| `parse(vcard : Text)` | `{vcard,uid,version,...}` |
| `version()` | `{version:Text}` |
| `request(request : Object)` | The selected operation's result above |

`query` options forward only `filter` and `limit`. `request` accepts operations
`discover`, `query`, `get`, `create`, `update`, `delete`, `build`, and `parse`.
It copies the object, sets `protocol:1`, and supplies missing network credentials
from the constructor. Offline operations remove credential properties before
serialization. The caller's object is not mutated. `build` accepts vCard
version `"3.0"` or `"4.0"`; use an explicit version for portable callers.

The contact object supports `uid`, `formattedName`, `givenName`, `familyName`,
`organization`, `title`, and `note` Text; `emails` and `phones` Collections of
`{value:Text,types:[Text]}`; `urls:[Text]`; and `addresses` containing
`{types:[Text],poBox,extended,street,locality,region,postalCode,country}`.
The raw parser preserves the supplied vCard Text, including unknown properties,
folding, and CRLF. Use that raw Text for lossless CRUD rather than rebuilding
parsed contacts. Create is conditional on absence; update/delete require the
current opaque ETag. Never silently retry a precondition failure as an overwrite.

## Result and error contract

Success is `{ok:true,stdout:Text,stderr:Text,exitCode:0,value:Object}`.
For request operations, **`value` is the CLI response's `result` object**, not
the outer `{protocol:1,result:...}` wrapper. Version normalizes the separate flat
version response into `value:{version:Text}`. Raw stdout retains the full JSON response.
Each invocation launches `carddav request`, writes one UTF-8 JSON object to
stdin, closes stdin (EOF), and waits with the generic 30-second process timeout.
Version uses `carddav --version` without stdin.
Its exact current stdout is `{"protocol":1,"version":"0.1.1"}` followed by LF.
The version follows the configured component/CLI release; request successes
continue to use the nested `result` wrapper.

The decoder checks numeric protocol 1 (not Boolean or Null), Object result,
and operation-specific required field types. Query requires `complete:true`;
truncated results are not success. Malformed success output returns
`invalid_protocol` while retaining the process streams and exit code.

Failures are `{ok:false,error:{code,message},...}`. Valid protocol-1 structured
CLI stderr preserves its **original error code and message**, e.g.
`conflict`, rather than replacing it with `cli_error`. Available
stdout/stderr/exitCode remain available; no successful `value` is fabricated.
Unstructured nonzero stderr retains `cli_error` and a generic message, with
the diagnostic available only in the stderr field. Prelaunch failures have no
invented exitCode. Other generic codes include `invalid_argument`,
`missing_executable`, `unsupported_platform`, `unsupported_architecture`,
`launch_failed`, `input_failed`, `process_error`, and `process_terminated`.
No error adds the outgoing request or constructor credentials.

```4d
var $client : cs.CardDAV.Client
var $built; $parsed : Object
$client:=cs.CardDAV.Client.new("macOS-universal")
$built:=$client.build(New object("uid"; "example-contact"; "formattedName"; "日本語"); "4.0")
If ($built.ok)
    $parsed:=$client.parse($built.value.vcard)
End if
```

The deterministic host fixture exercises offline Unicode, unknown properties,
raw preservation, >64 KiB input and both builders, then discovery/query and
conditional CRUD using a host-only mock `_CLI` subclass, never a live account.
The mock checks argv, protocol and in-memory credential forwarding; it does not
relax the real CLI's HTTPS/public-address restrictions. Real network behavior
is covered by native CLI tests, not claimed by this host fixture. Its configured expected
version comes from the test runner, not a hardcoded release. Both source and
compiled component acceptance must run through the actual 4D host.
