# LLMAPI client setup guide

The installer is served from the same LLMAPI origin as its checksums and helper binaries. It downloads `llmapi-onboard` and `llmapi-mcp-bridge` into a private temporary directory, verifies the exact SHA-256 checksum entries, and atomically installs private helpers under `~/.llmapi/bin` (the Windows current-user equivalent) before it starts onboarding. A failed private installation stops the process; do not work around that failure by pointing Claude Desktop at a temporary download.

## Install

On macOS or Linux, open the install page in a browser and copy its command. It sets `LLMAPI_SERVER_URL` from the page origin:

```sh
curl -fsSL "https://stage.example/install/install.sh" | LLMAPI_SERVER_URL="https://stage.example" bash
```

On Windows PowerShell:

```powershell
$env:LLMAPI_SERVER_URL='https://stage.example'; irm "$env:LLMAPI_SERVER_URL/install/install.ps1" | iex
```

The bootstrap intentionally does not take a credential command-line argument. It asks for one through the CLI's hidden prompt, unless `LLMAPI_TOKEN` is already set in the terminal environment:

```sh
export LLMAPI_TOKEN='…'
```

Choose clients interactively, or provide a comma-separated approved list with `LLMAPI_CLIENTS`:

```sh
export LLMAPI_CLIENTS='codex-cli,claude-code,claude-desktop'
```

You can override the selected server only with the normal onboarding option, for example `llmapi-onboard install --server-url https://stage.example`. The bootstrap itself derives its server from `LLMAPI_SERVER_URL` and verifies its downloads against that same origin.

## Verify and maintain

Run a diagnostic after installation:

```sh
~/.llmapi/bin/llmapi-onboard doctor
```

Each managed change has a receipt. List them with `llmapi-onboard receipts`, restore a receipt with `llmapi-onboard restore <receipt-id>`, and remove managed changes with `llmapi-onboard uninstall <receipt-id>`. Keep the private helpers if you intend to restore later; remove `~/.llmapi` only after uninstalling all managed receipts.

## Claude Desktop and streaming

Claude Desktop is configured for local MCP tool access only. It does not replace the application’s built-in Claude model. Restart Claude Desktop after installation so it reloads the local bridge and its private configuration.

Quick Tunnel deployments can restrict or interrupt SSE streaming. Use a direct server address or a streaming-capable public domain for streaming requests; non-streaming setup and model checks remain separate from that limitation.
