import json
import hashlib
import http.server
import os
import pathlib
import platform
import re
import shutil
import shlex
import subprocess
import sys
import tempfile
import threading
import unittest
from copy import deepcopy

ROOT = pathlib.Path(__file__).resolve().parents[1]
INSTALL = ROOT / "web" / "public" / "install"
VALIDATOR = ROOT / "scripts" / "validate-onboarding-manifest.py"
REQUIRED = {
    "codex-cli", "codex-desktop", "claude-code", "claude-desktop",
    "opencode", "continue", "aider", "cursor", "cline", "roo-code",
    "openai-sdk", "langchain",
}


class OnboardingAssetsTest(unittest.TestCase):
    def platform_artifacts(self):
        operating_system = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
        architecture = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine()]
        return operating_system, architecture

    def bootstrap_site(self, root, onboarding_body=None, operating_system=None):
        detected_operating_system, architecture = self.platform_artifacts()
        operating_system = operating_system or detected_operating_system
        install = root / "install"
        binary_directory = install / "bin"
        binary_directory.mkdir(parents=True)
        onboard = f"llmapi-onboard-{operating_system}-{architecture}"
        bridge = f"llmapi-mcp-bridge-{operating_system}-{architecture}"
        (binary_directory / onboard).write_text(onboarding_body or "#!/usr/bin/env bash\nprintf invoked > \"$HOME/onboard-ran\"\n")
        (binary_directory / bridge).write_text("#!/usr/bin/env bash\nexit 0\n")
        for artifact in (onboard, bridge):
            (binary_directory / artifact).chmod(0o700)
        checksums = []
        for artifact in (onboard, bridge):
            digest = hashlib.sha256((binary_directory / artifact).read_bytes()).hexdigest()
            checksums.append(f"{digest}  {artifact}")
        (install / "checksums.txt").write_text("\n".join(checksums) + "\n")
        return onboard, bridge

    def serve(self, root, redirect_checksums=False):
        paths = []

        class Handler(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=str(root), **kwargs)

            def do_GET(self):
                paths.append(self.path)
                if redirect_checksums and self.path == "/install/checksums.txt":
                    self.send_response(302)
                    self.send_header("Location", "/redirected")
                    self.end_headers()
                    return
                super().do_GET()

            def log_message(self, *_):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join, 1)
        self.addCleanup(server.shutdown)
        return f"http://127.0.0.1:{server.server_port}", paths

    def run_bash_bootstrap(self, home, origin, extra_environment=None):
        environment = os.environ.copy()
        environment.update({"HOME": str(home), "LLMAPI_SERVER_URL": origin})
        if extra_environment:
            environment.update(extra_environment)
        return subprocess.run(
            ["bash", str(INSTALL / "install.sh")],
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            env=environment,
            check=False,
        )

    def load_manifest(self):
        return json.loads((INSTALL / "manifest.json").read_text())

    def run_validator(self, manifest):
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as temp:
            json.dump(manifest, temp)
            temp_path = temp.name
        try:
            return subprocess.run(
                [sys.executable, str(VALIDATOR), temp_path],
                capture_output=True,
                text=True,
                check=False,
            )
        finally:
            pathlib.Path(temp_path).unlink(missing_ok=True)

    def assert_validator_fails(self, manifest, expected_message):
        result = self.run_validator(manifest)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(expected_message, result.stderr)

    def test_manifest_has_every_approved_client_and_explicit_mode(self):
        data = self.load_manifest()
        clients = {item["id"]: item for item in data["clients"]}
        self.assertEqual(set(clients), REQUIRED)
        self.assertEqual(clients["claude-desktop"]["integration"], "mcp-tool")
        self.assertEqual(clients["claude-code"]["integration"], "direct-anthropic")
        self.assertEqual(clients["codex-desktop"]["configFamily"], "codex")
        self.assertEqual(data["schemaVersion"], 1)
        self.assertEqual(
            data["artifacts"]["binaries"],
            [
                {"id": "llmapi-onboard", "namePattern": "llmapi-onboard-{os}-{arch}[.exe]"},
                {"id": "llmapi-mcp-bridge", "namePattern": "llmapi-mcp-bridge-{os}-{arch}[.exe]"},
            ],
        )

    def test_validator_rejects_duplicate_client_ids(self):
        data = self.load_manifest()
        duplicate = deepcopy(data)
        duplicate["clients"].append(deepcopy(duplicate["clients"][0]))
        self.assert_validator_fails(duplicate, "duplicate client id")

    def test_validator_rejects_unsafe_http_loopback_lookalike(self):
        data = self.load_manifest()
        data["defaultServerUrl"] = "http://127.example.com"
        self.assert_validator_fails(data, "must use https or a loopback http URL")

    def test_validator_rejects_unsupported_integration_mode(self):
        data = self.load_manifest()
        data["clients"][0]["integration"] = "unsupported-mode"
        self.assert_validator_fails(data, "unsupported integration mode")

    def test_validator_rejects_unsupported_delivery_mode(self):
        data = self.load_manifest()
        data["clients"][0]["delivery"] = "manual"
        self.assert_validator_fails(data, "unsupported delivery mode")

    def test_validator_rejects_unknown_schema_version(self):
        data = self.load_manifest()
        data["schemaVersion"] = 2
        self.assert_validator_fails(data, "unsupported schemaVersion")

    def test_validator_rejects_claude_desktop_direct_access(self):
        data = self.load_manifest()
        for client in data["clients"]:
            if client["id"] == "claude-desktop":
                client["integration"] = "direct-responses"
                break
        self.assert_validator_fails(data, "Claude Desktop must not be configured as direct access")

    def test_bootstraps_verify_and_durably_install_both_helpers(self):
        sh = (INSTALL / "install.sh").read_text()
        ps = (INSTALL / "install.ps1").read_text()
        bootstrap = sh + ps
        self.assertIn("LLMAPI_SERVER_URL", bootstrap)
        self.assertNotRegex(bootstrap, r"--(api-)?token")
        self.assertIn("checksums.txt", bootstrap)
        self.assertIn("/install/bin/$onboard_artifact", sh)
        self.assertIn("/install/bin/$bridge_artifact", sh)
        self.assertIn("/install/bin/$onboardArtifact", ps)
        self.assertIn("/install/bin/$bridgeArtifact", ps)
        self.assertIn("llmapi-onboard", bootstrap)
        self.assertIn("llmapi-mcp-bridge", bootstrap)
        self.assertIn("sha256sum", sh)
        self.assertIn("shasum -a 256", sh)
        self.assertIn("mktemp -d", sh)
        self.assertIn("chmod 700", sh)
        self.assertIn("trap", sh)
        self.assertIn('rm -rf "$temporary_directory"', sh)
        self.assertIn("$HOME/.llmapi/bin", sh)
        self.assertIn("Move-Item", ps)
        self.assertRegex(ps, r"Get-FileHash.+SHA256")
        self.assertIn("New-TemporaryFile", ps)
        self.assertIn("finally", ps)
        self.assertIn("Set-Acl", ps)

    def test_page_is_token_free_and_explains_all_client_modes(self):
        html = (INSTALL / "index.html").read_text()
        self.assertIn("window.location.origin", html)
        self.assertIn("MCP tool access", html)
        self.assertIn("does not replace Claude Desktop", html)
        self.assertIn("/v1/models", html)
        self.assertIn("fallback", html.lower())
        self.assertIn("navigator.clipboard.writeText", html)
        self.assertIn("aria-live", html)
        self.assertIn("mktemp", html)
        self.assertIn("MaximumRedirection 0", html)
        self.assertNotIn(" | iex", html)
        self.assertNotIn(" | LLMAPI_SERVER_URL", html)
        self.assertNotRegex(html, r"type=[\"']password")
        self.assertNotRegex(html, r"(?:local|session)Storage")
        self.assertNotRegex(html, r"--(api-)?token")

    def test_page_renders_every_manifest_client_and_disables_unsupported_platforms(self):
        html = (INSTALL / "index.html").read_text()
        self.assertIn("for (const client of manifest.clients)", html)
        self.assertIn("choice.disabled = !isSupported", html)
        self.assertIn("Unsupported on this platform", html)
        self.assertNotIn("manifest.clients.filter", html)

    def test_bash_bootstrap_downloads_bin_artifacts_cleans_before_invocation_and_keeps_noninteractive_stdin(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            home.mkdir()
            onboard, bridge = self.bootstrap_site(
                root,
                "#!/usr/bin/env bash\n"
                "test -z \"$(find \"$HOME/.llmapi/bin\" -maxdepth 1 -name '.install.*' -print)\" || exit 77\n"
                "test -t 0 && printf tty > \"$HOME/input-mode\" || printf noninteractive > \"$HOME/input-mode\"\n"
                "printf invoked > \"$HOME/onboard-ran\"\n",
            )
            origin, paths = self.serve(root)
            result = self.run_bash_bootstrap(home, origin)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("/install/checksums.txt", paths)
            self.assertIn(f"/install/bin/{onboard}", paths)
            self.assertIn(f"/install/bin/{bridge}", paths)
            self.assertEqual((home / "onboard-ran").read_text(), "invoked")
            self.assertEqual((home / "input-mode").read_text(), "noninteractive")
            self.assertFalse(any((home / ".llmapi" / "bin").glob(".install.*")))

    def test_bash_bootstrap_rejects_checksum_redirect_before_installing(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            home.mkdir()
            self.bootstrap_site(root)
            origin, paths = self.serve(root, redirect_checksums=True)
            result = self.run_bash_bootstrap(home, origin)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(paths, ["/install/checksums.txt"])
            self.assertFalse((home / ".llmapi" / "bin" / "llmapi-onboard").exists())

    def test_public_bash_command_cleans_up_and_returns_curl_status_under_errexit(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            self.bootstrap_site(root)
            origin, _ = self.serve(root, redirect_checksums=True)
            guide = (INSTALL / "guide.md").read_text()
            command = re.search(r"```sh\n(.*?)\n```", guide, re.DOTALL).group(1).replace("https://stage.example", origin)
            temp_directory = pathlib.Path(temporary) / "downloads"
            temp_directory.mkdir()
            wrappers = pathlib.Path(temporary) / "wrappers"
            wrappers.mkdir()
            (wrappers / "mktemp").write_text(f"#!/usr/bin/env bash\nprintf '%s/public-bootstrap' {shlex.quote(str(temp_directory))}\n")
            (wrappers / "mktemp").chmod(0o755)
            result = subprocess.run(
                ["bash", "-c", f"set -e; {command}"],
                env={**os.environ, "PATH": f"{wrappers}:{os.environ['PATH']}"},
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 22, result.stderr)
            self.assertEqual(list(temp_directory.iterdir()), [])

    @unittest.skipUnless(shutil.which("script"), "script is required for a pseudo-terminal fixture")
    def test_bash_bootstrap_routes_interactive_prompts_through_the_controlling_tty(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            home.mkdir()
            self.bootstrap_site(root, "#!/usr/bin/env bash\ntest -t 0 && printf tty > \"$HOME/input-mode\" || exit 78\n")
            origin, _ = self.serve(root)
            command = f"HOME={shlex.quote(str(home))} LLMAPI_SERVER_URL={shlex.quote(origin)} bash {shlex.quote(str(INSTALL / 'install.sh'))}"
            result = subprocess.run(["script", "-q", "/dev/null", "bash", "-c", command], capture_output=True, text=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((home / "input-mode").read_text(), "tty")

    def test_bash_bootstrap_rolls_back_both_helpers_when_second_replacement_fails(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            binary_directory = home / ".llmapi" / "bin"
            binary_directory.mkdir(parents=True)
            for name, content in (("llmapi-onboard", b"old-onboard"), ("llmapi-mcp-bridge", b"old-bridge")):
                path = binary_directory / name
                path.write_bytes(content)
                path.chmod(0o700)
            _, bridge = self.bootstrap_site(root)
            origin, _ = self.serve(root)
            wrapper_directory = pathlib.Path(temporary) / "wrappers"
            marker = pathlib.Path(temporary) / "second-replacement-reached"
            wrapper_directory.mkdir()
            (wrapper_directory / "mv").write_text(
                "#!/usr/bin/env bash\n"
                f"if [ \"$(basename \"$1\")\" = \"{bridge}\" ] && [ \"$(basename \"$2\")\" = \"llmapi-mcp-bridge\" ]; then printf reached > {shlex.quote(str(marker))}; exit 75; fi\n"
                "exec /bin/mv \"$@\"\n"
            )
            (wrapper_directory / "mv").chmod(0o755)
            result = self.run_bash_bootstrap(home, origin, {"PATH": f"{wrapper_directory}:{os.environ['PATH']}"})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(marker.read_text(), "reached")
            self.assertEqual((binary_directory / "llmapi-onboard").read_bytes(), b"old-onboard")
            self.assertEqual((binary_directory / "llmapi-mcp-bridge").read_bytes(), b"old-bridge")

    @unittest.skipUnless(platform.system() == "Darwin", "macOS ACL fixture")
    def test_bash_bootstrap_rerun_keeps_private_nonempty_bin_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            home.mkdir()
            self.bootstrap_site(root)
            origin, _ = self.serve(root)
            self.assertEqual(self.run_bash_bootstrap(home, origin).returncode, 0)
            retained = home / ".llmapi" / "bin" / "unrelated-private-file"
            retained.write_bytes(b"keep")
            retained.chmod(0o700)
            rerun = self.run_bash_bootstrap(home, origin)
            self.assertEqual(rerun.returncode, 0, rerun.stderr)
            self.assertEqual(retained.read_bytes(), b"keep")

    def test_bash_bootstrap_rolls_back_only_created_backups_when_second_backup_fails(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            binary_directory = home / ".llmapi" / "bin"
            binary_directory.mkdir(parents=True)
            for name, content in (("llmapi-onboard", b"old-onboard"), ("llmapi-mcp-bridge", b"old-bridge")):
                path = binary_directory / name
                path.write_bytes(content)
                path.chmod(0o700)
            self.bootstrap_site(root)
            origin, _ = self.serve(root)
            wrappers = pathlib.Path(temporary) / "wrappers"
            marker = pathlib.Path(temporary) / "second-backup-reached"
            wrappers.mkdir()
            (wrappers / "mv").write_text(
                "#!/usr/bin/env bash\n"
                f"if [ \"$(basename \"$1\")\" = llmapi-mcp-bridge ] && [ \"$(basename \"$2\")\" = llmapi-mcp-bridge ]; then printf reached > {shlex.quote(str(marker))}; exit 74; fi\n"
                "exec /bin/mv \"$@\"\n"
            )
            (wrappers / "mv").chmod(0o755)
            result = self.run_bash_bootstrap(home, origin, {"PATH": f"{wrappers}:{os.environ['PATH']}"})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(marker.read_text(), "reached")
            self.assertEqual((binary_directory / "llmapi-onboard").read_bytes(), b"old-onboard")
            self.assertEqual((binary_directory / "llmapi-mcp-bridge").read_bytes(), b"old-bridge")
            self.assertFalse(any(binary_directory.glob(".install.*")))

    def test_bash_bootstrap_sigterm_restores_pair_and_removes_transaction_state(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            binary_directory = home / ".llmapi" / "bin"
            binary_directory.mkdir(parents=True)
            for name, content in (("llmapi-onboard", b"old-onboard"), ("llmapi-mcp-bridge", b"old-bridge")):
                path = binary_directory / name
                path.write_bytes(content)
                path.chmod(0o700)
            onboard, _ = self.bootstrap_site(root)
            origin, _ = self.serve(root)
            wrappers = pathlib.Path(temporary) / "wrappers"
            marker = pathlib.Path(temporary) / "signal-stage-reached"
            wrappers.mkdir()
            (wrappers / "mv").write_text(
                "#!/usr/bin/env bash\n"
                f"if [ \"$(basename \"$1\")\" = \"{onboard}\" ] && [ \"$(basename \"$2\")\" = llmapi-onboard ]; then printf reached > {shlex.quote(str(marker))}; kill -TERM \"$PPID\"; sleep 1; exit 75; fi\n"
                "exec /bin/mv \"$@\"\n"
            )
            (wrappers / "mv").chmod(0o755)
            result = self.run_bash_bootstrap(home, origin, {"PATH": f"{wrappers}:{os.environ['PATH']}"})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(marker.read_text(), "reached")
            self.assertEqual((binary_directory / "llmapi-onboard").read_bytes(), b"old-onboard")
            self.assertEqual((binary_directory / "llmapi-mcp-bridge").read_bytes(), b"old-bridge")
            self.assertFalse(any(binary_directory.glob(".install.*")))

    def test_bash_bootstrap_rejects_destination_symlink_and_linux_acl_it_cannot_remove(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            binary_directory = home / ".llmapi" / "bin"
            binary_directory.mkdir(parents=True)
            target = pathlib.Path(temporary) / "elsewhere"
            target.write_text("not a helper")
            (binary_directory / "llmapi-onboard").symlink_to(target)
            self.bootstrap_site(root)
            origin, _ = self.serve(root)
            result = self.run_bash_bootstrap(home, origin)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue((binary_directory / "llmapi-onboard").is_symlink())

    def test_bash_bootstrap_fails_closed_when_linux_acl_cannot_be_removed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "site"
            home = pathlib.Path(temporary) / "home"
            home.mkdir()
            self.bootstrap_site(root, operating_system="linux")
            origin, paths = self.serve(root)
            wrappers = pathlib.Path(temporary) / "wrappers"
            wrappers.mkdir()
            (wrappers / "uname").write_text("#!/usr/bin/env bash\nif [ \"$1\" = -s ]; then printf Linux; else exec /usr/bin/uname \"$@\"; fi\n")
            (wrappers / "getfacl").write_text("#!/usr/bin/env bash\nprintf 'user::rwx\\nuser:attacker:rwx\\ngroup::---\\nother::---\\n'\n")
            (wrappers / "setfacl").write_text("#!/usr/bin/env bash\nexit 1\n")
            for wrapper in wrappers.iterdir():
                wrapper.chmod(0o755)
            result = self.run_bash_bootstrap(home, origin, {"PATH": f"{wrappers}:{os.environ['PATH']}"})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(paths, [])


if __name__ == "__main__":
    unittest.main()
