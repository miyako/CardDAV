"""Version changes use isolated copies of the initialized CardDAV destination."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
from unittest.mock import patch
import uuid


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import bump_release
import rename_feature
import test_host_runner
import test_hosted_acceptance
import test_release_assets
import test_sign_release


class BumpReleaseTests(unittest.TestCase):
    def setUp(self):
        self.root = ROOT / (".bump-release-test-" + uuid.uuid4().hex)
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)
        for name in ("component", "cli", "tests/host"):
            shutil.copytree(ROOT / name, self.root / name,
                            ignore=shutil.ignore_patterns("bin", "DerivedData", "Libraries", "__pycache__"))
        for name in ("template.json", "package.json", "README.md", "release-policy.json"):
            shutil.copy2(ROOT / name, self.root / name)
        shutil.copy2(ROOT / "tests/test_cli.py", self.root / "tests/test_cli.py")
        shutil.copy2(ROOT / "tests/cli-contract.json", self.root / "tests/cli-contract.json")
        self.git("init", "-q")
        self.git("config", "user.name", "Release fixture")
        self.git("config", "user.email", "release-fixture@example.invalid")
        self.config = json.loads((self.root / "template.json").read_text())
        self.version = self.config["version"]
        self.component = "component/" + self.config["repository"]
        self.feature = self.config["feature_class"]
        self.repository = "owner/" + self.config["repository"]
        self.git("remote", "add", "origin", "https://github.com/" + self.repository + ".git")
        self.git("commit", "-q", "--allow-empty", "-m", "fixture")
        self.environment = patch.dict(os.environ, {"GITHUB_REPOSITORY": self.repository})
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, check=True,
                              capture_output=True, text=True, encoding="utf-8").stdout.strip()

    def configure(self, language="go"):
        # Alternate language registries are synthetic fixtures, not a second
        # initialization of the adopted component.
        config = json.loads((self.root / "template.json").read_text())
        config["language"] = language
        (self.root / "template.json").write_text(json.dumps(config, indent=2) + "\n")
        cli = self.root / "cli"
        version = config["version"]
        if language == "rust":
            (cli / "src").mkdir(exist_ok=True)
            (cli / "src/main.rs").write_text(
                'fn main() { println!(r#"{{"protocol":1,"version":"' + version + '"}}"#); }\n')
            package = 'name = "' + config["cli"] + '"\nversion = "' + version + '"\n'
            (cli / "Cargo.toml").write_text("[package]\n" + package + "\n[dependencies]\n")
            (cli / "Cargo.lock").write_text("version = 3\n\n[[package]]\n" + package)
        elif language == "cpp":
            (cli / "main.cpp").write_text(
                '#include <iostream>\nint main() { std::cout << R"({"protocol":1,"version":"'
                + version + '"})"; }\n')
            (cli / "CMakeLists.txt").write_text(
                "cmake_minimum_required(VERSION 3.16)\nproject(" + config["cli"] + " LANGUAGES CXX)\n")

    def snapshot(self):
        return {str(p.relative_to(self.root)): p.read_bytes()
                for p in self.root.rglob("*") if p.is_file() and ".git" not in p.parts}

    def test_all_languages_case_identity_renamed_class_and_idempotency(self):
        pristine = self.snapshot()
        for language in ("go", "rust", "cpp"):
            with self.subTest(language=language):
                for child in list(self.root.iterdir()):
                    if child.name != ".git":
                        if child.is_dir():
                            shutil.rmtree(child)
                        else:
                            child.unlink()
                for relative, content in pristine.items():
                    path = self.root / relative
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(content)
                self.configure(language)
                rename_feature.rename_feature(self.root, "Identifier")
                feature = self.root / self.component / "Project/Sources/Classes/Identifier.4dm"
                feature.write_text(feature.read_text() + '\n// unrelated version 8.9.0\n'
                                   'Function fixtureVersion()\n\tThis.version:="' + self.version + '"\n')
                before = self.snapshot()
                result = bump_release.apply(self.root, "1.2.3")
                changed = sorted(name for name, value in self.snapshot().items() if before[name] != value)
                self.assertEqual(result["changed_paths"], changed)
                self.assertEqual(result["tag"], "1.2.3")
                self.assertIn('This.version:="1.2.3"', feature.read_text())
                self.assertIn("// unrelated version 8.9.0", feature.read_text())
                self.assertEqual(json.loads((self.root / "template.json").read_text())["version"], "1.2.3")
                cli_test = (self.root / "tests/test_cli.py").read_text()
                if '"version":"' + self.version + '"' in before["tests/test_cli.py"].decode():
                    self.assertIn('"version":"1.2.3"', cli_test)
                else:
                    self.assertNotIn("tests/test_cli.py", result["changed_paths"])
                    self.assertEqual(cli_test.encode(), before["tests/test_cli.py"])
                _, plan = bump_release.plan(self.root, "1.2.3")
                self.assertEqual(plan, {})
                snapshot = self.snapshot()
                self.assertEqual(bump_release.apply(self.root, "1.2.3")["changed_paths"], [])
                self.assertEqual(snapshot, self.snapshot())
                if language == "rust":
                    for name in ("Cargo.toml", "Cargo.lock"):
                        self.assertIn('version = "1.2.3"', (self.root / "cli" / name).read_text())

    def test_next_highest_stable_numeric_tag_and_modes(self):
        self.configure()
        for tag in ("1.9.9", "1.10.2", "1.10.1", "20.0.0-rc.1", "9.0.0+build",
                    "01.0.0", "v01.0.0", "v20.0.0-rc.1", "v9.0.0+build", "20R3.2", "unrelated"):
            self.git("tag", tag)
        before = self.snapshot()
        for mode, version in (("patch", "1.10.3"), ("minor", "1.11.0"), ("major", "2.0.0")):
            self.assertEqual(bump_release.next_version(self.root, mode),
                             {"version": version, "tag": version})
        self.assertEqual(before, self.snapshot())

    def test_legacy_and_mixed_tag_history_uses_highest_baseline_without_rewriting(self):
        self.configure()
        for tag in ("v1.10.2", "1.9.9"):
            self.git("tag", tag)
        self.assertEqual(bump_release.next_version(self.root, "patch"),
                         {"version": "1.10.3", "tag": "1.10.3"})
        self.git("tag", "1.11.0")
        before = self.git("show-ref", "--tags")
        self.assertEqual(bump_release.next_version(self.root, "patch"),
                         {"version": "1.11.1", "tag": "1.11.1"})
        self.assertEqual(self.git("show-ref", "--tags"), before)
        self.assertEqual(bump_release.next_version(self.root, "minor", tags=["v2.3.4"]),
                         {"version": "2.4.0", "tag": "2.4.0"})

    def test_no_stable_tag_bumps_configured_base(self):
        self.configure()
        bump_release.apply(self.root, "3.4.5")
        for tag in ("99.0.0-preview", "v99.0.0-preview", "20R3.2"):
            self.git("tag", tag)
        for mode, version in (("patch", "3.4.6"), ("minor", "3.5.0"), ("major", "4.0.0")):
            self.assertEqual(bump_release.next_version(self.root, mode)["version"], version)

    def test_retry_equal_version_validates_all_surfaces_and_does_not_bump_again(self):
        self.configure()
        bump_release.apply(self.root, "1.2.4")
        for tags in (["1.2.3"], ["v1.2.3"], ["1.2.3", "v1.2.3", "2.0.0-rc.1"]):
            self.assertEqual(bump_release.next_version(self.root, "patch", tags=tags),
                             {"version": "1.2.4", "tag": "1.2.4"})
        contract = self.root / "tests/host/contract.json"
        contract.write_text(contract.read_text().replace('$VERSION', '1.2.3').replace(
            '\\"version\\":\\"1.2.4\\"', '\\"version\\":\\"1.2.3\\"').replace(
                '"version": "1.2.4"', '"version": "1.2.3"'))
        before = self.snapshot()
        with self.assertRaisesRegex(ValueError, "inconsistent"):
            bump_release.next_version(self.root, "patch", tags=["v1.2.3"])
        self.assertEqual(before, self.snapshot())

    def test_next_refuses_configured_version_ahead_of_computed_release(self):
        self.configure()
        bump_release.apply(self.root, "3.4.5")
        before = self.snapshot()
        for mode in ("patch", "minor", "major"):
            with self.subTest(mode=mode), self.assertRaisesRegex(ValueError, "ahead"):
                bump_release.next_version(self.root, mode, tags=["v1.2.3"])
        self.assertEqual(before, self.snapshot())

    def test_empty_optional_feature_version_is_rejected_on_equal_retry(self):
        self.configure()
        feature = self.root / self.component / ("Project/Sources/Classes/" + self.feature + ".4dm")
        feature.write_text(feature.read_text() + '\nFunction fixtureVersion()\n\tThis.version:=""\n')
        before = self.snapshot()
        with self.assertRaisesRegex(ValueError, "inconsistent"):
            bump_release.apply(self.root, self.version, stage=True)
        with self.assertRaisesRegex(ValueError, "inconsistent"):
            bump_release.next_version(self.root, "patch", tags=["v0.0.9"])
        self.assertEqual(before, self.snapshot())
        self.assertEqual(self.git("diff", "--cached", "--name-only"), "")

    def test_stage_adds_only_changed_paths_and_empty_retry_is_noop(self):
        self.configure()
        self.git("add", "--", ".")
        self.git("commit", "-q", "-m", "initialized fixture")
        unrelated = self.root / "unrelated.txt"
        unrelated.write_text("must remain untracked\n")
        result = bump_release.apply(self.root, "0.2.0", stage=True)
        staged = self.git("diff", "--cached", "--name-only").splitlines()
        self.assertEqual(staged, result["changed_paths"])
        self.assertNotIn("unrelated.txt", staged)
        self.git("commit", "-q", "-m", "bump fixture")
        self.assertEqual(bump_release.apply(self.root, "0.2.0", stage=True)["changed_paths"], [])
        self.assertEqual(self.git("diff", "--cached", "--name-only"), "")
        self.assertEqual(unrelated.read_text(), "must remain untracked\n")

    def test_stage_validation_failure_neither_writes_nor_stages(self):
        self.configure()
        self.git("add", "--", ".")
        self.git("commit", "-q", "-m", "initialized fixture")
        contract = self.root / "tests/host/contract.json"
        contract.write_text(contract.read_text().replace('$VERSION', '0.0.1').replace(
            self.version, '0.0.1'))
        before = self.snapshot()
        with self.assertRaisesRegex(ValueError, "inconsistent"):
            bump_release.apply(self.root, "0.2.0", stage=True)
        self.assertEqual(before, self.snapshot())
        self.assertEqual(self.git("diff", "--cached", "--name-only"), "")

    def test_uninitialized_original_and_exact_destination_guards(self):
        config_path = self.root / "template.json"
        original = config_path.read_text()
        config = json.loads(original)
        config.update(initialized=False, language=None)
        config_path.write_text(json.dumps(config))
        before = self.snapshot()
        with self.assertRaises(ValueError):
            bump_release.apply(self.root, "1.0.0")
        self.assertEqual(before, self.snapshot())
        config_path.write_text(original)
        for repository in ("owner/carddav", "other/CardDAV", "owner/Wrong"):
            with self.subTest(repository=repository), self.assertRaises(ValueError):
                bump_release.apply(self.root, "1.0.0", repository)
        self.git("remote", "set-url", "origin", "https://github.com/miyako/4d-component-template.git")
        with patch.dict(os.environ, {"GITHUB_REPOSITORY": "miyako/4d-component-template"}):
            with self.assertRaisesRegex(ValueError, "original template"):
                bump_release.apply(self.root, "1.0.0")

    def test_invalid_new_versions_make_no_changes(self):
        self.configure()
        before = self.snapshot()
        for version in ("v1.0.0", "01.2.3", "1.2", "1.2.3\n", "1.2.3-beta", "1.2.3+build", None):
            with self.subTest(version=version), self.assertRaises(ValueError):
                bump_release.apply(self.root, version)
            self.assertEqual(before, self.snapshot())

    def test_inconsistent_or_malformed_surfaces_fail_before_writes(self):
        self.configure("rust")
        targets = {
            "template.json": ('"version": "' + self.version + '"', '"version": "not-semver"'),
            "package.json": ('"version": "' + self.version + '"', '"version": "999999.0.0"'),
            "cli/Cargo.toml": ('version = "' + self.version + '"', 'version = "999999.0.0"'),
            "cli/Cargo.lock": ('version = "' + self.version + '"', 'version = "999999.0.0"'),
            "cli/src/main.rs": ('"version":"' + self.version + '"', '"version":"bad"'),
            "tests/host/Project/Sources/Methods/Smoke.4dm": ('$request.version', '"0.3.0"'),
            "tests/host/contract.json": ('$VERSION', '0.8.0'),
            "README.md": ('"version":"' + self.version + '"', '"version":"0.9.0"'),
        }
        for relative, (old, new) in targets.items():
            path = self.root / relative
            text = path.read_text()
            self.assertIn(old, text)
            path.write_text(text.replace(old, new))
            before = self.snapshot()
            with self.subTest(relative=relative), self.assertRaises(ValueError):
                bump_release.apply(self.root, "1.2.3")
            self.assertEqual(before, self.snapshot())
            path.write_text(text)

    def test_preserves_history_policy_dependencies_and_unrelated_versions(self):
        self.configure("rust")
        readme = self.root / "README.md"
        original = readme.read_text()
        history = ('\nHistorical release v0.1.0; dependency 0.1.0\n'
                   'Historical response {"protocol":1,"version":"0.1.0"}\n')
        readme.write_text(original + history)
        cargo = self.root / "cli/Cargo.lock"
        cargo.write_text(cargo.read_text() + '\n[[package]]\nname = "third-party"\nversion = "0.1.0"\n')
        manifest = self.root / "cli/Cargo.toml"
        manifest.write_text(manifest.read_text() + 'third-party = "0.1.0"\n')
        policy = self.root / "release-policy.json"
        policy.write_text('{"schema":1,"apple_notarized_versions":["0.1.0"]}\n')
        bump_release.apply(self.root, "0.2.0")
        after = readme.read_text()
        # Only the live CLI response example changes, not release commands,
        # upstream links, dependency-manager examples or historical observations.
        self.assertEqual(after, original.replace('"version":"' + self.version + '"', '"version":"0.2.0"') + history)
        self.assertIn('name = "third-party"\nversion = "0.1.0"', cargo.read_text())
        self.assertIn('third-party = "0.1.0"', manifest.read_text())
        self.assertIn('["0.1.0"]', policy.read_text())
        self.assertEqual(json.loads((self.root / "package.json").read_text())["tool"]["version"], "20.7")

    def test_injected_go_version_assignment(self):
        self.configure()
        source = self.root / "cli/main.go"
        source.write_text('package main\nimport "fmt"\nvar version string = "' + self.version
                          + '"\nfunc main() { fmt.Printf(`{"protocol":1,"version":"%s"}`, version) }\n')
        bump_release.apply(self.root, "2.3.4")
        self.assertIn('var version string = "2.3.4"', source.read_text())
        self.assertIn('"version":"%s"', source.read_text())

    def test_dynamic_cli_test_binding_is_required_and_preserved(self):
        source = self.root / "tests/test_cli.py"
        original = (
            'import json\nfrom pathlib import Path\n'
            'ROOT = Path(__file__).resolve().parents[1]\n'
            'class CLITests:\n'
            '    @classmethod\n'
            '    def setUpClass(cls):\n'
            '        config = json.loads((ROOT / "template.json").read_text())\n'
            '        cls.version = config["version"]\n')
        source.write_text(original)
        result = bump_release.apply(self.root, "1.2.3")
        self.assertNotIn("tests/test_cli.py", result["changed_paths"])
        self.assertEqual(source.read_text(), original)
        source.write_text(original.replace('cls.version = config["version"]', 'cls.version = "stale"'))
        before = self.snapshot()
        with self.assertRaisesRegex(ValueError, "missing supported component version surface"):
            bump_release.apply(self.root, "2.3.4", stage=True)
        self.assertEqual(before, self.snapshot())
        self.assertEqual(self.git("diff", "--cached", "--name-only"), "")

    def test_rust_cargo_package_version_injection(self):
        self.configure("rust")
        source = self.root / "cli/src/main.rs"
        source.write_text(source.read_text().replace(
            'println!(r#"{{"protocol":1,"version":"' + self.version + '"}}"#);',
            'println!(r#"{{"protocol":1,"version":"{}"}}"#, env!("CARGO_PKG_VERSION"));'))
        before = source.read_bytes()
        result = bump_release.apply(self.root, "2.3.4")
        self.assertEqual(before, source.read_bytes())
        self.assertNotIn("cli/src/main.rs", result["changed_paths"])
        self.assertIn('version = "2.3.4"', (self.root / "cli/Cargo.toml").read_text())

    def test_cpp_constant_and_cmake_project_version(self):
        self.configure("cpp")
        source = self.root / "cli/main.cpp"
        source.write_text('constexpr auto kVersion = "' + self.version + '";\n' + source.read_text().replace(
            'R"({"protocol":1,"version":"' + self.version + '"})"',
            'R"({"protocol":1,"version":")" << kVersion << R"("})"'))
        cmake = self.root / "cli/CMakeLists.txt"
        cmake.write_text(cmake.read_text().replace(
            "project(" + self.config["cli"] + " LANGUAGES CXX)",
            "project(" + self.config["cli"] + " VERSION " + self.version + " LANGUAGES CXX)")
                        + '\nset(CLI_VERSION "' + self.version + '")\n')
        bump_release.apply(self.root, "2.3.4")
        self.assertIn('kVersion = "2.3.4"', source.read_text())
        self.assertIn("project(" + self.config["cli"] + " VERSION 2.3.4 LANGUAGES CXX)", cmake.read_text())
        self.assertIn('set(CLI_VERSION "2.3.4")', cmake.read_text())
        self.assertIn("VERSION 3.16", cmake.read_text())

    def test_malformed_contract_and_duplicate_metadata_fail_without_writes(self):
        self.configure()
        for relative, edit in (
                ("template.json", lambda text: text.replace('"version": "' + self.version + '"',
                                                           '"version": "' + self.version
                                                           + '", "version": "' + self.version + '"')),
                ("tests/host/contract.json", lambda text: text.replace(
                    '\\"protocol\\":1,\\"version\\":\\"$VERSION\\"', 'not json')),
                ("tests/cli-contract.json", lambda text: text.replace(
                    '"version": "$VERSION"', '"version": "bad"'))):
            path = self.root / relative
            text = path.read_text()
            changed = edit(text)
            self.assertNotEqual(text, changed, relative)
            path.write_text(changed)
            before = self.snapshot()
            with self.subTest(relative=relative), self.assertRaises(ValueError):
                bump_release.apply(self.root, "2.3.4")
            self.assertEqual(before, self.snapshot())
            path.write_text(text)

    def test_cli_outputs_json_changed_paths_and_github_outputs(self):
        self.configure()
        output = self.root / "github-output"
        env = dict(os.environ, GITHUB_OUTPUT=str(output))
        command = [sys.executable, str(ROOT / "scripts/bump_release.py"), "--root", str(self.root)]
        next_result = subprocess.run(command + ["next", "--mode", "minor"], env=env,
                                     capture_output=True, text=True, check=True)
        major, minor, _ = map(int, self.version.split("."))
        next_minor = f"{major}.{minor + 1}.0"
        self.assertEqual(json.loads(next_result.stdout)["version"], next_minor)
        applied = subprocess.run(command + ["apply", "--version", "0.2.0", "--stage"], env=env,
                                 capture_output=True, text=True, check=True)
        result = json.loads(applied.stdout)
        self.assertTrue(result["changed_paths"])
        lines = output.read_text().splitlines()
        self.assertIn("version=0.2.0", lines)
        self.assertIn("tag=0.2.0", lines)
        self.assertFalse(any(line.startswith("tag=v") for line in lines))
        self.assertEqual(json.loads(next(line.split("=", 1)[1] for line in lines
                                       if line.startswith("changed_paths="))), result["changed_paths"])
        self.assertEqual(self.git("diff", "--cached", "--name-only").splitlines(), result["changed_paths"])

    def test_missing_and_symlink_surfaces_fail_without_writes(self):
        self.configure()
        source = self.root / "cli/main.go"
        content = source.read_bytes()
        source.unlink()
        before = self.snapshot()
        with self.assertRaises(ValueError):
            bump_release.apply(self.root, "1.0.0")
        self.assertEqual(before, self.snapshot())
        source.symlink_to(self.root / "README.md")
        before = self.snapshot()
        with self.assertRaises(ValueError):
            bump_release.apply(self.root, "1.0.0")
        self.assertEqual(before, self.snapshot())
        source.unlink()
        source.write_bytes(content)

    def test_bumped_version_retains_host_report_signing_and_publication_gates(self):
        dynamic = {relative: (self.root / relative).read_bytes() for relative in (
            "tests/host/contract.json", "tests/host/Project/Sources/Methods/Smoke.4dm")}
        self.assertIn(b"$VERSION", dynamic["tests/host/contract.json"])
        self.assertIn(b"$request.version", dynamic["tests/host/Project/Sources/Methods/Smoke.4dm"])
        bump_release.apply(self.root, "1.2.3")
        version = json.loads((self.root / "template.json").read_text())["version"]
        self.assertIn('"version":"1.2.3"',
                      (self.root / self.component / "Documentation/Protocol.md").read_text())
        self.assertEqual(bump_release.apply(self.root, version)["changed_paths"], [])
        for relative, content in dynamic.items():
            self.assertEqual((self.root / relative).read_bytes(), content)
        gates = (
            (test_host_runner.HostRunnerTests, (
                "test_source_and_unpacked_release_staged_and_cleaned",)),
            (test_hosted_acceptance.HostedAcceptanceTests, (
                "test_accepted_zip_has_embedded_native_and_both_host_reports",
                "test_prefixed_tag_is_rejected_before_host_or_package_acceptance")),
            (test_release_assets.ReleaseAssetsTests, (
                "test_unsigned_exact_bytes_and_complete_contract_pass_without_secrets",
                "test_signed_publication_requires_proof_and_separate_final_host",
                "test_legacy_prefixed_signing_proof_cannot_approve_new_release",
                "test_prefixed_publication_never_creates_tag_or_release",
                "test_gh_receives_only_verified_literal_assets_and_existing_tag")),
            (test_sign_release.SigningTests, (
                "test_manual_release_identity_overrides_workflow_branch_and_checks_head",
                "test_preflight_platform_actions_identity_policy_and_commit")),
        )
        for case_type, methods in gates:
            for method in methods:
                with self.subTest(gate=case_type.__name__, method=method, version=version):
                    fixture = case_type(method)
                    fixture.version = version
                    try:
                        fixture.setUp()
                        getattr(fixture, method)()
                    finally:
                        fixture.doCleanups()


if __name__ == "__main__":
    unittest.main()
