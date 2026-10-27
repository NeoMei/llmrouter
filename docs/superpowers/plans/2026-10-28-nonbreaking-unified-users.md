# Nonbreaking unified users implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development or executing-plans. Execute the already authorized repair without another design confirmation.

**Goal:** Preserve LLMRouter's original capabilities and deliver the corresponding unified-user business flow.

**Architecture:** Keep independently authenticated legacy and unified credentials side by side. Add durable unified keys and budgets to the existing UniHub transaction boundary; dispatch their standard API calls through platform billing and read technical monitoring from durable platform calls.

**Tech Stack:** Go/Gin/GORM, UniHub Go/PostgreSQL, React/TypeScript/Bun.

**Spec:** `docs/superpowers/specs/2026-10-28-nonbreaking-unified-users-design.md`

## Global Constraints

- Do not disable UniHub, automatically link identities, rewrite old keys/balances, grant production roles, transfer funds or issue real supplier/payment calls.
- Normal route authentication and authorization remain mandatory.
- No unified call may debit native wallets or token quota.
- Keep canonical integer-string asset amounts and immutable actor/payer/product/instance scope.
- Preserve unrelated untracked files. Work on `codex/nonbreaking-product-integration`; only the coordinator commits.

### Task 1: Native coexistence

**Repository:** LLMRouter.

Files: `middleware/native_boundary.go`, `native_api_boundary.go`, `native_web_boundary.go`, API key validation in `middleware/auth.go`, config in `router/unihub.go`, corresponding middleware/router tests.

- [x] Add failing integrated-mode tests: native flag true permits normal native login/session/token/relay/admin authorization; flag false stays isolated; projected key credentials stay denied.
- [x] Change the native-boundary predicate to honor the explicit flag independently of UniHub enablement; keep private-transport checks intact.
- [x] Validate projection ownership in both writable and read-only native API-key authentication.
- [x] Run targeted coexistence/auth tests and review the diff.

### Task 2: Durable keys and budgets in UniHub

**Repository:** UniHub.

Files: additive database migration; `internal/modelaccess/api_keys.go`; `internal/billing/usage_budget.go`; `internal/httpapi/model.go`; product scope policy; focused PostgreSQL integration tests.

- [x] Write and run failing tests for create/list/revoke, actor/payer/model/instance isolation, role loss and key secrecy.
- [x] Implement the spec's exact public endpoints and immutable `uh_key_` credential scope; preserve transient task credential semantics.
- [x] Enforce key/account budgets under the existing account lock for reserve and top-up; prove concurrent holds, release and idempotent settle use one ledger.
- [x] Run real PostgreSQL tests, HTTP scope tests and migration checks.

### Task 3: Durable key dispatch and product UI

**Repository:** LLMRouter, with UniHub console return-view handling.

Files: `internal/platformbilling/client.go`, adapter admission validation, additive standard API dispatch router, `router/unihub_bff.go`; `web/src/features/unihub/api-keys-panel.tsx`; key route, capabilities, SSO and native sign-in entry.

Interfaces: use Task 2's `/api-keys`, `/usage-budget`, `credential_kind` and `instance_id`. Instance comes from `UNIHUB_PRODUCT_INSTANCE_ID`, normalized to `default`.

- [x] Add failing tests for standard client bearer-only calls, explicit idempotency replay, wrong instance and no fallback to native billing.
- [x] Route unified keys through existing platform admission/settlement on standard endpoints, synthesize per-call IDs only when absent, preserve explicit IDs.
- [x] Add allowlisted authenticated BFF routes and a payer-bound key list/create/revoke and budget editor. Render native key UI for legacy users and unified key UI on the same `/keys` route.
- [x] Keep created secrets in component memory only; clear on payer/session change. Recheck finance authority server side; show limits separately from money.
- [x] Provide explicit native login entry and retain default unified SSO. Expose product-user authorization navigation with accurate authority descriptions.
- [x] Run adapter/router, typecheck, frontend protocol and rendered interaction checks.

### Task 4: Technical monitoring bridge

**Repository:** LLMRouter.

Files: platform adapter readback and timing metadata; `service/platform_telemetry.go`; affected dashboard/log controllers and tests.

- [x] Write failing tests showing a unified call is visible for its actual actor/key/account and hidden from others, including failed/async calls.
- [x] Read durable platform call/attempt rows and trusted usage into existing dashboard/log response shapes; keep native paths unchanged.
- [x] Record only technical timing/stream metadata in post-consumption hooks. Preserve zero local projected wallet/quota/user counters.
- [x] Test logical-call idempotence, account isolation and no native monetary mutation.

### Task 5: Whole-chain verification and delivery

- [x] Review both full diffs independently and resolve correctness/security findings.
- [x] Run backend required suites and frontend type/lint/format/build checks.
- [x] Exercise the spec's full chain against an isolated real UniHub database and controlled supplier; independently read back ledger, monitoring and untouched native balances.
- [x] Record exact source/test/deployment boundaries.
- [ ] Publish/deploy verified immutable artifacts after the separate deployment step; preserve existing data and runtime configuration.

## Delivery boundary

The user chose to preserve the existing service credential and complete code repair first. Server deployment, registry changes and release publication are deferred; production remains on the previously deployed versions. Source commits and draft review pull requests are the deliverables for this repair stage.

## Final verification

Final code passed the LLMRouter required backend script (full tests, vet, race, build, onboarding), 171 frontend unit tests, 49 browser regressions, type/lint/format/copyright/knip/build, UniHub full Go tests with real PostgreSQL/Formance and actual LLMRouter process, 41 console tests and 16 SDK tests. The final actual cross-repository chain was rerun after the request-context race fix. OIDC verification and supplier responses were controlled fixtures. Independent code reviews and scoped fix reviews found no remaining blocking findings. No server changes were made.
