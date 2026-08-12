# LLMROUTER Client Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish, test, deploy, and release a secure cross-platform `/install/` onboarding system for LLMROUTER direct-API and desktop-MCP clients.

**Architecture:** A versioned static manifest drives a no-Node bootstrap page and platform launchers. The launchers download a checksummed Go onboarding executable that owns configuration discovery, transactional writes, receipts, restore/uninstall, model discovery, and doctor checks; a second constrained Go executable provides Claude Desktop's stdio MCP bridge. Existing TypeScript CLI behavior converges on the same LLMROUTER naming and safety contract, while Docker and GitHub release jobs build the web assets and all OS/architecture helper binaries from one revision.

**Tech Stack:** Go 1.25.1, Bun 1.3.14, TypeScript 5.7, Bash, PowerShell, static HTML/CSS/JavaScript, Node test runner, Playwright 1.62.1, Docker, GitHub Actions.

## Global Constraints

- Public product and artifact names are `LLMROUTER`; legacy `codex-gateway` names are migration inputs only.
- `/install/` defaults to its own `window.location.origin`; `--server-url` and `LLMROUTER_SERVER_URL` are explicit overrides.
- API tokens are accepted through a hidden prompt or `LLMROUTER_TOKEN`, never through a normal command-line option.
- Basic installation requires no Node.js runtime.
- Automatic writes are limited to documented user-owned configuration files and must be atomic, reversible, idempotent, secret-redacted, and symlink-safe.
- Codex CLI and Codex Desktop share Codex configuration; desktop credential visibility is checked separately.
- Claude Desktop is MCP tool access and must never be described as replacement of its built-in conversation model.
- Quick Tunnel is not accepted as SSE release evidence.
- Deployment remains parallel on `113.249.120.24`; the existing NewAPI container, database, Nginx upstream, and `llmrouter.quukk.com` traffic are not changed.
- Preserve the untracked `.codebase-memory/` directory and do not accidentally add `scripts/prepare-sidecar-env.py`.

---

## File Structure

### Manifest and public assets

- `web/public/install/manifest.json` — canonical client, protocol, artifact, fallback-model, and schema-version data.
- `web/public/install/index.html` — static accessible onboarding UI; contains no token field.
- `web/public/install/install.sh` — POSIX bootstrapper that downloads and verifies `llmrouter-onboard`.
- `web/public/install/install.ps1` — Windows bootstrapper with the same contract.
- `web/public/install/guide.md` — JavaScript-free usage, security, restore, and limitations guide.
- `web/public/install/bin/` — generated release helper binaries; ignored in source except `.gitkeep`.
- `web/public/install/checksums.txt` — generated checksums for helper binaries.
- `onboarding/manifest.schema.json` — reviewable JSON Schema for the canonical manifest.

### Go onboarding executable

- `cmd/llmrouter-onboard/main.go` — process entry point and exit-code mapping only.
- `internal/onboarding/options.go` — environment/flag/prompt precedence and client allow-list parsing.
- `internal/onboarding/url.go` — HTTPS/loopback URL normalization and endpoint construction.
- `internal/onboarding/manifest.go` — manifest decoding and schema-version compatibility checks.
- `internal/onboarding/api.go` — bounded health, model, Responses, Chat, and Messages probes.
- `internal/onboarding/securefs.go` — safe path resolution, ownership checks, atomic writes, modes/ACL boundary.
- `internal/onboarding/receipt.go` — secret-free transactions, backups, restore, uninstall, and conflict detection.
- `internal/onboarding/managedtext.go` — idempotent named text blocks for shell/TOML/YAML-style files.
- `internal/onboarding/jsonmerge.go` — lossless-at-field-level JSON object merge for Claude Desktop/OpenCode/Continue.
- `internal/onboarding/clients.go` — adapter registry and common interface.
- `internal/onboarding/client_codex.go` — Codex CLI/Desktop provider configuration.
- `internal/onboarding/client_claude_code.go` — Claude Code gateway environment configuration.
- `internal/onboarding/client_claude_desktop.go` — Claude Desktop MCP merge and private bridge credentials.
- `internal/onboarding/client_opencode.go` — OpenCode provider configuration.
- `internal/onboarding/client_continue.go` — version-gated Continue configuration.
- `internal/onboarding/client_aider.go` — Aider managed environment/configuration.
- `internal/onboarding/manual.go` — Cursor/Cline/Roo/SDK/LangChain generated settings.
- `internal/onboarding/doctor.go` — read-only checks and opt-in low-token smoke tests.

### MCP bridge

- `cmd/llmrouter-mcp-bridge/main.go` — stdio wiring and clean process errors.
- `internal/onboarding/mcpbridge/config.go` — private credential/model policy loading.
- `internal/onboarding/mcpbridge/protocol.go` — JSON-RPC framing and MCP initialize/list-tools/call-tool handlers.
- `internal/onboarding/mcpbridge/client.go` — bounded LLMROUTER model-list and Chat requests.

### Tests and release integration

- `internal/onboarding/*_test.go` and `internal/onboarding/mcpbridge/*_test.go` — unit/fixture/security coverage.
- `onboarding/testdata/` — disposable client configurations and mock HTTP fixtures.
- `scripts/build-onboarding-artifacts.sh` — reproducible cross-build/checksum/static-asset validation.
- `scripts/validate-onboarding-manifest.py` — dependency-free semantic checks beyond JSON Schema.
- `scripts/onboarding_assets_test.py` — manifest, guide, command, and secret-leak regression tests.
- `scripts/install_ps1.Tests.ps1` — Pester coverage for Windows URL, checksum, cleanup, and token-argument behavior.
- `scripts/onboarding_e2e.sh` — disposable-HOME install/doctor/restore/uninstall integration test.
- `web/e2e/install.spec.ts` — desktop/mobile onboarding-page interaction tests.
- `cli/src/*` — rename public CLI surface to LLMROUTER and expose restore/uninstall compatibility commands.
- `Dockerfile`, `scripts/verify-cli.sh`, `scripts/verify-web.sh`, `.github/workflows/ci.yml`, `.github/workflows/release.yml` — mandatory build and release gates.

---

### Task 1: Lock the Manifest and Asset Contract

**Files:**
- Create: `onboarding/manifest.schema.json`
- Create: `web/public/install/manifest.json`
- Create: `scripts/validate-onboarding-manifest.py`
- Create: `scripts/onboarding_assets_test.py`
- Modify: `web/.gitignore`

**Interfaces:**
- Produces: manifest schema version `1` and client IDs `codex-cli`, `codex-desktop`, `claude-code`, `claude-desktop`, `opencode`, `continue`, `aider`, `cursor`, `cline`, `roo-code`, `openai-sdk`, `langchain`.
- Produces: artifact names `llmrouter-onboard-{os}-{arch}[.exe]` and `llmrouter-mcp-bridge-{os}-{arch}[.exe]` consumed by Tasks 4, 5, and 7.

- [ ] **Step 1: Write the failing semantic manifest test**

```python
# scripts/onboarding_assets_test.py
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
```

- [ ] **Step 2: Run the test and verify the missing manifest fails**

Run: `python3 -m unittest scripts/onboarding_assets_test.py -v`

Expected: `ERROR` containing `web/public/install/manifest.json`.

- [ ] **Step 3: Add schema, manifest, validator, and generated-binary ignore rule**

The manifest's client records must use this exact shape:

```json
{
  "schemaVersion": 1,
  "installerMinVersion": "0.1.0",
  "fallbackModels": ["DeepSeek-V4-Flash-A", "glm-5.2"],
  "clients": [
    {
      "id": "codex-cli",
      "displayName": "Codex CLI",
      "platforms": ["linux", "darwin", "windows"],
      "integration": "direct-responses",
      "delivery": "automatic",
      "configFamily": "codex",
      "probe": "responses"
    },
    {
      "id": "claude-desktop",
      "displayName": "Claude Desktop",
      "platforms": ["darwin", "windows"],
      "integration": "mcp-tool",
      "delivery": "automatic",
      "configFamily": "claude-desktop",
      "probe": "mcp"
    }
  ],
  "artifacts": {
    "basePath": "/install/bin",
    "checksumPath": "/install/checksums.txt"
  }
}
```

Add all twelve records, with automatic/direct modes matching the design. `validate-onboarding-manifest.py` must reject duplicate IDs, unsupported modes, missing required clients, non-HTTPS non-loopback defaults, an unknown schema version, and Claude Desktop configured as direct access. Add `web/public/install/bin/*` and `!web/public/install/bin/.gitkeep` to `web/.gitignore`.

- [ ] **Step 4: Run semantic and formatting checks**

Run:

```bash
python3 scripts/validate-onboarding-manifest.py web/public/install/manifest.json
python3 -m unittest scripts/onboarding_assets_test.py -v
python3 -m json.tool web/public/install/manifest.json >/dev/null
python3 -m json.tool onboarding/manifest.schema.json >/dev/null
```

Expected: validator prints `onboarding manifest: PASS`; unit tests pass.

- [ ] **Step 5: Commit**

```bash
git add onboarding/manifest.schema.json web/public/install/manifest.json web/public/install/bin/.gitkeep web/.gitignore scripts/validate-onboarding-manifest.py scripts/onboarding_assets_test.py
git commit -m "feat(onboarding): define client manifest contract"
```

### Task 2: Build the Secure Transaction and Option Core

**Files:**
- Create: `internal/onboarding/options.go`
- Create: `internal/onboarding/options_test.go`
- Create: `internal/onboarding/url.go`
- Create: `internal/onboarding/url_test.go`
- Create: `internal/onboarding/securefs.go`
- Create: `internal/onboarding/securefs_test.go`
- Create: `internal/onboarding/receipt.go`
- Create: `internal/onboarding/receipt_test.go`
- Create: `internal/onboarding/managedtext.go`
- Create: `internal/onboarding/managedtext_test.go`
- Create: `internal/onboarding/jsonmerge.go`
- Create: `internal/onboarding/jsonmerge_test.go`
- Create: `cmd/llmrouter-onboard/main.go`

**Interfaces:**
- Produces: `NormalizeServerURL(string) (*url.URL, error)`, `ResolveOptions(Inputs) (Options, error)`, `BeginTransaction(home string) (*Transaction, error)`, `(*Transaction).Write(path string, data []byte) error`, `(*Transaction).Commit(meta ReceiptMeta) (Receipt, error)`, `Restore(home, receiptID string) error`, and `Uninstall(home, receiptID string) error`.
- Produces: executable commands `install`, `doctor`, `restore`, `uninstall`, and `receipts` consumed by client adapters and launchers.

- [ ] **Step 1: Write failing table tests for input precedence and URL policy**

```go
func TestNormalizeServerURL(t *testing.T) {
	tests := []struct{ in, want string; ok bool }{
		{"https://stage.example/v1/", "https://stage.example", true},
		{"http://127.0.0.1:13100", "http://127.0.0.1:13100", true},
		{"http://stage.example", "", false},
		{"https://user:pass@stage.example", "", false},
		{"https://stage.example?token=secret", "", false},
	}
	for _, tc := range tests {
		u, err := NormalizeServerURL(tc.in)
		if tc.ok { require.NoError(t, err); require.Equal(t, tc.want, u.String()) } else { require.Error(t, err) }
	}
}

func TestResolveOptionsDoesNotAcceptTokenFlag(t *testing.T) {
	_, err := ResolveOptions(Inputs{Args: []string{"--token", "leak"}})
	require.ErrorContains(t, err, "LLMROUTER_TOKEN or hidden prompt")
}
```

- [ ] **Step 2: Run the focused tests and verify they fail to compile**

Run: `go test ./internal/onboarding -run 'TestNormalizeServerURL|TestResolveOptions' -count=1`

Expected: FAIL because package symbols do not exist.

- [ ] **Step 3: Implement the public types and strict precedence**

```go
type Inputs struct {
	Args       []string
	Env        map[string]string
	PageOrigin string
	Prompt     Prompter
}

type Options struct {
	ServerURL *url.URL
	Token     Secret
	Clients   []ClientID
	Model     string
	Smoke     bool
}

type ClientID string
const (
	ClientCodexCLI      ClientID = "codex-cli"
	ClientCodexDesktop  ClientID = "codex-desktop"
	ClientClaudeCode    ClientID = "claude-code"
	ClientClaudeDesktop ClientID = "claude-desktop"
	ClientOpenCode      ClientID = "opencode"
	ClientContinue      ClientID = "continue"
	ClientAider         ClientID = "aider"
)

type Secret struct{ value string }
func NewSecret(value string) Secret { return Secret{value: value} }
func (Secret) String() string { return "[REDACTED]" }
func (s Secret) Reveal() string { return s.value }

type Prompter interface {
	ReadHidden(label string) (string, error)
	SelectMany(label string, allowed []ClientID) ([]ClientID, error)
}
```

Reject `--token`, unknown clients, missing non-interactive inputs, control characters, and unsupported URL components. Make `Secret.String()` return `[REDACTED]`; expose token bytes only through `Secret.Reveal()` at the HTTP/config boundary.

- [ ] **Step 4: Write failing transaction tests**

```go
func TestTransactionRollbackAndConflict(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(target, []byte("original\n"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(target, []byte("configured\n")))
	receipt, err := tx.Commit(ReceiptMeta{Clients: []ClientID{ClientClaudeCode}})
	require.NoError(t, err)
	require.NotContains(t, string(mustRead(t, receipt.Path)), "secret")
	require.NoError(t, os.WriteFile(target, []byte("manual change\n"), 0o600))
	require.ErrorContains(t, Restore(home, receipt.ID), "changed since configuration")
}

func TestTransactionRejectsDestinationSymlink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real")
	link := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(real, nil, 0o600))
	require.NoError(t, os.Symlink(real, link))
	tx, _ := BeginTransaction(home)
	require.ErrorContains(t, tx.Write(link, []byte("x")), "symlink")
}
```

- [ ] **Step 5: Implement secure writes, receipts, restore, and managed merges**

Use state roots `~/.llmrouter/receipts`, `~/.llmrouter/backups`, and `~/.llmrouter/credentials`. Each `ReceiptFile` must contain `Path`, `OriginalHash`, `ConfiguredHash`, `BackupPath`, and `Existed`; `Receipt` must not contain a token or environment-value map. Atomic write uses `CreateTemp` in the destination directory, `Chmod(0600)`, `Sync`, `Close`, `Rename`, then directory `Sync` on POSIX. Check every existing path component with `Lstat`; reject symlinks and files not owned by the current UID. Keep Windows ACL application behind a `secureFilePlatform(path string) error` build-tag implementation.

`Receipt` contains `ID`, `Path`, `Home`, `CreatedAt`, `Meta`, and `Files`; it contains neither `Secret` nor serialized credential/environment values. `ReceiptFile` contains `Path`, `OriginalHash`, `ConfiguredHash`, `BackupPath`, and `Existed`.

Managed text markers are exactly `# LLMROUTER:BEGIN <client>` and `# LLMROUTER:END <client>`. JSON merge accepts a path of object keys and preserves sibling fields:

```go
func MergeJSONObject(input []byte, keys []string, value any) ([]byte, error)
func ApplyManagedBlock(input, client, content string) (string, error)
func RemoveManagedBlock(input, client string) (string, error)
```

- [ ] **Step 6: Add the command entry point and exit codes**

`cmd/llmrouter-onboard/main.go` maps usage errors to `2`, unsafe filesystem/config conflicts to `3`, network/auth failures to `4`, and unexpected failures to `1`. It must print only redacted errors and support `--home` solely for tests/administration.

- [ ] **Step 7: Run core tests including race detection**

Run:

```bash
go test ./internal/onboarding -count=1
go test -race ./internal/onboarding -count=1
go build ./cmd/llmrouter-onboard
```

Expected: all tests pass; build succeeds without creating tracked output.

- [ ] **Step 8: Commit**

```bash
git add cmd/llmrouter-onboard internal/onboarding
git commit -m "feat(onboarding): add secure configuration transactions"
```

### Task 3: Implement Direct-Access Client Adapters and Doctor

**Files:**
- Create: `internal/onboarding/manifest.go`
- Create: `internal/onboarding/api.go`
- Create: `internal/onboarding/api_test.go`
- Create: `internal/onboarding/clients.go`
- Create: `internal/onboarding/clients_test.go`
- Create: `internal/onboarding/client_codex.go`
- Create: `internal/onboarding/client_claude_code.go`
- Create: `internal/onboarding/client_opencode.go`
- Create: `internal/onboarding/client_continue.go`
- Create: `internal/onboarding/client_aider.go`
- Create: `internal/onboarding/manual.go`
- Create: `internal/onboarding/doctor.go`
- Create: `internal/onboarding/doctor_test.go`
- Create: `onboarding/testdata/*`

**Interfaces:**
- Consumes: `Options`, `Transaction`, `ApplyManagedBlock`, and `MergeJSONObject` from Task 2.
- Produces: `ClientAdapter` registry and `RunDoctor(context.Context, DoctorOptions) DoctorReport` used by the CLI and certification scripts.

- [ ] **Step 1: Write the adapter contract and failing fixture tests**

```go
type ClientAdapter interface {
	ID() ClientID
	Discover(Platform, string) Discovery
	Plan(context.Context, ConfigureRequest) (ConfigPlan, error)
	Check(context.Context, CheckRequest) []Check
}

type Platform struct { OS, Arch string }
type Discovery struct { Installed bool; Paths []string; Automatic bool; Reason string }
type ConfigureRequest struct { Home string; Platform Platform; ServerURL *url.URL; Token Secret; Model string; BridgePath string }
type PlannedWrite struct { Path string; Data []byte; Mode fs.FileMode }
type ConfigPlan struct { Client ClientID; Writes []PlannedWrite; Instructions []string; RestartRequired bool }
type CheckRequest struct { Home string; Platform Platform; ServerURL *url.URL; Token Secret; Model string }

func TestAdaptersPreserveUnrelatedConfiguration(t *testing.T) {
	tests := []struct{ client ClientID; fixture, preserved string }{
		{ClientCodexCLI, "codex-existing.toml", `[model_providers.native]`},
		{ClientOpenCode, "opencode-existing.json", `"unrelated"`},
		{ClientContinue, "continue-existing.json", `"tabAutocompleteModel"`},
		{ClientAider, "aider-existing.yml", `dark-mode: true`},
	}
	for _, tc := range tests {
		t.Run(string(tc.client), func(t *testing.T) {
			result := configureFixture(t, tc.client, tc.fixture)
			require.Contains(t, result, tc.preserved)
			require.Equal(t, 1, strings.Count(result, "LLMROUTER:BEGIN"))
		})
	}
}
```

- [ ] **Step 2: Run focused tests and verify missing adapters fail**

Run: `go test ./internal/onboarding -run 'TestAdapters|TestDoctor|TestAPI' -count=1`

Expected: FAIL because adapter registry and API probes do not exist.

- [ ] **Step 3: Implement exact client configuration behavior**

Use these managed values:

```text
Codex: provider id llmrouter, base_url <origin>/v1, wire_api responses, env_key LLMROUTER_TOKEN
Claude Code: ANTHROPIC_BASE_URL=<origin>, ANTHROPIC_AUTH_TOKEN from private managed environment, selected model aliases only when explicitly chosen
OpenCode: provider id llmrouter, npm @ai-sdk/openai-compatible, baseURL <origin>/v1, apiKey {env:LLMROUTER_TOKEN}
Continue: provider openai, apiBase <origin>/v1, apiKey ${LLMROUTER_TOKEN}, selected public model
Aider: OPENAI_API_BASE=<origin>/v1 and OPENAI_API_KEY via an LLMROUTER managed environment block
```

Codex CLI and Desktop return the same planned config file but separate checks. Continue automatic mutation must require a recognized JSON/YAML strategy; unknown syntax returns generated manual settings without changing files. `manual.go` returns secret-free snippets for Cursor, Cline, Roo Code, OpenAI Python/Node SDK, and LangChain.

- [ ] **Step 4: Add mock HTTP protocol tests**

```go
func TestAPIProbesUseCorrectEndpointsAndBoundedBodies(t *testing.T) {
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.URL.Path
		require.LessOrEqual(t, r.ContentLength, int64(4096))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" { io.WriteString(w, `{"data":[{"id":"model-a"}]}`); return }
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	client := NewAPIClient(mustLoopbackURL(t, srv.URL), NewSecret("token"), srv.Client())
	require.NoError(t, client.ProbeResponses(context.Background(), "model-a"))
	require.NoError(t, client.ProbeMessages(context.Background(), "model-a"))
	require.Equal(t, "POST /v1/responses", <-seen)
	require.Equal(t, "POST /v1/messages", <-seen)
}
```

Define `mustLoopbackURL(t, raw)` in `api_test.go` as a wrapper around `NormalizeServerURL`, and define `configureFixture`/`mustRead` in the corresponding `_test.go` files so all snippets compile without production-only test hooks.

Every request uses a 10-second default timeout, `max_output_tokens`/`max_tokens <= 16`, no streaming in the default doctor, and a user agent containing the onboarding version. Error text includes status/request ID but not response secrets or token values.

- [ ] **Step 5: Implement doctor reports and opt-in smoke behavior**

```go
type Check struct { Name, Status, Message string }
type DoctorReport struct { Checks []Check; Passed, Failed, Skipped int }
type DoctorOptions struct {
	Home string; ServerURL *url.URL; Token Secret; Clients []ClientID; Model string; Smoke bool
}
```

Without `Smoke`, health/auth/model/config/permission checks run and inference checks are `SKIP`. With `Smoke`, execute only the protocol probes required by selected clients. Never put prompt text or credentials in `DoctorReport`.

- [ ] **Step 6: Run adapter, doctor, CLI, and race tests**

Run:

```bash
go test ./internal/onboarding -count=1
go test -race ./internal/onboarding -count=1
go run ./cmd/llmrouter-onboard --help
```

Expected: tests pass; help lists `install`, `doctor`, `restore`, `uninstall`, and `receipts`, and does not list `--token`.

- [ ] **Step 7: Commit**

```bash
git add internal/onboarding onboarding/testdata cmd/llmrouter-onboard
git commit -m "feat(onboarding): configure direct API clients"
```

### Task 4: Add Claude Desktop MCP Bridge and Adapter

**Files:**
- Create: `internal/onboarding/mcpbridge/config.go`
- Create: `internal/onboarding/mcpbridge/config_test.go`
- Create: `internal/onboarding/mcpbridge/protocol.go`
- Create: `internal/onboarding/mcpbridge/protocol_test.go`
- Create: `internal/onboarding/mcpbridge/client.go`
- Create: `internal/onboarding/mcpbridge/client_test.go`
- Create: `cmd/llmrouter-mcp-bridge/main.go`
- Create: `internal/onboarding/client_claude_desktop.go`
- Create: `internal/onboarding/client_claude_desktop_test.go`

**Interfaces:**
- Consumes: normalized URL, private credential file, transaction API, and selected model allow-list.
- Produces: MCP protocol version `2025-06-18`, tools `llmapi_list_models` and `llmapi_chat`, and executable `llmrouter-mcp-bridge` consumed by Claude Desktop configuration.

- [ ] **Step 1: Write failing MCP protocol tests**

```go
func TestServerInitializesAndListsOnlyBoundedTools(t *testing.T) {
	server := NewServer(fakeClient{models: []string{"model-a"}})
	init := call(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	require.Equal(t, "2025-06-18", init.Result.(map[string]any)["protocolVersion"])
	listed := call(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	require.Equal(t, []string{"llmapi_list_models", "llmapi_chat"}, toolNames(listed))
}

func TestChatRejectsUnknownModelAndOversizedPrompt(t *testing.T) {
	server := NewServer(fakeClient{models: []string{"model-a"}})
	requireRPCError(t, server, chatCall("model-b", "hello"), -32602)
	requireRPCError(t, server, chatCall("model-a", strings.Repeat("x", 32769)), -32602)
}
```

In `protocol_test.go`, define `fakeClient` against `type Client interface { ListModels(context.Context) ([]string, error); Chat(context.Context, ChatRequest) (string, error) }`; `call`, `chatCall`, `toolNames`, and `requireRPCError` encode one JSON-RPC line, pass it to `Server.Handle`, and decode/assert the response. Production exports `NewServer(client Client) *Server` and `(*Server).Handle(context.Context, []byte) []byte`.

- [ ] **Step 2: Run MCP tests and verify they fail to compile**

Run: `go test ./internal/onboarding/mcpbridge -count=1`

Expected: FAIL because the package is not implemented.

- [ ] **Step 3: Implement newline-delimited stdio JSON-RPC and bounded tools**

Accept `initialize`, `notifications/initialized`, `ping`, `tools/list`, and `tools/call`. Reject all other methods with `-32601`. Limit each input line to 64 KiB, prompt to 32 KiB, output text to 64 KiB, one in-flight request, and HTTP duration to 30 seconds. `llmapi_chat` accepts only `{model, prompt, max_output_tokens}` with a configured model and clamps output tokens to `1..256`. It calls `/v1/chat/completions` non-streaming. Do not implement arbitrary URLs, headers, files, shell, or generic proxying.

- [ ] **Step 4: Write and implement Claude Desktop merge tests**

```go
func TestClaudeDesktopMergePreservesExistingServersAndHidesToken(t *testing.T) {
	home := t.TempDir()
	writeClaudeDesktopFixture(t, home, `{"mcpServers":{"filesystem":{"command":"fs"}}}`)
	receipt := configureClaudeDesktop(t, home, "https://stage.example", "super-secret")
	config := readClaudeDesktopConfig(t, home)
	require.Contains(t, config, `"filesystem"`)
	require.Contains(t, config, `"llmrouter"`)
	require.NotContains(t, config, "super-secret")
	require.NotContains(t, string(mustRead(t, receipt.Path)), "super-secret")
}
```

Use macOS path `~/Library/Application Support/Claude/claude_desktop_config.json` and Windows path `%APPDATA%/Claude/claude_desktop_config.json`. The merged entry executes the installed bridge with `--config <private ~/.llmrouter/credentials/claude-desktop.json>`. The credential file contains origin, token, model allow-list, and timeout; mode/ACL must be private. Linux is reported unsupported for official Claude Desktop rather than creating a speculative path.

- [ ] **Step 5: Run bridge, adapter, redaction, and build tests**

Run:

```bash
go test ./internal/onboarding/mcpbridge ./internal/onboarding -run 'MCP|ClaudeDesktop|Redact' -count=1
go test -race ./internal/onboarding/mcpbridge -count=1
go build ./cmd/llmrouter-mcp-bridge
```

Expected: all pass; binary builds.

- [ ] **Step 6: Commit**

```bash
git add cmd/llmrouter-mcp-bridge internal/onboarding/mcpbridge internal/onboarding/client_claude_desktop.go internal/onboarding/client_claude_desktop_test.go
git commit -m "feat(onboarding): support Claude Desktop through MCP"
```

### Task 5: Publish Secure Bootstrap Scripts, Guide, and Install Page

**Files:**
- Create: `web/public/install/install.sh`
- Create: `web/public/install/install.ps1`
- Create: `web/public/install/index.html`
- Create: `web/public/install/guide.md`
- Modify: `scripts/onboarding_assets_test.py`
- Create: `scripts/install_ps1.Tests.ps1`

**Interfaces:**
- Consumes: manifest/artifact names and onboarding CLI from Tasks 1–4.
- Produces: public same-origin bootstrap commands and static UI consumed by Playwright and deployment checks.

- [ ] **Step 1: Add failing bootstrap and secret-leak tests**

```python
def test_bootstraps_use_origin_override_and_never_token_argument(self):
    sh = (INSTALL / "install.sh").read_text()
    ps = (INSTALL / "install.ps1").read_text()
    self.assertIn("LLMROUTER_SERVER_URL", sh)
    self.assertIn("LLMROUTER_SERVER_URL", ps)
    self.assertNotRegex(sh + ps, r"--(api-)?token")

def test_page_has_all_clients_and_explicit_claude_desktop_limit(self):
    html = (INSTALL / "index.html").read_text()
    self.assertIn("window.location.origin", html)
    self.assertIn("MCP tool access", html)
    self.assertIn("does not replace Claude Desktop", html)
    self.assertNotRegex(html, r"type=[\"']password")
```

- [ ] **Step 2: Run asset tests and verify missing assets fail**

Run: `python3 -m unittest scripts/onboarding_assets_test.py -v`

Expected: FAIL for missing `install.sh`, `install.ps1`, and `index.html`.

- [ ] **Step 3: Implement the Bash bootstrap**

The script must use `set -eu`, normalize `uname -s/-m` to manifest artifact names, create a `0700` temporary directory, download `checksums.txt` and the binary from `${LLMROUTER_SERVER_URL%/}/install`, validate SHA-256 using `sha256sum` or `shasum -a 256`, and `exec` the binary. It must reject HTTP except loopback and clean the directory with `trap`. The generated page command is exactly:

```bash
curl -fsSL "${ORIGIN}/install/install.sh" | LLMROUTER_SERVER_URL="${ORIGIN}" bash
```

- [ ] **Step 4: Implement the PowerShell bootstrap**

Use `$ErrorActionPreference = 'Stop'`, `[System.Uri]`, `New-TemporaryFile`/private temporary directory, `Invoke-WebRequest`, `Get-FileHash -Algorithm SHA256`, an exact checksum comparison, and `try/finally` cleanup. Pass server origin through `$env:LLMROUTER_SERVER_URL`; do not accept or print a token parameter. The page command is:

```powershell
$env:LLMROUTER_SERVER_URL='https://stage.example'; irm "$env:LLMROUTER_SERVER_URL/install/install.ps1" | iex
```

- [ ] **Step 5: Implement the static page and guide**

The page reads `manifest.json`, detects OS, renders all client cards, labels direct versus MCP, fetches same-origin `/v1/models` without a token and visibly falls back when authentication is required/unavailable, and generates OS-specific commands. Copy buttons use `navigator.clipboard.writeText` and an `aria-live` status element. Never request, store, render, or append a token. `guide.md` documents hidden prompt, `LLMROUTER_TOKEN`, `LLMROUTER_CLIENTS`, `--server-url`, doctor, receipts, restore, uninstall, Quick Tunnel SSE limits, and desktop restart behavior.

- [ ] **Step 6: Add PowerShell parser and Pester tests**

```powershell
# scripts/install_ps1.Tests.ps1
Describe 'LLMROUTER PowerShell bootstrap' {
  BeforeAll { $script = Get-Content "$PSScriptRoot/../web/public/install/install.ps1" -Raw }
  It 'has no token parameter' { $script | Should -Not -Match '--(api-)?token' }
  It 'requires SHA-256 verification' { $script | Should -Match 'Get-FileHash.+SHA256' }
  It 'uses the explicit server environment' { $script | Should -Match 'LLMROUTER_SERVER_URL' }
  It 'always cleans temporary files' { $script | Should -Match 'finally' }
  It 'parses as valid PowerShell' {
    $tokens = $null
    $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile(
      "$PSScriptRoot/../web/public/install/install.ps1", [ref]$tokens, [ref]$errors
    )
    $errors | Should -BeNullOrEmpty
  }
}
```

On Windows run `Invoke-Pester scripts/install_ps1.Tests.ps1 -CI`; on non-Windows development machines run it when `pwsh` is installed. CI installation of Pester is pinned to the repository-approved major version and the test is mandatory.

- [ ] **Step 7: Run static, shell, PowerShell, and build checks**

Run:

```bash
python3 -m unittest scripts/onboarding_assets_test.py -v
bash -n web/public/install/install.sh
command -v shellcheck >/dev/null && shellcheck web/public/install/install.sh || true
command -v pwsh >/dev/null && pwsh -NoProfile -Command 'Invoke-Pester scripts/install_ps1.Tests.ps1 -CI' || true
cd web && bun run build
test -f dist/install/index.html
test -f dist/install/manifest.json
```

Expected: tests pass, Bash parses, web build contains install assets.

- [ ] **Step 8: Commit**

```bash
git add web/public/install scripts/onboarding_assets_test.py scripts/install_ps1.Tests.ps1
git commit -m "feat(web): publish LLMROUTER client installer"
```

### Task 6: Make the Existing TypeScript CLI Convergent and Reversible

**Files:**
- Modify: `cli/package.json`
- Modify: `cli/src/index.ts`
- Modify: `cli/src/client-state.ts`
- Modify: `cli/src/client-state.test.ts`
- Modify: `cli/src/init.ts`
- Modify: `cli/src/status.ts`
- Modify: `cli/src/doctor.ts`
- Create: `cli/src/restore.ts`
- Create: `cli/src/uninstall.ts`
- Create: `cli/src/branding.test.ts`

**Interfaces:**
- Consumes: LLMROUTER markers, state-root naming, URL policy, and receipt secrecy contract.
- Produces: `llmrouter init|status|doctor|restore|uninstall`, with a temporary `codex-gateway` bin alias only for migration.

- [ ] **Step 1: Write failing branding, receipt, and command tests**

```ts
test('public CLI is LLMROUTER branded and exposes reversible commands', () => {
  assert.equal(program.name(), 'llmrouter')
  assert.deepEqual(
    program.commands.map((command) => command.name()).sort(),
    ['doctor', 'init', 'restore', 'status', 'uninstall']
  )
})

test('receipt never serializes environment values', async () => {
  const home = await makeHome()
  await configureClaude({ home, serverUrl: 'https://gateway.test', apiToken: 'super-secret' })
  const receiptDir = path.join(home, '.llmrouter', 'receipts')
  const [name] = await fs.readdir(receiptDir)
  const receipt = await fs.readFile(path.join(receiptDir, name), 'utf8')
  assert.doesNotMatch(receipt, /super-secret|ANTHROPIC_API_KEY.*:/)
})
```

- [ ] **Step 2: Run CLI tests and verify failures**

Run: `cd cli && npm test`

Expected: FAIL because the program is named `codex-gateway`, commands are missing, and receipts contain `envVars` values.

- [ ] **Step 3: Rename public state and preserve one-way migration discovery**

Use package/bin `llmrouter`, description `Configure supported clients to use LLMROUTER`, marker `# LLMROUTER:BEGIN`, environment key `LLMROUTER_TOKEN`, provider ID/name `llmrouter`/`LLMROUTER`, and state root `.llmrouter`. On first run, detect read-only legacy receipts/backups under `.codex-gateway` but never rewrite/delete them automatically. Remove token values from `ClientReceipt`; store only environment variable names.

- [ ] **Step 4: Expose restore and uninstall commands**

`restore [receipt]` defaults to the latest receipt and uses existing hash conflict protection. `uninstall [client]` removes only the matching managed LLMROUTER block/provider when its current hash matches and then writes a new reversible receipt. Both commands return nonzero on conflicts and redact paths only when they contain a detected token-like value.

- [ ] **Step 5: Run CLI verification**

Run:

```bash
bash scripts/verify-cli.sh
cd cli && node dist/index.js --help
```

Expected: all tests/build pass; help is LLMROUTER-branded and lists reversible commands.

- [ ] **Step 6: Commit**

```bash
git add cli
git commit -m "feat(cli): converge onboarding on LLMROUTER"
```

### Task 7: Build Artifacts Reproducibly in Docker and CI

**Files:**
- Create: `scripts/build-onboarding-artifacts.sh`
- Create: `scripts/onboarding_e2e.sh`
- Modify: `scripts/verify-backend.sh`
- Modify: `scripts/verify-web.sh`
- Modify: `Dockerfile`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: helper commands and static assets from Tasks 1–6.
- Produces: six onboarding binaries per command (`linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`, `windows-amd64.exe`, `windows-arm64.exe`), deterministic `checksums.txt`, container-served assets, and release uploads.

- [ ] **Step 1: Write the failing build/e2e script assertions**

`scripts/build-onboarding-artifacts.sh --check` must fail unless every expected binary exists and its checksum entry matches. `scripts/onboarding_e2e.sh` starts a local mock server, uses a temporary HOME, runs install twice, doctor without smoke, restore, and uninstall, then asserts no `secret-test-token` appears beneath the receipt directory or captured logs.

Run: `bash scripts/build-onboarding-artifacts.sh --check`

Expected: FAIL listing all missing binaries.

- [ ] **Step 2: Implement reproducible cross-build and checksums**

Use `CGO_ENABLED=0`, `-trimpath`, and `-ldflags "-s -w -buildid="`. Build both commands for the platform matrix into `web/public/install/bin/`, sort filenames bytewise, and generate checksums with a Go/Python helper so Linux/macOS output format is identical. Running the script twice at the same revision must produce identical checksums.

- [ ] **Step 3: Restructure Docker stages without changing the runtime service**

Add a Go `onboarding-artifacts` stage before the web stage, run the build script, then `COPY --from=onboarding-artifacts /build/web/public/install/bin ...` and `checksums.txt` into the web builder before `bun run build`. Continue embedding `web/dist` into the backend and keep the runtime entry point unchanged. Add a container smoke assertion for `/install/index.html`, `/install/manifest.json`, and one helper binary.

- [ ] **Step 4: Add mandatory CI and release gates**

CI runs manifest tests, Go onboarding tests with race detection, Bash syntax, PowerShell parser on Windows, CLI verification, web verification, and `onboarding_e2e.sh`. Release jobs upload helper binaries/checksums and include their aggregate checksum in platform evidence JSON. Release publication must fail when the static manifest references a missing artifact.

- [ ] **Step 5: Run local build and container verification**

Run:

```bash
bash scripts/build-onboarding-artifacts.sh
bash scripts/build-onboarding-artifacts.sh --check
bash scripts/onboarding_e2e.sh
docker build -t llmrouter:onboarding-test .
cid=$(docker run -d -p 127.0.0.1:33100:3000 llmrouter:onboarding-test)
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true' EXIT
for i in $(seq 1 60); do curl -fsS http://127.0.0.1:33100/healthz && break; sleep 1; done
curl -fsS http://127.0.0.1:33100/install/manifest.json | python3 -m json.tool >/dev/null
curl -fsSI http://127.0.0.1:33100/install/bin/llmrouter-onboard-linux-amd64
```

Expected: build/check/e2e pass; container endpoints return `200`.

- [ ] **Step 6: Commit**

```bash
git add scripts/build-onboarding-artifacts.sh scripts/onboarding_e2e.sh scripts/verify-backend.sh scripts/verify-web.sh Dockerfile .github/workflows/ci.yml .github/workflows/release.yml web/public/install/checksums.txt
git commit -m "build(release): package onboarding artifacts"
```

### Task 8: Add Browser Acceptance and Run Full Repository Review

**Files:**
- Create: `web/e2e/install.spec.ts`
- Modify: `docs/README.md` if present, otherwise `README.md` and `README.zh_CN.md`
- Modify: files identified by review/test failures

**Interfaces:**
- Consumes: complete container/web/onboarding functionality.
- Produces: desktop/mobile screenshots, clean repository gates, and release-ready documentation.

- [ ] **Step 1: Write Playwright acceptance tests**

```ts
test('install page generates same-origin commands without secrets', async ({ page }, testInfo) => {
  await page.route('**/v1/models', route => route.fulfill({ json: { data: [{ id: 'model-a' }] } }))
  await page.goto('/install/')
  await expect(page.getByRole('heading', { name: /Connect clients to LLMROUTER/i })).toBeVisible()
  await page.getByLabel('Claude Desktop').check()
  await expect(page.getByText('MCP tool access')).toBeVisible()
  await expect(page.getByText(/does not replace Claude Desktop/i)).toBeVisible()
  const command = await page.getByTestId('install-command').textContent()
  expect(command).toContain(page.url().split('/install/')[0])
  expect(command).not.toMatch(/token|sk-[A-Za-z0-9]/i)
  await testInfo.attach('llmrouter-install-page', { body: await page.screenshot({ fullPage: true }), contentType: 'image/png' })
})
```

Add a fallback-model test, OS override test, copy feedback test, all-client visibility test, keyboard navigation test, and an assertion that no token input exists. Existing Playwright projects provide desktop Chrome and Pixel 7 coverage.

- [ ] **Step 2: Run focused browser tests and fix failures**

Run: `cd web && bunx playwright test e2e/install.spec.ts`

Expected: both Chromium projects pass and attach screenshots.

- [ ] **Step 3: Run three review/test loops**

For each loop, inspect `git diff`, unsafe path/secret handling, configuration preservation, cross-platform branches, subprocess arguments, network timeouts, output limits, and product wording. Fix only evidence-backed issues, add a regression test first, then rerun the focused suite. Record loop results in `artifacts/onboarding/review-summary.md` with headings `Loop 1`, `Loop 2`, and `Loop 3`; each issue entry contains file, failing test, fix commit, and final result.

- [ ] **Step 4: Run full backend, frontend, CLI, browser, and security verification**

Run:

```bash
bash scripts/verify-backend.sh
bash scripts/verify-web.sh
bash scripts/verify-cli.sh
bash scripts/onboarding_e2e.sh
cd web && bun run e2e
cd .. && go test -race ./internal/onboarding/... -count=1
git diff --check
```

Expected: all commands pass. Warnings must be classified in the review summary; unresolved correctness/security failures block release.

- [ ] **Step 5: Update user documentation**

Document `/install/`, direct versus MCP modes, supported clients, hidden-token usage, restore/uninstall, temporary staging URL behavior, and Quick Tunnel SSE limitation. Leave the written-spec status pending until Task 9's real-system certification passes.

- [ ] **Step 6: Commit**

```bash
git add web/e2e/install.spec.ts README.md README.zh_CN.md artifacts/onboarding/review-summary.md
git commit -m "test(onboarding): certify client installation flows"
```

### Task 9: Deploy Parallel Staging, Certify Real Clients, and Publish

**Files:**
- Create locally and retain: `artifacts/onboarding/deployment-evidence.md`
- Create on server: `/opt/codex-gateway-next/evidence/onboarding-<timestamp>/`
- Modify: release/tag metadata only after mandatory checks pass

**Interfaces:**
- Consumes: immutable image and release artifacts from Tasks 7–8.
- Produces: healthy public staging `/install/`, certification evidence, pushed commits/tag, and private GitHub prerelease.

- [ ] **Step 1: Verify repository and remote safety before deployment**

Run:

```bash
git status --short --branch
git log --oneline llmrouter/main..HEAD
git diff --name-only llmrouter/main...HEAD
gh repo view NeoMei/LLMROUTER --json nameWithOwner,visibility,url
```

Expected: only intentional onboarding commits are ahead; repository visibility is `PRIVATE`; `.codebase-memory/` and `scripts/prepare-sidecar-env.py` are untracked and unstaged.

- [ ] **Step 2: Build and identify the immutable image**

Run:

```bash
version="v0.1.0-rc.2"
commit=$(git rev-parse --short=8 HEAD)
image="llmrouter:${version}-${commit}"
docker build -t "$image" .
docker image inspect "$image" --format '{{.Id}}' | tee artifacts/onboarding/image-id.txt
```

Expected: build passes and records a content-addressed image ID.

- [ ] **Step 3: Back up and recreate only the parallel LLMROUTER service**

Using an SSH agent or `SSHPASS` supplied outside shell history, copy the image and release assets to `113.249.120.24`. Before recreation, record `docker inspect llmrouter`, its image ID, environment file checksum, database counts, Redis DB setting, `docker inspect new-api`, and current Nginx upstream. Recreate only `llmrouter` on `127.0.0.1:13100`, retaining its existing database and Redis separation. If mandatory health checks fail, recreate `llmrouter` from the recorded previous image; never alter `new-api` or Nginx.

- [ ] **Step 4: Run server and public-origin certification**

Save command output under `/opt/codex-gateway-next/evidence/onboarding-<timestamp>/`:

```bash
curl -fsS http://127.0.0.1:13100/healthz
curl -fsS http://127.0.0.1:13100/install/manifest.json | python3 -m json.tool >/dev/null
curl -fsSI http://127.0.0.1:13100/install/install.sh
curl -fsSI http://127.0.0.1:13100/install/install.ps1
curl -fsSI http://127.0.0.1:13100/install/bin/llmrouter-onboard-linux-amd64
docker logs --since 10m llmrouter 2>&1 | tee llmrouter-logs.txt
```

Repeat health, manifest, page, helper download, `/v1/models`, and one bounded non-streaming request through the current public staging origin. Run SSE certification only through local `13100` or another streaming-capable route.

- [ ] **Step 5: Run disposable-profile real onboarding certification**

On Linux, use a disposable HOME to install and verify Codex CLI, Claude Code, OpenCode, Continue, and Aider plans, then run doctor, repeat install, restore, and uninstall. On the available macOS workstation, use disposable configuration roots to verify Codex Desktop configuration/credential visibility and Claude Desktop JSON/MCP bridge initialization. If official applications are installed, launch and perform the bounded real interaction; otherwise record `UNEXECUTED: application unavailable` rather than `PASS`. Do not expose the real token in evidence.

- [ ] **Step 6: Verify NewAPI non-regression and isolation**

Compare the before/after `new-api` container ID/image, Nginx upstream, production database counts, LLMROUTER database counts, and health endpoints. Expected: production identifiers and upstream are unchanged; LLMROUTER alone uses the new image; both services are healthy.

- [ ] **Step 7: Finalize tracked certification evidence**

Write `artifacts/onboarding/deployment-evidence.md` with image ID, staging origin, server evidence directory, protocol/client matrix, desktop executed/unexecuted status, NewAPI isolation comparison, test commands, known limitations, and rollback image. Change `docs/superpowers/specs/2026-08-11-llmrouter-client-onboarding-design.md` status to `Implemented and certified`, commit both files, and rerun `git diff --check` plus the focused onboarding suites. This commit must exist before the release tag.

```bash
git add artifacts/onboarding/deployment-evidence.md docs/superpowers/specs/2026-08-11-llmrouter-client-onboarding-design.md
git commit -m "docs(release): record onboarding certification"
```

- [ ] **Step 8: Push, tag, and publish the private prerelease**

After all mandatory evidence passes:

```bash
git push llmrouter HEAD:main
git tag -a v0.1.0-rc.2 -m "LLMROUTER v0.1.0-rc.2"
git push llmrouter v0.1.0-rc.2
gh release create v0.1.0-rc.2 \
  --repo NeoMei/LLMROUTER \
  --prerelease \
  --title "LLMROUTER v0.1.0-rc.2" \
  --notes-file artifacts/onboarding/deployment-evidence.md \
  web/public/install/checksums.txt web/public/install/bin/*
```

Expected: main contains the certified revision; tag and private prerelease resolve to the same commit; artifacts and checksums are attached.

- [ ] **Step 9: Final verification**

Run:

```bash
git status --short --branch
gh release view v0.1.0-rc.2 --repo NeoMei/LLMROUTER --json url,isPrerelease,tagName,targetCommitish,assets
curl -fsS "${PUBLIC_STAGING_ORIGIN}/install/" >/dev/null
curl -fsS "${PUBLIC_STAGING_ORIGIN}/healthz" >/dev/null
```

Expected: only intentionally untracked local files remain; release is a prerelease with onboarding artifacts; public staging page and health endpoint respond successfully.

---

## Final Evidence Checklist

- [ ] Manifest/schema semantic validation output.
- [ ] Go unit, race, bridge, and disposable-HOME integration results.
- [ ] Bash syntax/ShellCheck and PowerShell parser/Pester results or explicit platform evidence.
- [ ] Backend, web, CLI, and full Playwright results.
- [ ] Desktop and mobile install-page screenshots.
- [ ] Container image ID and static-asset HTTP checks.
- [ ] LLMROUTER protocol matrix and bounded inference results.
- [ ] Direct-client configuration matrix.
- [ ] Codex Desktop configuration and credential-visibility result.
- [ ] Claude Desktop MCP initialize/list-tools/tool-call result.
- [ ] NewAPI unchanged and LLMROUTER database/Redis isolation evidence.
- [ ] Public staging install URL and Quick Tunnel limitation.
- [ ] Git commit, tag, private GitHub release URL, and attached checksums.
