# Native WeChat Pay Top-Up Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a disabled-by-default first-party WeChat Pay APIv3 Native channel that creates exact-CNY top-up orders, verifies callbacks, reconciles missing notifications, and credits immutable quota exactly once.

**Architecture:** Keep `TopUp` as the order and entitlement record. Put WeChat signing, response verification, callback decryption, and provider calls behind a small service interface; keep immutable amount and quota checks in the model settlement boundary; expose local-only status polling and run provider reconciliation through the existing leased system-task scheduler. Add an isolated CNY wallet card and hook so existing Stripe, Waffo, Waffo Pancake, Creem, and Epay flows retain their current JavaScript-number and currency behavior.

**Tech Stack:** Go 1.25, Gin, GORM v2, shopspring/decimal, official `wechatpay-go` v0.2.21, React 19, TypeScript, Base UI/Tailwind, `qrcode.react`, Bun test/build tooling.

**Spec:** `docs/superpowers/specs/2026-08-31-native-wechat-pay-topup-design.md`

## Global Constraints

- The provider is disabled unless `WECHATPAY_ENABLED=true`, payment compliance is confirmed, and every credential/readiness check passes.
- Provider-facing money is integer fen; browser input stays decimal text and never becomes an authoritative JavaScript number.
- Settlement uses stored `credited_quota`; it never recomputes quota from current exchange-rate or pricing settings.
- All database behavior supports SQLite, MySQL 5.7.8+, and PostgreSQL 9.6+.
- All JSON operations in Go business code use `common.Marshal`, `common.Unmarshal`, or `common.DecodeJson`; SDK-owned generated JSON remains inside the SDK.
- Callback, query, and create responses must be cryptographically verified by the official SDK before business use.
- Existing Waffo, Stripe, Creem, Waffo Pancake, Epay, and subscription semantics remain unchanged.
- No real payment, production enablement, deployment, push, or refund is part of this implementation plan.

---

### Task 1: Exact CNY domain and WeChat configuration

**Files:**
- Create: `service/wechat_pay.go`
- Create: `service/wechat_pay_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `ParseWeChatCNYAmount(string) (int64, error)`.
- Produces: `CalculateWeChatCreditedQuota(amountMinor int64, exchangeRate decimal.Decimal, quotaPerUnit decimal.Decimal) (int, error)`.
- Produces: `LoadWeChatPayConfig() (WeChatPayConfig, error)` and `WeChatPayConfig.Ready() error`.
- Produces: `WeChatPayProvider` with `CreateNative`, `QueryOrder`, `CloseOrder`, and `ParseNotify` methods.
- Produces: normalized `WeChatPayTransaction`, `WeChatPayCreateRequest`, and `WeChatPayCreateResult` types independent of SDK structs.

- [ ] **Step 1: Write failing amount and readiness tests**

Add table tests with literal expectations: `"50" → 5000`, `"50.01" → 5001`; reject empty, zero, negative, exponent notation, more than two decimal places, and int64 overflow. Test `¥50` at exchange rate `7.3` and quota-per-unit `500000` against the hand-derived rounded quota `3424658`. Test that readiness rejects HTTP callback origins, query strings, missing files, APIv3 keys not exactly 32 bytes, malformed key IDs, and inverted min/max values.

- [ ] **Step 2: Run the focused service tests and verify RED**

Run: `go test ./service -run 'Test(ParseWeChatCNYAmount|CalculateWeChatCreditedQuota|WeChatPayConfig)' -count=1`

Expected: compilation failure because the WeChat domain types and functions do not exist.

- [ ] **Step 3: Implement exact parsing and configuration**

Use a strict decimal regex and `decimal.NewFromString`; compare the shifted decimal against `math.MaxInt64` before conversion. Load public identifiers and limits from environment variables, secret values from read-only file paths, and derive `https://<trusted-origin>/api/wechat-pay/notify` from `system_setting.ServerAddress`. Keep APIv3 key bytes out of exported config serialization and all logs.

- [ ] **Step 4: Add the official SDK and provider adapter**

Add `github.com/wechatpay-apiv3/wechatpay-go v0.2.21`. Initialize the client with `option.WithWechatPayPublicKeyAuthCipher`, build Native `PrepayRequest` values, capture `Request-Id`, and parse callbacks with `notify.NewNotifyHandler`. Map SDK results into normalized domain structs before returning to controllers or reconciliation code.

- [ ] **Step 5: Run focused and package tests and verify GREEN**

Run: `go test ./service -run 'Test(ParseWeChatCNYAmount|CalculateWeChatCreditedQuota|WeChatPayConfig|WeChatPayProvider)' -count=1`

Expected: PASS with no network calls; SDK behavior is exercised through a local signed fixture or a narrowly injected client boundary.

- [ ] **Step 6: Commit Task 1**

Commit message: `feat(payment): add WeChat Pay APIv3 provider`

---

### Task 2: Exact TopUp snapshots and atomic settlement

**Files:**
- Modify: `model/topup.go`
- Modify: `model/payment_method_guard_test.go`
- Create: `model/topup_wechat_pay_test.go`

**Interfaces:**
- Consumes: normalized verified transaction fields from Task 1.
- Produces: `PaymentProviderWeChatPay` and `PaymentMethodWeChatNative` constants.
- Produces: `CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput) (*TopUp, bool, error)`.
- Produces: `SettleWeChatTopUp(WeChatTopUpSettlement) (alreadyDone bool, userID int, creditedQuota int, err error)`.
- Produces: `ExpireWeChatTopUp(tradeNo string) error`, `GetUserWeChatTopUp(userID int, tradeNo string) (*TopUp, error)`, `HasDueWeChatTopUps(now int64) bool`, and `ClaimDueWeChatTopUps(now int64, limit int) ([]*TopUp, error)`.

- [ ] **Step 1: Write failing migration and idempotent-order tests**

Assert that a migrated `TopUp` persists `currency`, `paid_amount_minor`, `credited_quota`, nullable `provider_trade_no`, `next_reconcile_time`, and `reconcile_attempts`. Create the same deterministic trade number twice with identical snapshots and assert one row is reused; retry with a different amount and assert `ErrTopUpOrderConflict`.

- [ ] **Step 2: Run model tests and verify RED**

Run: `go test ./model -run 'Test(WeChatTopUpMigration|CreateOrReuseWeChatTopUp)' -count=1`

Expected: compilation failure for missing fields and functions.

- [ ] **Step 3: Add provider-neutral fields and order creation**

Add nullable `*string` provider transaction ID and a composite unique index with `payment_provider`. Use GORM operations only. On unique trade-number insertion races, load the winner and compare owner, provider, amount, currency, credited quota, creation time-derived expiry, and status before reuse.

- [ ] **Step 4: Write failing settlement tests**

Cover exact stored-quota credit, duplicate callback idempotency, callback/query race, amount mismatch, currency mismatch, foreign provider, terminal status, quota overflow rollback, transaction-ID conflict across two orders, and cache synchronization after commit. Mutating `USDExchangeRate` after order creation must not change credited quota.

- [ ] **Step 5: Run settlement tests and verify RED**

Run: `go test ./model -run 'TestSettleWeChatTopUp' -count=1`

Expected: compilation failure for missing settlement behavior.

- [ ] **Step 6: Implement atomic settlement and expiry**

Lock by `trade_no` through `lockForUpdate(tx)`, validate immutable snapshots, treat identical success as idempotent, save the provider transaction ID and success state, and call `creditTopUpQuota` in the same transaction. Sync cache and write the top-up log only after commit. Make manual admin completion use stored `credited_quota` for WeChat orders.

- [ ] **Step 7: Write RED tests for durable reconciliation claims**

Create due and not-due pending rows; assert only due WeChat rows are claimed, each claim atomically advances attempt count and next time, concurrent claims return each row once, and terminal or foreign-provider rows are never claimed.

- [ ] **Step 8: Implement claims and run model GREEN tests**

Run: `go test ./model -run 'Test(WeChat|SettleWeChat|ClaimDueWeChat|ManualCompleteWeChat)' -count=1`

Expected: PASS.

- [ ] **Step 9: Commit Task 2**

Commit message: `feat(payment): add exact WeChat top-up settlement`

---

### Task 3: Create, status, and callback APIs

**Files:**
- Create: `controller/topup_wechat_pay.go`
- Create: `controller/topup_wechat_pay_test.go`
- Modify: `controller/payment_webhook_availability.go`
- Modify: `controller/payment_webhook_availability_test.go`
- Modify: `controller/topup.go`
- Modify: `router/api-router.go`

**Interfaces:**
- Consumes: provider/config interfaces from Task 1 and model operations from Task 2.
- Produces: `POST /api/user/wechat-pay/native` with `{amount: string, idempotency_key: string}`.
- Produces: `GET /api/user/wechat-pay/orders/:trade_no` with owner-filtered local state.
- Produces: public `POST /api/wechat-pay/notify` callback.
- Extends `/api/user/topup/info` with `enable_wechat_pay`, `wechat_pay_min_amount`, and `wechat_pay_max_amount` only when readiness and compliance pass.

- [ ] **Step 1: Write failing readiness and create-handler tests**

Test disabled configuration, incomplete files, invalid amount, invalid idempotency key, quota ceiling, deterministic order number, identical retry reuse, conflicting retry rejection, exact provider request, definitive 4xx marking `failed`, and ambiguous timeout retaining `pending`. Use a provider fake that returns complete normalized provider responses; assert handler responses and database rows, not fake existence.

- [ ] **Step 2: Run controller create tests and verify RED**

Run: `go test ./controller -run 'Test(WeChatPayAvailability|RequestWeChatNativePay)' -count=1`

Expected: compilation failure because handlers and readiness functions do not exist.

- [ ] **Step 3: Implement readiness, create route, and local status route**

Generate `WXN` plus 29 lowercase SHA-256 hex characters from authenticated user ID and the validated idempotency key. Apply `CriticalRateLimit` to create, `DisableCache` to status, normal user authentication to both, and the anonymous body limit to notify. Never synchronously call the provider from status.

- [ ] **Step 4: Write failing callback tests**

Cover cryptographic parse failure, `SIGNTEST` failure, wrong event state, wrong `NATIVE` trade type, configured merchant/AppID mismatch, unknown order, amount/currency mismatch, duplicate success, and transient database failure. Valid semantic incidents must be durably recorded then return `204`; unverifiable or non-durable failures return non-2xx.

- [ ] **Step 5: Implement callback validation and acknowledgement policy**

Read the request body only through the SDK callback parser, validate every normalized field, call the one model settlement operation, and keep the full path free of outbound provider calls. Log correlation fields without signatures, keys, authorization headers, or decrypted bodies.

- [ ] **Step 6: Run controller GREEN tests**

Run: `go test ./controller -run 'Test(WeChatPay|RequestWeChat|GetWeChat|WeChatPayNotify)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit Task 3**

Commit message: `feat(payment): expose WeChat Native top-up APIs`

---

### Task 4: Leased reconciliation and close-before-expire state machine

**Files:**
- Create: `service/wechat_pay_reconciliation.go`
- Create: `service/wechat_pay_reconciliation_test.go`
- Modify: `model/system_task.go`
- Modify: `controller/system_task_handlers.go`
- Create: `controller/system_task_wechat_pay_test.go`

**Interfaces:**
- Consumes: `HasDueWeChatTopUps`, `ClaimDueWeChatTopUps`, provider query/close methods, and model settlement/expiry operations.
- Produces: `ReconcileWeChatTopUps(ctx context.Context, provider WeChatPayProvider, config WeChatPayConfig, now int64, limit int) WeChatPayReconcileSummary`.
- Produces: scheduled system-task type `wechat_pay_reconcile` every 30 seconds only when configured and due work exists.

- [ ] **Step 1: Write failing reconciliation state tests**

Assert `SUCCESS → settle`, pre-deadline `NOTPAY → pending`, deadline `NOTPAY → close → expired`, `CLOSED → expired`, query/close ambiguity → pending with retry time, and payment/close race resolved by a second signed query. Include transaction mismatch and provider error summaries without leaking bodies.

- [ ] **Step 2: Run service reconciliation tests and verify RED**

Run: `go test ./service -run 'TestReconcileWeChatTopUps' -count=1`

Expected: compilation failure for missing reconciler.

- [ ] **Step 3: Implement the bounded state machine**

Process only claimed rows. Validate successful query results through the same normalized transaction validator used by callbacks. Never map `NOTPAY` directly to failed or expired. After deadline, mark expired only after successful close or authoritative `CLOSED`; retain pending on ambiguous close.

- [ ] **Step 4: Write and implement scheduled-handler tests**

Verify registration, `Enabled()` gating on readiness and due work, 30-second cadence, DB-leased task execution, and summary persistence. Run: `go test ./controller -run 'TestWeChatPayReconcileSystemTask' -count=1`.

- [ ] **Step 5: Run service, controller, and model payment tests**

Run: `go test ./service ./controller ./model -run 'WeChat|PaymentMethodGuard|PaymentWebhookAvailability' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit Task 4**

Commit message: `feat(payment): reconcile WeChat top-up orders`

---

### Task 5: Isolated CNY wallet UI and QR lifecycle

**Files:**
- Modify: `web/src/features/wallet/types.ts`
- Modify: `web/src/features/wallet/api.ts`
- Create: `web/src/features/wallet/hooks/use-wechat-native-payment.ts`
- Create: `web/src/features/wallet/hooks/use-wechat-native-payment.test.ts`
- Create: `web/src/features/wallet/components/cny-topup-card.tsx`
- Create: `web/src/features/wallet/components/dialogs/wechat-native-payment-dialog.tsx`
- Modify: `web/src/features/wallet/hooks/index.ts`
- Modify: `web/src/features/wallet/index.tsx`
- Modify: `web/src/i18n/locales/en.json`
- Modify: `web/src/i18n/locales/zh.json`
- Modify: synchronized locale JSON files generated by `bun run i18n:sync`

**Interfaces:**
- Consumes: top-up info readiness and create/status API contracts from Task 3.
- Produces: CNY card presets `50`, `100`, `200`, `500`; editable decimal-text input; Native QR dialog using existing `qrcode.react`.
- Produces: hook states `idle | creating | pending | success | failed | expired | error`.

- [ ] **Step 1: Write failing hook tests**

Use injected API functions and fake timers. Assert decimal text is sent unchanged, one idempotency key is reused across create retries, local status polls every two seconds, polling stops on terminal state/unmount, and success invokes wallet/history refresh once.

- [ ] **Step 2: Run hook tests and verify RED**

Run from `web/`: `bun test src/features/wallet/hooks/use-wechat-native-payment.test.ts`

Expected: module-not-found failure for the new hook.

- [ ] **Step 3: Implement API types and hook**

Add exact string/minor-unit response types and include `failed` in `TopupStatus`. Keep WeChat code out of `use-payment.ts`; use `crypto.randomUUID()` once per browser action and preserve it until a terminal state or explicit new payment.

- [ ] **Step 4: Write failing component tests for observable UI**

Assert CNY and international sections are distinct, `50` renders `¥50.00`, no `¥365` text appears, QR value equals returned `code_url`, countdown is based on server expiry, and failed/expired states expose a new-payment action.

- [ ] **Step 5: Implement the card and dialog**

Render the CNY card only when `enable_wechat_pay` is true. Use `t('English source key')` for every user-facing string, accessible labels for amount and QR state, and a dialog close action that stops browser polling without mutating the server order.

- [ ] **Step 6: Sync translations and run frontend GREEN checks**

Run from `web/`:

```bash
bun run i18n:sync
bun test src/features/wallet/hooks/use-wechat-native-payment.test.ts src/features/wallet
bun run lint
bun run build
```

Expected: all commands exit 0.

- [ ] **Step 7: Commit Task 5**

Commit message: `feat(wallet): add WeChat Native CNY top-ups`

---

### Task 6: Cross-database, regression, and operational verification

**Files:**
- Modify only files required by failures discovered in this verification task.

**Interfaces:**
- Consumes every previous task.
- Produces verified disabled-by-default implementation artifacts; does not enable or deploy them.

- [ ] **Step 1: Run formatting and static checks**

Run:

```bash
gofmt -w service/wechat_pay*.go model/topup*.go controller/*wechat_pay*.go controller/system_task_handlers.go router/api-router.go
go vet ./service ./model ./controller ./router
```

- [ ] **Step 2: Run focused backend payment suites**

Run: `go test ./service ./model ./controller ./router -run 'WeChat|TopUp|Payment|SystemTask' -count=1`

- [ ] **Step 3: Run complete backend tests**

Run: `go test ./... -count=1`

- [ ] **Step 4: Run complete frontend checks**

Run from `web/`:

```bash
bun test
bun run lint
bun run build
```

- [ ] **Step 5: Inspect migration SQL on supported dialect fixtures**

Verify SQLite AutoMigrate directly and use existing MySQL/PostgreSQL migration test infrastructure or dry-run GORM sessions to confirm nullable composite uniqueness, quoting, and conditional claims contain no unsupported dialect syntax.

- [ ] **Step 6: Re-read the spec line-by-line**

Confirm every in-scope requirement maps to code and at least one observable test. Confirm no secret value, real merchant private key, APIv3 key, decrypted callback payload, production enablement, refund, deployment, or unrelated file entered the diff.

- [ ] **Step 7: Commit verification fixes if any**

Commit message when needed: `test(payment): verify WeChat Native top-ups`

- [ ] **Step 8: Stop before production operations**

Report local branch, commits, focused/full test evidence, remaining manual T+1 runbook work, and the exact secret files still required. Request separate authorization before any server upload, production configuration, real payment, push, merge, or deployment.
