//%attributes = {"invisible":true}
var $ignored; $i : Integer
var $parameter; $name; $target; $raw; $large; $note; $crlf; $builtCard; $etag : Text
var $request; $result; $report; $contact; $parsed; $built; $data; $entry; $property; $fields : Object
var $tool; $online : cs.CardDAV.Client
var $mock : cs.MockCLI
var $requestFile; $statusFile; $renamedExecutable : 4D.File
var $invalid : Variant
var HOST_StatusFile : 4D.File
var HOST_Phase : Text
var HOST_PreviousHandler : Text
$ignored:=Get database parameter(User param value; $parameter)
If (Is Windows)
	$requestFile:=File($parameter; fk platform path)
Else
	$requestFile:=File($parameter; fk posix path)
End if
$request:=JSON Parse($requestFile.getText())
If (Is Windows)
	$statusFile:=File($request.status; fk platform path)
Else
	$statusFile:=File($request.status; fk posix path)
End if
$statusFile.setText("{\"success\":false,\"phase\":\"starting\"}")
HOST_StatusFile:=$statusFile
HOST_Phase:="constructor"
HOST_PreviousHandler:=Method called on error(ek local)
ON ERR CALL("Host_error"; ek local)
$target:="macOS-universal"
If (Is Windows)
	$target:="Windows-x64"
End if
$tool:=cs.CardDAV.Client.new($target)
$report:=New object("success"; True; "finished"; False; "cases"; New collection)
$report.compiled:=$tool._cli.compiled
If ($request.expectCompiled#$report.compiled)
	$report.success:=False
End if
HOST_Phase:="renamed executable"
If (Is Windows)
	$renamedExecutable:=File($request.executable; fk platform path)
Else
	$renamedExecutable:=File($request.executable; fk posix path)
End if
$report.renamedExecutable:=cs.CardDAV._Process.new().execute($renamedExecutable; New collection("--version"))
If (Not($report.renamedExecutable.ok))
	$report.success:=False
End if
HOST_Phase:="UTF8 stdin through EOF"
$crlf:=Char(13)+Char(10)
$report.rawPreserved:=True
For each ($name; New collection("ASCII"; "path with spaces"; "quote \" and \\ slash"; "line\\nbreak"; "日本語"; "$() ; & % !"))
	$raw:="BEGIN:VCARD"+$crlf+"VERSION:3.0"+$crlf+"UID:host-test"+$crlf+"FN:"+$name+$crlf+"X-UNKNOWN;X-PARAM=preserve:opaque value"+$crlf+"END:VCARD"+$crlf
	$result:=$tool.parse($raw)
	$report.cases.push($result)
	If (Not($result.ok))
		$report.success:=False
	Else
		If ($result.value.vcard#$raw)
			$report.success:=False
			$report.rawPreserved:=False
		End if
	End if
End for each
HOST_Phase:="large raw stdin"
$large:="BEGIN:VCARD"+$crlf+"VERSION:4.0"+$crlf+"UID:host-test"+$crlf+"FN:Large 日本語"+$crlf+"X-RAW:preserve"+$crlf
For ($i; 1; 1800)
	$large+=" 012345678901234567890123456789012345678901234567890123456789 日本語"+$crlf
End for
$large+="END:VCARD"+$crlf
$result:=$tool.parse($large)
$report.largeParse:=$result
$report.largeInput:=Length($large)>65536
$report.largePreserved:=False
If ($result.ok)
	$report.largePreserved:=($result.value.vcard=$large)
End if
If (Not($report.largePreserved))
	$report.success:=False
End if
HOST_Phase:="vCard builders"
$note:=""
For ($i; 1; 1800)
	$note+="Large note 日本語 0123456789012345678901234567890123456789 "
End for
$contact:=New object("uid"; "host-test"; "formattedName"; "Host 日本語"; "givenName"; "Given"; "familyName"; "Family"; "organization"; "Host Org"; "title"; "Engineer"; "note"; $note)
$contact.emails:=New collection(New object("value"; "host@example.test"; "types"; New collection("work")))
$contact.phones:=New collection(New object("value"; "+1-555-0100"; "types"; New collection("work"; "voice")))
$contact.addresses:=New collection(New object("types"; New collection("work"); "poBox"; "Box"; "extended"; "Suite"; "street"; "Main Street"; "locality"; "City"; "region"; "State"; "postalCode"; "12345"; "country"; "Country"))
$contact.urls:=New collection("https://example.test/日本語")
$report.builders:=New collection
For each ($name; New collection("3.0"; "4.0"))
	$built:=$tool.build($contact; $name)
	$entry:=New object("build"; $built; "roundTrip"; False; "large"; False)
	If ($built.ok)
		$parsed:=$tool.parse($built.value.vcard)
		$entry.parse:=$parsed
		$entry.large:=Length($built.value.vcard)>65536
		If ($parsed.ok)
			$entry.roundTrip:=($parsed.value.vcard=$built.value.vcard) & ($parsed.value.uid="host-test") & ($parsed.value.version=$name)
			$fields:=New object
			For each ($property; $parsed.value.properties)
				$fields[$property.name]:=$property.value
			End for each
			$entry.contactFields:=New object("uid"; $fields.UID; "formattedName"; $fields.FN; "name"; $fields.N; "organization"; $fields.ORG; "title"; $fields.TITLE; "email"; $fields.EMAIL; "phone"; $fields.TEL; "address"; $fields.ADR; "url"; $fields.URL; "largeNotePreserved"; $fields.NOTE=$note)
		End if
		$builtCard:=$built.value.vcard
	End if
	If (Not($entry.roundTrip))
		$report.success:=False
	End if
	$report.builders.push($entry)
End for each
HOST_Phase:="invalid vCard and version"
$report.invalid:=$tool.parse("not a vcard")
If ($report.invalid.ok)
	$report.success:=False
End if
$report.version:=$tool.version()
$report.versionMatches:=False
If ($report.version.ok)
	$report.versionMatches:=($report.version.value.version=$request.version)
End if
If (Not($report.versionMatches))
	$report.success:=False
End if
HOST_Phase:="host-only mock discovery and conditional CRUD"
$request.dav:=New object("endpoint"; "https://host.example.test/"; "username"; "host-user"; "password"; "host-only"; "bookURL"; "https://host.example.test/books/default/"; "url"; "https://host.example.test/books/default/host-test.vcf")
$online:=cs.CardDAV.Client.new($target; $request.dav)
$request.dav.password:="changed-after-construction"
$mock:=cs.MockCLI.new($target)
$online._cli:=$mock
$report.network:=New object
$result:=$online.discover()
$report.network.discover:=$result
If (Not($result.ok))
	$report.success:=False
End if
$result:=$online.create($request.dav.url; $builtCard)
$report.network.create:=$result
$etag:=""
If ($result.ok)
	$etag:=$result.value.etag
Else
	$report.success:=False
End if
$result:=$online.get($request.dav.url)
$report.network.get:=$result
$report.network.rawPreserved:=False
If ($result.ok)
	$report.network.rawPreserved:=($result.value.vcard=$builtCard)
Else
	$report.success:=False
End if
$result:=$online.query($request.dav.bookURL; New object("limit"; 10))
$report.network.query:=$result
If (Not($result.ok))
	$report.success:=False
End if
$report.network.conflict:=$online.update($request.dav.url; $builtCard; "\"stale\"")
If ($report.network.conflict.ok)
	$report.success:=False
End if
$result:=$online.update($request.dav.url; $builtCard; $etag)
$report.network.update:=$result
If ($result.ok)
	$etag:=$result.value.etag
Else
	$report.success:=False
End if
$result:=$online.delete($request.dav.url; $etag)
$report.network.delete:=$result
If (Not($result.ok))
	$report.success:=False
End if
$report.network.calls:=$mock.calls
$report.network.credentialsChecked:=$mock.credentialsChecked
$report.requestNull:=$tool.request(Null)
$report.buildNull:=$tool.build(Null; "4.0")
$report.unsupportedOperation:=$tool.request(New object("operation"; "unknown"))
HOST_Phase:="strict malformed protocol checks"
$report.protocolInvalid:=New collection
For each ($name; New collection("not json"; "true"; "false"; "null"; "1"; "[]"; "{\"protocol\":true,\"result\":{}}"; "{\"protocol\":false,\"result\":{}}"; "{\"protocol\":null,\"result\":{}}"; "{\"protocol\":2,\"result\":{}}"; "{\"protocol\":1,\"result\":null}"; "{\"protocol\":1,\"result\":{}}"))
	$result:=$tool._decode(New object("ok"; True; "stdout"; $name; "stderr"; ""; "exitCode"; 0); "parse")
	$report.protocolInvalid.push($result)
	If ($result.ok | ($result.error.code#"invalid_protocol"))
		$report.success:=False
	End if
End for each
$report.operationInvalid:=New collection
For each ($name; New collection("discover"; "query"; "get"; "create"; "update"; "delete"; "build"; "parse"; "version"))
	$data:=New object
	Case of
		: ($name="discover")
			$data.addressbooks:=New collection(New object("url"; False))
		: ($name="query")
			$data:=New object("contacts"; New collection; "complete"; False)
		: (($name="get") | ($name="create") | ($name="update"))
			$data:=New object("url"; "https://example.test/card"; "etag"; True; "vcard"; "raw")
		: ($name="delete")
			$data.deleted:="true"
		: (($name="parse") | ($name="build"))
			$data:=New object("vcard"; "raw"; "uid"; "host-test"; "version"; "9.0")
		: ($name="version")
			$data.version:=False
	End case
	If ($name="version")
		$result:=$tool._decode(New object("ok"; True; "stdout"; "{\"protocol\":1,\"version\":false}"; "stderr"; ""; "exitCode"; 0); $name)
	Else
		$result:=$tool._decode(New object("ok"; True; "stdout"; JSON Stringify(New object("protocol"; 1; "result"; $data)); "stderr"; ""; "exitCode"; 0); $name)
	End if
	$report.operationInvalid.push($result)
	If ($result.ok | ($result.error.code#"invalid_protocol"))
		$report.success:=False
	End if
End for each
$report.structuredError:=$tool._decode(New object("ok"; False; "stdout"; ""; "stderr"; "{\"protocol\":1,\"error\":{\"code\":\"precondition_failed\",\"message\":\"Stale ETag\"}}"+Char(10); "exitCode"; 1; "error"; New object("code"; "cli_error"; "message"; "CLI exited with a nonzero status")); "update")
$report.unstructuredError:=$tool._decode(New object("ok"; False; "stdout"; ""; "stderr"; "unstructured diagnostic"; "exitCode"; 9; "error"; New object("code"; "cli_error"; "message"; "CLI exited with a nonzero status")); "parse")
HOST_Phase:="generic resource and launch failures"
$report.missing:=cs.CardDAV._CLI.new("missing-cli"; $target).execute(New collection("--version"))
$report.unsupported:=cs.CardDAV.Client.new("unsupported-arm-target").version()
$report.launch:=cs.CardDAV._Process.new().execute($requestFile; New collection)
If (($report.missing.error.code#"missing_executable") | ($report.unsupported.error.code#"unsupported_architecture") | ($report.launch.error.code#"launch_failed"))
	$report.success:=False
End if
HOST_Phase:="File and Folder arguments are literal"
$report.fileArgument:=cs.CardDAV._CLI.new("carddav"; $target).execute(New collection("--version"; $requestFile))
$report.folderArgument:=cs.CardDAV._CLI.new("carddav"; $target).execute(New collection("--version"; $requestFile.parent))
If ($report.fileArgument.ok | $report.folderArgument.ok)
	$report.success:=False
End if
$report.rejectedArguments:=New collection
HOST_Phase:="unsupported argument types"
For each ($invalid; New collection(123; 1.25; True; False; Null; Current date; Current time; New collection; New object))
	$result:=cs.CardDAV._Process.new().execute($requestFile; New collection($invalid))
	$report.rejectedArguments.push(New object("type"; Value type($invalid); "result"; $result))
	If ($result.ok | ($result.error.code#"invalid_argument"))
		$report.success:=False
	End if
End for each
$report.rejectedNul:=New collection
HOST_Phase:="NUL arguments"
For each ($name; New collection(Char(0)+"prefix"; "middle"+Char(0)+"suffix"; "trailing"+Char(0)))
	$result:=cs.CardDAV._Process.new().execute($requestFile; New collection($name))
	$report.rejectedNul.push($result)
	If ($result.ok | ($result.error.code#"invalid_argument"))
		$report.success:=False
	End if
End for each
$report.missingFile:=cs.CardDAV._Process.new().execute(Null; New collection)
$report.missingArguments:=cs.CardDAV._Process.new().execute($requestFile; Null)
$report.finished:=True
ON ERR CALL(HOST_PreviousHandler; ek local)
$statusFile.setText(JSON Stringify($report))
HOST_StatusFile:=Null
HOST_Phase:=""
HOST_PreviousHandler:=""
QUIT 4D
