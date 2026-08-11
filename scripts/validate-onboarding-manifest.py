#!/usr/bin/env python3
from __future__ import annotations

import json
import sys
from pathlib import Path
from urllib.parse import urlparse

REQUIRED_CLIENTS = {
    "codex-cli",
    "codex-desktop",
    "claude-code",
    "claude-desktop",
    "opencode",
    "continue",
    "aider",
    "cursor",
    "cline",
    "roo-code",
    "openai-sdk",
    "langchain",
}

ALLOWED_INTEGRATIONS = {
    "direct-responses",
    "direct-anthropic",
    "direct-openai",
    "mcp-tool",
}

ALLOWED_DELIVERIES = {
    "automatic",
    "generated",
}

ALLOWED_PROBES = {
    "responses",
    "messages",
    "chat-completions",
    "mcp",
}

ALLOWED_PLATFORMS = {
    "linux",
    "darwin",
    "windows",
}


def fail(message: str) -> None:
    print(f"onboarding manifest: ERROR: {message}", file=sys.stderr)
    raise SystemExit(1)


def load_manifest(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        fail(f"missing manifest: {path}")
    except json.JSONDecodeError as exc:
        fail(f"invalid JSON: {exc.msg}")
    raise SystemExit(1)


def is_loopback_host(host: str | None) -> bool:
    if host is None:
        return False
    host = host.lower()
    return host in {"localhost", "127.0.0.1", "::1"} or host.startswith("127.")


def validate_optional_default_url(value: object, field_name: str) -> None:
    if not isinstance(value, str) or not value:
        return
    parsed = urlparse(value)
    if parsed.scheme == "https":
        return
    if parsed.scheme == "http" and is_loopback_host(parsed.hostname):
        return
    fail(f"{field_name} must use https or a loopback http URL")


def validate_manifest(data: dict) -> None:
    if data.get("schemaVersion") != 1:
        fail("unsupported schemaVersion")

    clients = data.get("clients")
    if not isinstance(clients, list):
        fail("clients must be an array")

    seen: dict[str, dict] = {}
    for client in clients:
        if not isinstance(client, dict):
            fail("each client must be an object")
        client_id = client.get("id")
        if not isinstance(client_id, str) or not client_id:
            fail("each client must have a string id")
        if client_id in seen:
            fail(f"duplicate client id: {client_id}")
        seen[client_id] = client

        integration = client.get("integration")
        if integration not in ALLOWED_INTEGRATIONS:
            fail(f"unsupported integration mode for {client_id}: {integration}")

        delivery = client.get("delivery")
        if delivery not in ALLOWED_DELIVERIES:
            fail(f"unsupported delivery mode for {client_id}: {delivery}")

        probe = client.get("probe")
        if probe not in ALLOWED_PROBES:
            fail(f"unsupported probe for {client_id}: {probe}")

        platforms = client.get("platforms")
        if not isinstance(platforms, list) or not platforms:
            fail(f"{client_id} must declare at least one platform")
        for platform in platforms:
            if platform not in ALLOWED_PLATFORMS:
                fail(f"unsupported platform for {client_id}: {platform}")

    missing = REQUIRED_CLIENTS - set(seen)
    if missing:
        fail(f"missing required clients: {', '.join(sorted(missing))}")

    if "claude-desktop" in seen and seen["claude-desktop"].get("integration") != "mcp-tool":
        fail("Claude Desktop must not be configured as direct access")

    artifacts = data.get("artifacts")
    if not isinstance(artifacts, dict):
        fail("artifacts must be an object")
    if artifacts.get("basePath") != "/install/bin":
        fail("artifacts.basePath must be /install/bin")
    if artifacts.get("checksumPath") != "/install/checksums.txt":
        fail("artifacts.checksumPath must be /install/checksums.txt")

    validate_optional_default_url(data.get("defaultServerUrl"), "defaultServerUrl")
    validate_optional_default_url(data.get("defaultServerURL"), "defaultServerURL")
    validate_optional_default_url(data.get("defaultOrigin"), "defaultOrigin")
    validate_optional_default_url(data.get("serverUrl"), "serverUrl")
    validate_optional_default_url(data.get("serverURL"), "serverURL")

    defaults = data.get("defaults")
    if isinstance(defaults, dict):
        validate_optional_default_url(defaults.get("serverUrl"), "defaults.serverUrl")
        validate_optional_default_url(defaults.get("serverURL"), "defaults.serverURL")
        validate_optional_default_url(defaults.get("origin"), "defaults.origin")


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: validate-onboarding-manifest.py <manifest-path>", file=sys.stderr)
        return 2
    path = Path(argv[1])
    data = load_manifest(path)
    validate_manifest(data)
    print("onboarding manifest: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
