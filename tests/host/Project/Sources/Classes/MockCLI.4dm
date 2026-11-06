Class extends cs.CardDAV._CLI

property calls : Collection
property credentialsChecked : Boolean
property _vcard : Text
property _etag : Text
property _exists : Boolean

Class constructor($target : Text)
	Super("carddav"; $target)
	This.calls:=New collection
	This.credentialsChecked:=True
	This._exists:=False

Function execute($arguments : Collection; $input : Text)->$result : Object
	var $request; $data; $payload : Object
	If ($arguments.length#1)
		return This._failure("invalid_arguments")
	End if
	If ($arguments[0]#"request")
		return This._failure("invalid_arguments")
	End if
	$request:=JSON Parse($input)
	If (($request.protocol#1) | ($request.endpoint#"https://host.example.test/") | ($request.username#"host-user") | ($request.password#"host-only"))
		This.credentialsChecked:=False
		return This._failure("invalid_arguments")
	End if
	This.calls.push($request.operation)
	Case of
		: ($request.operation="discover")
			$data:=New object("addressbooks"; New collection(New object("url"; "https://host.example.test/books/default/"; "displayName"; "Host 日本語"; "supportedVersions"; New collection("3.0"; "4.0"))))
		: ($request.operation="query")
			If (($request.bookURL#"https://host.example.test/books/default/") | ($request.limit#10))
				return This._failure("invalid_arguments")
			End if
			$data:=New object("contacts"; New collection; "complete"; True)
			If (This._exists)
				$data.contacts.push(New object("url"; "https://host.example.test/books/default/host-test.vcf"; "etag"; This._etag; "vcard"; This._vcard))
			End if
		Else
			If ($request.url#"https://host.example.test/books/default/host-test.vcf")
				return This._failure("invalid_arguments")
			End if
			Case of
				: ($request.operation="create")
					If (This._exists)
						return This._failure("conflict")
					End if
					This._vcard:=$request.vcard
					This._etag:="\"host-1\""
					This._exists:=True
					$data:=New object("url"; $request.url; "etag"; This._etag)
				: ($request.operation="get")
					If (Not(This._exists))
						return This._failure("not_found")
					End if
					$data:=New object("url"; $request.url; "etag"; This._etag; "vcard"; This._vcard)
				: (($request.operation="update") | ($request.operation="delete"))
					If (Not(This._exists))
						return This._failure("not_found")
					End if
					If ($request.etag#This._etag)
						return This._failure("conflict")
					End if
					If ($request.operation="update")
						This._vcard:=$request.vcard
						This._etag:="\"host-2\""
						$data:=New object("url"; $request.url; "etag"; This._etag)
					Else
						This._exists:=False
						$data:=New object("deleted"; True)
					End if
				Else
					return This._failure("invalid_arguments")
			End case
	End case
	$payload:=New object("protocol"; 1; "result"; $data)
	$result:=New object("ok"; True; "stdout"; JSON Stringify($payload)+Char(10); "stderr"; ""; "exitCode"; 0)

Function _failure($code : Text)->$result : Object
	var $payload : Object
	$payload:=New object("protocol"; 1; "error"; New object("code"; $code; "message"; "Host fixture rejected request"))
	$result:=New object("ok"; False; "stdout"; ""; "stderr"; JSON Stringify($payload)+Char(10); "exitCode"; 1; "error"; New object("code"; "cli_error"; "message"; "CLI exited with a nonzero status"))
