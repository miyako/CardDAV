property _cli : cs._CLI
property _credentials : Object

Class constructor($target : Text; $credentials : Object)
	var $field : Text
	This._cli:=cs._CLI.new("carddav"; $target)
	This._credentials:=New object
	If (Value type($credentials)=Is object)
		For each ($field; New collection("endpoint"; "username"; "password"))
			If (Value type($credentials[$field])=Is text)
				This._credentials[$field]:=$credentials[$field]
			End if
		End for each
	End if

Function discover()->$result : Object
	$result:=This.request(New object("operation"; "discover"))

Function query($bookURL : Text; $options : Object)->$result : Object
	var $input : Object
	var $field : Text
	$input:=New object("operation"; "query"; "bookURL"; $bookURL)
	If (Value type($options)=Is object)
		For each ($field; New collection("filter"; "limit"))
			If (Value type($options[$field])#Is undefined)
				$input[$field]:=$options[$field]
			End if
		End for each
	End if
	$result:=This.request($input)

Function get($url : Text)->$result : Object
	$result:=This.request(New object("operation"; "get"; "url"; $url))

Function create($url : Text; $vcard : Text)->$result : Object
	$result:=This.request(New object("operation"; "create"; "url"; $url; "vcard"; $vcard))

Function update($url : Text; $vcard : Text; $etag : Text)->$result : Object
	$result:=This.request(New object("operation"; "update"; "url"; $url; "vcard"; $vcard; "etag"; $etag))

Function delete($url : Text; $etag : Text)->$result : Object
	$result:=This.request(New object("operation"; "delete"; "url"; $url; "etag"; $etag))

Function build($contact : Object; $version : Text)->$result : Object
	If ($contact=Null)
		return New object("ok"; False; "error"; New object("code"; "invalid_argument"; "message"; "A contact Object is required"))
	End if
	$result:=This.request(New object("operation"; "build"; "contact"; $contact; "version"; $version))

Function parse($vcard : Text)->$result : Object
	$result:=This.request(New object("operation"; "parse"; "vcard"; $vcard))

Function version()->$result : Object
	$result:=This._decode(This._cli.execute(New collection("--version")); "version")

Function request($request : Object)->$result : Object
	var $input : Object
	var $field : Text
	If ($request=Null)
		return New object("ok"; False; "error"; New object("code"; "invalid_argument"; "message"; "A request Object is required"))
	End if
	If (Value type($request.operation)#Is text)
		return New object("ok"; False; "error"; New object("code"; "invalid_argument"; "message"; "A supported operation is required"))
	End if
	If (New collection("discover"; "query"; "get"; "create"; "update"; "delete"; "build"; "parse").indexOf($request.operation)<0)
		return New object("ok"; False; "error"; New object("code"; "invalid_argument"; "message"; "A supported operation is required"))
	End if
	$input:=OB Copy($request)
	$input.protocol:=1
	If (($input.operation#"build") & ($input.operation#"parse"))
		For each ($field; New collection("endpoint"; "username"; "password"))
			If (Value type($input[$field])=Is undefined)
				If (Value type(This._credentials[$field])=Is text)
					$input[$field]:=This._credentials[$field]
				End if
			End if
		End for each
	Else
		For each ($field; New collection("endpoint"; "username"; "password"))
			OB REMOVE($input; $field)
		End for each
	End if
	$result:=This._decode(This._cli.execute(New collection("request"); JSON Stringify($input)); $input.operation)

Function _decode($result : Object; $operation : Text)->$decoded : Object
	var $payload : Variant
	var $previous; $stream : Text
	var $parseErrors : Collection
	var CLI_LastErrors : Collection
	If (Not($result.ok))
		If ($result.error.code#"cli_error")
			return $result
		End if
		$stream:=$result.stderr
	Else
		$stream:=$result.stdout
	End if
	$previous:=Method called on error(ek local)
	CLI_LastErrors:=Null
	ON ERR CALL("_CaptureError"; ek local)
	$payload:=JSON Parse($stream)
	ON ERR CALL($previous; ek local)
	$parseErrors:=CLI_LastErrors
	CLI_LastErrors:=Null
	If (($parseErrors#Null) | (Value type($payload)#Is object))
		If (Not($result.ok))
			return $result
		End if
		return This._protocolError($result; "CLI response must be a JSON object")
	End if
	If ((Value type($payload.protocol)#Is real) & (Value type($payload.protocol)#Is longint))
		If (Not($result.ok))
			return $result
		End if
		return This._protocolError($result; "Unsupported CLI protocol version")
	End if
	If ($payload.protocol#1)
		If (Not($result.ok))
			return $result
		End if
		return This._protocolError($result; "Unsupported CLI protocol version")
	End if
	If (Not($result.ok))
		If (Value type($payload.error)#Is object)
			return $result
		End if
		If ((Not(This._text($payload.error.code; True))) | (Not(This._text($payload.error.message; True))))
			return $result
		End if
		$result.error:=New object("code"; $payload.error.code; "message"; $payload.error.message)
		return $result
	End if
	If ($operation="version")
		If (Not(This._text($payload.version; True)))
			return This._protocolError($result; "CLI response does not match the requested operation")
		End if
		$result.value:=New object("version"; $payload.version)
	Else
		If (Value type($payload.result)#Is object)
			return This._protocolError($result; "CLI result must be an Object")
		End if
		If (Not(This._validResult($operation; $payload.result)))
			return This._protocolError($result; "CLI response does not match the requested operation")
		End if
		$result.value:=$payload.result
	End if
	$decoded:=$result

Function _protocolError($result : Object; $message : Text)->$decoded : Object
	$result.ok:=False
	$result.error:=New object("code"; "invalid_protocol"; "message"; $message)
	$decoded:=$result

Function _text($value : Variant; $nonempty : Boolean)->$valid : Boolean
	If (Value type($value)#Is text)
		return False
	End if
	If ($nonempty)
		return Length($value)>0
	End if
	$valid:=True

Function _validResult($operation : Text; $data : Object)->$valid : Boolean
	var $item; $version : Variant
	var $field : Text
	Case of
		: ($operation="version")
			return This._text($data.version; True)
		: (($operation="build") | ($operation="parse"))
			For each ($field; New collection("vcard"; "uid"; "version"))
				If (Not(This._text($data[$field]; True)))
					return False
				End if
			End for each
			If (($data.version#"3.0") & ($data.version#"4.0"))
				return False
			End if
			If ($operation="parse")
				If (Value type($data.formattedName)#Is undefined)
					If (Not(This._text($data.formattedName; True)))
						return False
					End if
				End if
				If (Value type($data.properties)#Is undefined)
					If (Value type($data.properties)#Is collection)
						return False
					End if
					For each ($item; $data.properties)
						If (Value type($item)#Is object)
							return False
						End if
						If ((Not(This._text($item.name; True))) | (Not(This._text($item.group; False))) | (Not(This._text($item.value; False))))
							return False
						End if
						If (Value type($item.parameters)#Is object)
							return False
						End if
					End for each
				End if
			End if
		: (($operation="get") | ($operation="create") | ($operation="update"))
			If ((Not(This._text($data.url; True))) | (Not(This._text($data.etag; False))))
				return False
			End if
			If ($operation="get")
				return This._text($data.vcard; True)
			End if
		: ($operation="delete")
			If (Value type($data.deleted)#Is Boolean)
				return False
			End if
			return $data.deleted
		: ($operation="discover")
			If (Value type($data.addressbooks)#Is collection)
				return False
			End if
			For each ($item; $data.addressbooks)
				If (Value type($item)#Is object)
					return False
				End if
				If (Not(This._text($item.url; True)))
					return False
				End if
				If (Value type($item.displayName)#Is undefined)
					If (Not(This._text($item.displayName; False)))
						return False
					End if
				End if
				If (Value type($item.supportedVersions)#Is undefined)
					If (Value type($item.supportedVersions)#Is collection)
						return False
					End if
					For each ($version; $item.supportedVersions)
						If (Not(This._text($version; True)))
							return False
						End if
					End for each
				End if
			End for each
		: ($operation="query")
			If ((Value type($data.contacts)#Is collection) | (Value type($data.complete)#Is Boolean))
				return False
			End if
			If (Not($data.complete))
				return False
			End if
			For each ($item; $data.contacts)
				If (Value type($item)#Is object)
					return False
				End if
				If (Not(This._validResult("get"; $item)))
					return False
				End if
			End for each
		Else
			return False
	End case
	$valid:=True
