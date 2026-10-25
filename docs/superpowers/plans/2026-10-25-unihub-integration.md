# LLMRouter UniHub Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate LLMRouter with UniHub identity, personal/team resources, recharge/subscription UI and unified model settlement.

**Architecture:** Reuse the durable gateway bridge on current LLMRouter and an independently authenticated UniHub product UI module. Keep UniHub the sole platform ledger and preserve existing local operator/legacy paths. Add separately authorized pool reporting to UniHub.

**Tech Stack:** Go/Gin/GORM; React 19/TypeScript/TanStack Query; UniHub Go/PostgreSQL/Formance and TypeScript SDK.

**Spec:** docs/superpowers/specs/2026-10-25-unihub-integration-design.md

## Global Constraints

- SQLite, MySQL >= 5.7.8 and PostgreSQL >= 9.6 must remain supported in LLMRouter.
- Platform calls never debit local user/token quota or fall back to local billing.
- CREDIT/6 and CNY/6 are separate integer-string assets; no silent fallback or conversion.
- Business identity is fixed issuer plus immutable UniHub user ID; no email merging or operator elevation.
- UniHub product tokens stay in memory; service/merchant tokens never reach browsers.
- Financial writes use stable explicit idempotency keys and readback; no automatic sandbox settlement.
- Every member/finance operation is reauthorized by UniHub; exact registered client/redirect/Origin is required.
- Existing product-origin query isolation remains unchanged; cross-product reports require explicit llmrouter scope and current finance role.
- Preserve upstream licenses, current account-pool/provider behavior, local operators and legacy users.
- New backend tests use testify require/assert; new frontend tests belong in module __tests__ directories.
- No production cutover, publication or real-merchant activation in this implementation run.

---

### Task 1: Bring the durable composable gateway onto current LLMRouter

**Files:** internal/platformbilling/*; router/platform*.go; controller/relay.go; service/{billing,billing_session,quota,text_quota,tiered_settle,task_billing,violation_fee}.go; relay/common/relay_info.go; relay/channel/openai/relay-openai.go; relay/compatible_handler.go; main.go.

**Interfaces:** Consumes existing UniHub model credential/internal calls protocol. Produces opted-in `POST /platform/v1/chat/completions` and `GET /platform/v1/calls/:callID`, with existing ConfigFromEnv/New lifecycle.

- [ ] Commit the pre-existing reviewed composable bridge changes as a preservation checkpoint; merge pinned current LLMRouter into this isolated worktree and resolve only integration conflicts.
- [ ] Add a regression protecting current account-pool retry validation and the platform no-local-quota invariant. Use actual route/HTTP fixtures, including replay suppression after terminal response loss, rather than implementation-shape assertions.
- [ ] Run `GOMAXPROCS=4 go test -p 4 ./internal/platformbilling ./service ./router -count=1` and verify a meaningful red for a newly uncovered integration defect before fixing it. Reused tests need not be rewritten.
- [ ] Adapt platform integration to the new current route/usage/send boundaries. Preserve all current native paths; fail closed for unsupported formats and unknown sends.
- [ ] Run the same suites plus `go test -race ./internal/platformbilling ./service/accountpool -count=1`; record commands/results and commit.

### Task 2: Add explicit unified model pool reporting in UniHub

**Files:** UniHub worktree internal/products/{registry,registry_test}.go; internal/httpapi/{product,product_queries}*.go; packages/sdk/src/index.ts and tests; api/openapi.yaml; scripts/product_client.py and scripts/test-product-client.py; docs/headless-product-api.md. Worktree is `/Users/neomei/.codex/worktrees/composable-billing/unihub`.

**Interfaces:** Produces `GET /v1/accounts/{account}/model-pool/calls`, `/model-pool/calls/{call}`, `/model-pool/usage/summary`, gated by `usage:all-products` AND llmrouter product AND current owner/billing_admin role. SDK produces `modelPoolCalls(account,filter)`, `modelPoolCall(account,id,origin?)`, `modelPoolUsage(account,filter)`.

- [ ] Write real HTTP policy tests: ordinary product, missing scope and member/admin are denied; finance llmrouter client can aggregate SuperWagie/AgentWiki and filter one origin; existing `/calls` remains own-origin; account/cursor/asset boundaries remain intact.
- [ ] Run focused tests with the established isolated PostgreSQL fixture and verify red, then add strict registry scope validation and handlers reusing existing billing queries. Update the offline product-client onboarding scope validator with the same llmrouter-only rule so operators can actually configure the capability.
- [ ] Add SDK real HTTP contract tests for the named methods and OpenAPI paths; quantities stay strings, errors remain structured.
- [ ] Run affected Go tests, SDK tests/typecheck, update exact configuration/docs and commit only Task 2 files. Never edit production configuration or waive RBAC to make UI work.

### Task 3: Integrate LLMRouter product identity, account selection and financial UI

**Files:** LLMRouter internal/platformaccount/* (new fixed-service identity validation/config); model/platform_identity*.go; controller/platform_account*.go; router/platform_account*.go; main.go; web/src/features/unihub/{api,types,session,account-panel,billing-panel,usage-panel,index} modules and __tests__; login/wallet entry components; i18n locales; optional licensed vendored UniHub SDK.

**Interfaces:** Public non-secret integration config identifies fixed UniHub base URL/client/redirect. Proof endpoint validates supplied product token against fixed `/v1/me` and `/v1/accounts` (never a browser supplied service URL) and returns immutable identity/account information; any local projection is least-privilege and cannot use local money/key endpoints. Product frontend independently calls UniHub with its own scoped token. Short-lived model grants carry chosen account/model/task and funding mode into Task 1 route. Task 2 SDK modelPool methods support optional finance overview.

- [ ] Read applicable frontend/backend instructions, relevant auth/session and wallet source. Write failing HTTP/DB tests for fixed issuer/user mapping, untrusted proofs, no email merge/admin elevation, local financial bypass rejection and account scope. Write frontend behavior tests for PKCE state, logout/expiry clearing, exact money, account-switch request binding and finance visibility.
- [ ] Implement explicit opt-in fixed integration config and proof mapping without storing product passwords/tokens in browser persistence or database. Keep operator/local login functioning and distinguish all credential types.
- [ ] Add UniHub-native registration/login and product-owned account UI to discoverable login/wallet surfaces. Support personal/team selection, team creation/invitation acceptance, role-aware member/finance actions, resources, immutable catalog/subscriptions and recharge/order readback. Preserve one idempotency key through an uncertain financial operation, offer query/retry of the original object and do not automatically pay or settle sandbox orders. Do not promise durable API keys from model grants.
- [ ] Add usage finance pool view with own-origin fallback and clear visibility labels; no provider secrets or request content. Display missing live payment capability and unsupported gateway protocols accurately. Support credential issuance/revocation and normal/streaming chat through the trusted gateway, with stable task/call IDs.
- [ ] Run new tests plus existing auth/wallet regression tests, backend suites, frontend typecheck, touched-file lint and production build; fix failures; commit.

### Task 4: End-to-end acceptance, deployment recipe and whole integration review

**Files:** LLMRouter docs/integrations/unihub.md; explicit non-secret env example; route/identity integration fixtures; UniHub docs and gateway_acceptance fixture if adaptation is needed.

**Interfaces:** Consumes Tasks 1–3. Produces reproducible setup and acceptance without production mutation.

- [ ] Run actual LLMRouter route against UniHub HTTP, isolated PostgreSQL and real Formance using the existing composable acceptance fixture. Cover CREDIT and CNY, stream, lost settlement response, durable recovery and zero duplicate provider sends/local charges.
- [ ] Exercise product identity and UI against actual local UniHub/Casdoor if available; otherwise use explicit controlled HTTP fixtures and record that boundary. No credentials or actual provider keys in reports.
- [ ] Document exact llmrouter client/Origin/redirect, scopes, funding profiles/rates, internal service networking, fixed routing identity, token lifecycle, supported protocols, legacy-account migration boundary and rollback for new traffic.
- [ ] Run necessary affected tests/builds and final diff review. Resolve important findings, record exact results and mark completed tasks; leave a reviewable committed branch and honest acceptance report.
