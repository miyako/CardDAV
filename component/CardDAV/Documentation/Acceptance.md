# Local acceptance

The authoritative, artifact-bound result is `dist/CardDAV.acceptance.json`,
beside `dist/CardDAV.zip`. Inspect `finished`, `result`, `release_ready`,
`commit`, `artifact.sha256`, actual tool versions and each step's status.
A previous ZIP does not approve a subsequent failed acceptance attempt.
`build/acceptance.json` records the latest attempt, including failures.

## Reproduction

```sh
python3 scripts/doctor.py --root . --tool4d .tools/tool4d.app/Contents/MacOS/tool4d
python3 -m unittest discover -s tests -v
(cd cli && go test -race ./... && go vet ./... && go mod verify)
python3 scripts/generate_icons.py --check
python3 scripts/accept_local.py --root . --repository miyako/CardDAV \
  --tool4d .tools/tool4d.app/Contents/MacOS/tool4d --cross
```

The free pinned compiler is downloaded with
`python3 scripts/ci_config.py download-tool4d --root .`; the fixed vendor archive
must match the SHA256 in `package.json`. No signing credentials, license
activation, global configuration or private account is required.

## Evidence interpretation

| Surface | Required evidence |
| --- | --- |
| Go behavior | CLI tests including local TLS mock CardDAV, race detector, vet and module verification |
| Python gates | Initialized adoption, protocol/host contracts, version-bumped fixtures, bare-tag/package/publication/signing-proof regressions |
| Three CLI artifacts | Same-source manifests under `build/<target>/`, including source digest, revision and executable SHA256 |
| Native ARM64 CLI | Execution on macOS ARM64, separate from cross-built Intel/Windows |
| Rosetta CLI | Explicit translated execution, never called native Intel acceptance |
| Source 4D | Fresh `build/source-host-status.json`, namespaced Client calls, UTF-8/large stdin through EOF and structured errors |
| Compiler | Fresh `build/compile-status.json`, required targets, input/output digests and real CompiledCode/Libraries |
| Package | `CardDAV.4dbase/CardDAV.4DZ`, stripped `.4dm` source, explicit nested directories, executable modes, notices and generated completion metadata |
| Compiled 4D | Fresh `build/compiled-host-status.json` from the exact unpacked final ZIP, compiled mode true |

Build-only Windows/Intel artifacts are not native execution results. Go mock
TLS clients use test-only trust configuration; production certificate
verification is never disabled. The host fixture exercises offline contact
operations and process transport without requiring account credentials.

## Separate gates

Native Intel 4D, native Windows x86_64 CLI/4D and DLL dependency checks,
live authorized iCloud discovery/CRUD, published Dependency Manager
installation, and signing/notarization require separate evidence. Publish
requires signing/notarization; local and CI inspection can remain unsigned.
Windows ARM64 is unsupported. Mock success does not establish server-specific
live behavior; no live account or previously authorized CalDAV Keychain item
is used for this component.

Local acceptance is intentionally `release_ready:false`. The retained CI and
Publish gates must not be bypassed to turn partial local evidence into a
release claim. Component tags must equal bare SemVer (`0.1.0`, never
`v0.1.0`); this local flow creates no tag, release, push or publication.
