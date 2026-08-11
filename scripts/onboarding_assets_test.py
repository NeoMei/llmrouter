import json
import pathlib
import subprocess
import sys
import tempfile
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
        self.assertIn("llmapi-onboard", bootstrap)
        self.assertIn("llmapi-mcp-bridge", bootstrap)
        self.assertIn("sha256sum", sh)
        self.assertIn("shasum -a 256", sh)
        self.assertIn("mktemp -d", sh)
        self.assertIn("chmod 700", sh)
        self.assertIn("trap", sh)
        self.assertIn('rm -f "$checksums"', sh)
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
        self.assertNotRegex(html, r"type=[\"']password")
        self.assertNotRegex(html, r"(?:local|session)Storage")
        self.assertNotRegex(html, r"--(api-)?token")


if __name__ == "__main__":
    unittest.main()
