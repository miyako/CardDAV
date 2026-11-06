"""Native, offline CardDAV stdin contract tests (no account or network access)."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


def json_line(value):
    return (json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")


class GoCLI(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        selected = os.environ.get("TEST_CLI_LANGUAGE")
        if selected == "none":
            raise unittest.SkipTest("CLI toolchains explicitly disabled")
        if selected not in (None, "go"):
            raise RuntimeError("CardDAV requires the selected Go implementation")
        if shutil.which("go") is None:
            raise RuntimeError("Go toolchain required")
        config = json.loads((ROOT / "template.json").read_text(encoding="utf-8"))
        if config["language"] != "go" or config["cli"] != "carddav":
            raise RuntimeError("CardDAV CLI configuration mismatch")
        cls.version = config["version"]
        scratch = tempfile.TemporaryDirectory(prefix=".carddav CLI path ", dir=ROOT / "tests")
        cls.addClassCleanup(scratch.cleanup)
        cls.executable = Path(scratch.name) / ("carddav.exe" if os.name == "nt" else "carddav")
        env = os.environ.copy()
        env.pop("GOOS", None)
        env.pop("GOARCH", None)
        env.update(CGO_ENABLED="0", GOTOOLCHAIN="local")
        subprocess.run(
            ["go", "build", "-mod=readonly", "-o", str(cls.executable), "."],
            cwd=ROOT / "cli", env=env, check=True, capture_output=True, timeout=180,
        )

    def invoke(self, args, data=b""):
        return subprocess.run(
            [str(self.executable), *args], input=data, capture_output=True, timeout=30,
        )

    def test_version(self):
        result = self.invoke(["--version"])
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b'{"protocol":1,"version":"0.1.0"}\n')
        self.assertEqual(result.stderr, b"")

    def test_native_contract(self):
        contract = json.loads((ROOT / "tests" / "cli-contract.json").read_text(encoding="utf-8"))
        self.assertEqual(contract["schema"], 1)
        for case in contract["cases"]:
            with self.subTest(args=case["args"], stdin=case.get("stdin")):
                data = case.get("stdin", "")
                data = json_line(data) if isinstance(data, dict) else data.encode("utf-8")
                result = self.invoke(case["args"], data)
                self.assertEqual(result.returncode, case["exitCode"])
                for stream in ("stdout", "stderr"):
                    expected = case[stream]
                    if isinstance(expected, dict):
                        expected = json.loads(json.dumps(expected).replace("$VERSION", self.version))
                        expected = json_line(expected)
                    else:
                        expected = expected.encode("utf-8")
                    self.assertEqual(getattr(result, stream), expected)

    def test_unicode_raw_round_trip(self):
        contact = {
            "uid": "stable-id", "formattedName": '世界 café 😀 "quoted"; ,\\\nnext',
            "note": "😀" * 100, "emails": [{"value": "a@example.org", "types": ["home"]}],
        }
        for version in ("3.0", "4.0"):
            built = self.invoke(["request"], json_line({
                "protocol": 1, "operation": "build", "version": version, "contact": contact,
            }))
            self.assertEqual(built.returncode, 0, built.stderr)
            card = json.loads(built.stdout)["result"]
            parsed = self.invoke(["request"], json_line({
                "protocol": 1, "operation": "parse", "vcard": card["vcard"],
            }))
            self.assertEqual(parsed.returncode, 0, parsed.stderr)
            value = json.loads(parsed.stdout)["result"]
            self.assertEqual(value["vcard"], card["vcard"])
            self.assertEqual(value["uid"], contact["uid"])
            self.assertEqual(value["formattedName"], contact["formattedName"])
            self.assertEqual(value["version"], version)
            for line in card["vcard"].split("\r\n"):
                self.assertLessEqual(len(line.encode("utf-8")), 75)

    def test_malformed_input_and_private_error_redaction(self):
        for data in (
            b"", b"[]", b"{}\n{}", b"\xff", b" " * (4 * 1024 * 1024 + 1),
            b'{"protocol":1,"operation":"parse","vcard":"PRIVATE CONTENT"}',
            b'{"protocol":1,"operation":"get","endpoint":"https://user:SECRET@example.org/a"}',
            b'{"protocol":1,"operation":"get","endpoint":"https://127.0.0.1/a"}',
            b'{"protocol":1,"operation":"build","contact":{"formattedName":"a","uid":"1","uid":"2"}}',
        ):
            with self.subTest(length=len(data)):
                result = self.invoke(["request"], data)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, b"")
                error = json.loads(result.stderr)
                self.assertEqual(error["protocol"], 1)
                self.assertTrue(result.stderr.endswith(b"\n"))
                for secret in (b"SECRET", b"PRIVATE CONTENT", b"127.0.0.1", b"example.org"):
                    self.assertNotIn(secret, result.stderr)

    def test_no_secret_argv(self):
        for args in ([], ["unknown"], ["request", "--password", "SECRET"], ["--version", "extra"]):
            result = self.invoke(args)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, b"")
            self.assertNotIn(b"SECRET", result.stderr)


if __name__ == "__main__":
    unittest.main()
