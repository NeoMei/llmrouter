import json
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
INSTALL = ROOT / "web" / "public" / "install"
REQUIRED = {
    "codex-cli", "codex-desktop", "claude-code", "claude-desktop",
    "opencode", "continue", "aider", "cursor", "cline", "roo-code",
    "openai-sdk", "langchain",
}


class OnboardingAssetsTest(unittest.TestCase):
    def test_manifest_has_every_approved_client_and_explicit_mode(self):
        data = json.loads((INSTALL / "manifest.json").read_text())
        clients = {item["id"]: item for item in data["clients"]}
        self.assertEqual(set(clients), REQUIRED)
        self.assertEqual(clients["claude-desktop"]["integration"], "mcp-tool")
        self.assertEqual(clients["claude-code"]["integration"], "direct-anthropic")
        self.assertEqual(clients["codex-desktop"]["configFamily"], "codex")
        self.assertEqual(data["schemaVersion"], 1)


if __name__ == "__main__":
    unittest.main()
