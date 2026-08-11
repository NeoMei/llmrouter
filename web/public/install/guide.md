# LLMAPI client setup guide

The installer is served from the same LLMAPI origin as its checksums and helper binaries. It downloads `llmapi-onboard` and `llmapi-mcp-bridge` into a private temporary directory, verifies the exact SHA-256 checksum entries, and atomically installs private helpers under `~/.llmapi/bin` (the Windows current-user equivalent) before it starts onboarding. A failed private installation stops the process; do not work around that failure by pointing Claude Desktop at a temporary download.

## Install

On macOS or Linux, open the install page in a browser and copy its command. It sets `LLMAPI_SERVER_URL` from the page origin:

```sh
llmapi_install() { local tmp http_status status=0; if tmp="$(mktemp)"; then if http_status="$(curl -fsS --max-redirs 0 --write-out '%{http_code}' -o "$tmp" "https://stage.example/install/install.sh")"; then case "$http_status" in 2??) if LLMAPI_SERVER_URL="https://stage.example" bash "$tmp"; then :; else status=$?; fi ;; *) status=1 ;; esac; else status=$?; fi; rm -f "$tmp" || :; else status=$?; fi; return "$status"; }; llmapi_install
```

On Windows PowerShell:

```powershell
$installer = Join-Path ([IO.Path]::GetTempPath()) ('llmapi-install-' + [guid]::NewGuid() + '.ps1'); try { $response = Invoke-WebRequest -Uri 'https://stage.example/install/install.ps1' -OutFile $installer -PassThru -MaximumRedirection 0 -ErrorAction Stop; if ($response.StatusCode -lt 200 -or $response.StatusCode -ge 300) { throw 'installer download failed' }; $env:LLMAPI_SERVER_URL='https://stage.example'; & $installer } finally { Remove-Item -LiteralPath $installer -Force -ErrorAction SilentlyContinue }
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
