# Selective Upstream Sync Implementation Plan

> **For agentic workers:** Use subagent-driven-development for the independent ownership groups below. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Port the confirmed New API and OpenCodex correctness fixes without replacing LLMRouter's gateway, billing, continuity, or payment architecture.

**Architecture:** Adapt behavior from immutable upstream commits into the existing Go packages. Keep each ownership group independently reviewable. No wholesale merge or RelayKit/task-plugin migration.

**Tech Stack:** Go/Gin/GORM, SQLite/MySQL/PostgreSQL, Redis, React/Bun build assets.

**Spec:** User-approved 2026-10-24 assessment in this chat, extending the constraints in `docs/superpowers/specs/2026-08-18-core-upstream-correctness-port-design.md`.

## Global Constraints

- Base: `bb7ce224f60dd16092c32d6be4cea14828f0ca86`; integration branch `codex/upstream-sync-20261024`.
- Preserve Gateway channel type 59 and all existing channel identities; do not copy upstream channel constants.
- Preserve wallet atomic reservation/refund, final-group billing, saturating quota math and audit markers, scoped Responses continuity, final-request tool policy, replayable bodies, BYOK, onboarding, and WeChat top-ups.
- JSON operations use `common.*`. Database changes must support SQLite, MySQL and PostgreSQL; this sync should not need schema changes.
- Backend regression tests use testify require/assert and deterministic boundary fixtures. Observe the relevant failure before fixing it.
- Source toolchain and all Go build stages are aligned to Go 1.26.10; configuration edits are verified by real build/checks rather than new source-text tests.
- Root owns commits/integration. Workers edit only their assigned files and report changes; no git checkout/reset/add/commit and no subagents.
- All external release/deployment actions remain separate from local integration. Preserve original checkout and untracked files.

### Task 1: Build, HTTP transport, logs and Redis recovery

**Files:** `go.mod`, Dockerfiles, Go CI/release configuration; `common/constants.go`, `common/init.go`, `.env.example`, `service/http_client.go`, its tests; `middleware/logger.go`, its tests; `common/limiter/limiter.go`, its tests; `service/error.go` and retry-header tests.

**References:** New API `7d8b91268` (Go 1.26.10), `b518d0033` (response-header timeout), `e2a0c4b16` (NOSCRIPT recovery), `30748c655` (query credential redaction). OpenCodex `30e82f9a287020f47797db54b08012ef80363f27` (`x-should-retry:false`).

**Interfaces:** Existing `newRelayHTTPTransport`, limiter `Allow`, Gin logger formatter, and HTTP error construction; emit existing `types.WithSkipRetry()` semantics so Task 3 does not need retry-policy changes.

- [x] Read each referenced patch and local boundary; preserve existing timeout/proxy and logging contracts.
- [x] Add regressions proving: a stalled response-header wait terminates but a started stream is unaffected; negative/overflow timeout values are safe; Redis SCRIPT FLUSH recovers limits; credentials in query parameters are absent from access logs while benign query data remains; an explicit upstream retry veto becomes SkipRetry without allowing `true` to override local vetoes.
- [x] Run focused tests red, then adapt the smallest fix. Set `RELAY_RESPONSE_HEADER_TIMEOUT=1800` by default, 0 disables it; cap duration conversion before multiplying.
- [x] Set all owned Go toolchain references to 1.26.10, including onboarding build stage. Run focused tests and release configuration checks.
- [x] Root commits this group after review and green verification.

### Task 2: Claude billing and task retry reservation

**Files:** `service/text_quota.go`, `service/billing_usage.go`, `service/tiered_settle.go`, `relay/channel/claude/relay-claude.go`, `relay/relay_task.go`, related tests. Coordinate before touching controller or generic billing-session files.

**References:** New API `1b63c28dcc999c4445c70f21ec90103ab49e8be5`, `94cc3ff0906db37703d5ce2bc6a479209b7b9b23`, `55e2ae54d1445d92482ac9bb27b2409bb8f46b70`.

**Interfaces:** Existing billing session `Reserve` and atomic wallet methods, existing normalized billing usage and expression settlement. Read `pkg/billingexpr/expr.md` before changing expression settlement.

- [x] Read exact patches, current accounting chain and tests; do not import task-plugin architecture or replace already-correct wallet Reserve behavior.
- [x] Add failures for explicit zero versus missing Claude usage, cache-only charges and used_quota, cache write total without 5m/1h breakdown, and task retry to a higher-priced group that reserves before any upstream send.
- [x] Cover retry insufficient balance (no send), lower-price retry/refund, failed request refund and successful settlement with hand-derived quota expectations.
- [x] Implement canonical usage handling and pre-submit repricing/reservation with checked quota math. Preserve ordinary relay group preparation.
- [x] Run `go test ./service/... ./relay/... ./controller/...` focused by relevant test names first, then package checks. Write exact red/green evidence and changed paths in report.
- [x] Root reviews and commits this group.

### Task 3: Streaming completion, Claude keepalive and slow writers

**Files:** `relay/helper/stream_scanner.go`, `relay/channel/openai/relay_responses.go`, `relay/channel/openai/helper.go`, `relay/channel/api_request.go`, `controller/relay.go`, related tests. Do not change billing branches or converters owned by other tasks.

**References:** New API `0c2af9780e45f6f9073528fb0fcbdcd64b5d9495`, `7e6596728ff640392bd77169ebb6137a8fe0c309`, `25372e30b1f94255b1f818aeaaf006f042572580`.

**Interfaces:** Request-scoped pinger lifecycle, scanner terminal state and output delivery; preserve tool validation and eligibility checks before marking completion/persisting continuity.

- [x] Read complete upstream patches and local lifecycle before editing.
- [x] Add regressions for a delivered completed Responses event followed by cancellation, failed final write, malformed/invalid tools, and incomplete terminal events. Only successfully delivered valid completions count as success.
- [x] Add deterministic keepalive lifecycle coverage across request preparation and retries; final errors after SSE headers must be valid SSE rather than a second JSON response.
- [x] Add controlled writer/deadline tests for cumulative-byte stream budgets; avoid long sleeps and throughput benchmarks.
- [x] Adapt fixes while keeping concurrent writes serialized and shutting down pingers/readers cleanly on all exits.
- [x] Run focused package tests and race tests for changed stream paths, report exact commands and results.
- [x] Root reviews and commits this group.

### Task 4: Cross-protocol message and thinking compatibility

**Files:** `dto/text_request.go` or the current Message DTO owner; `service/relayconvert/internal/oai_chat/*`, `service/relayconvert/internal/oai_responses/*`, `service/relayconvert/internal/claude_messages/*`; related converter fixtures/tests. Coordinate any changes to Claude response DTO or native adaptor.

**References:** New API `783735607d863bc182a00b939e498cc22e92f2f2`; OpenCodex `561347cca9cb885ec479009593dee20747dae3c2`, `7a9dc77bdbb89b1e15dd6cac869a4b87a1c91a0b`.

**Interfaces:** Existing converter registry and request-scoped tool ID normalization. A typed/structured reasoning-details carrier may be added to Message and deltas, but must not replace final-request tool validation or leak unsupported signature metadata to unrelated providers.

- [x] Read upstream behavior and local conversion paths, including streaming and non-streaming reverse conversion.
- [x] Add failures proving adjacent assistant messages retain every tool call and result pairing, and configuration_update control items never create empty user messages in Responses-to-Chat conversion.
- [x] Define capability-aware configuration_update behavior from source: preserve supported native input, omit unsupported control items without changing actual conversation items.
- [x] Add exact round-trip fixtures for thinking text plus signature through Chat reasoning_details, both stream and nonstream, including tool-result replay and plain reasoning without signature.
- [x] Implement narrowly; preserve unknown metadata safely and provider signature association. Run converter suite and related DTO/tool policy tests.
- [x] Root reviews and commits this group.

### Task 5: Integration, review and local delivery

**Files:** This plan, synchronization evidence document under `docs/operations/`, any fixes found by review.

- [x] Verify each selected patch is covered and distinguish rc.46 changes from later main patches. Record deliberate deferrals: scoped access tokens, task plugins, RelayKit migration, new provider features and GUI changes.
- [x] Run `bash scripts/verify-backend.sh` with real built web assets, plus relevant release configuration/CI checks; preserve output logs and failures for diagnosis.
- [x] Run frontend build/typecheck and existing payment regression coverage; no new UI is introduced.
- [x] Obtain independent task reviews and a whole-branch review. Resolve correctness findings and rerun affected checks.
- [x] Commit integration/evidence, verify clean worktree, and deliver the branch plus exact verification limits. Do not claim production acceptance from unit tests.
