# Native WeChat Pay Top-Up Design

## Goal

Add a first-party WeChat Pay APIv3 Native payment channel to LLMRouter so a signed-in user can enter a CNY amount, scan a WeChat QR code, and receive the corresponding immutable wallet quota after a verified payment.

The approved checkout contract is exact and simple: entering `50` means paying `¥50.00` and receiving `¥50.00` of quota value at the order's snapshotted conversion rate. The wallet remains a quota ledger rather than a legally denominated CNY stored-value account. The new channel does not use the legacy Epay `amount × 7.3` payment calculation.

## Approved scope

The first release covers one-time wallet top-ups through WeChat Native payment:

- Create a CNY top-up order.
- Obtain a Native payment `code_url` from WeChat Pay.
- Render the payment QR code and poll local order state.
- Verify and decrypt WeChat Pay APIv3 callbacks.
- Validate payment identity, currency, and amount before settlement.
- Credit the wallet exactly once.
- Reconcile pending orders against WeChat Pay when notification delivery is delayed.
- Query and close unpaid orders after their 15-minute payment deadline before marking them expired.

Subscriptions, automatic refunds, JSAPI payment, Mini Program payment, APP payment, and Alipay implementation are outside this first release. Alipay will be a separate provider that can later reuse the CNY wallet user experience and established settlement invariants.

## Confirmed merchant prerequisites

The production merchant configuration currently available is:

- WeChat Pay merchant ID: `1295594101`.
- Bound application ID: `wxa0b1bc308cdea2b2`.
- Native payment product: enabled.
- Merchant API certificate: configured.
- APIv3 key: configured.
- WeChat Pay public-key mode: enabled.
- Domain ownership verification file: publicly available at `/MP_verify_EyH0GhcnlJXUKru2.txt`.

No secret key or private-key material belongs in this document, source control, application logs, database settings, or browser responses.

## Selected approach

Implement WeChat Pay as a dedicated official provider alongside the existing Waffo, Stripe, Creem, and Epay code paths. Do not translate the official WeChat API into the legacy Epay protocol and do not build a broad generic payment framework first.

The provider owns WeChat-specific request signing, API calls, callback verification, payload decryption, and transaction-state interpretation. It reuses the existing `TopUp` order model, database row-lock pattern, quota ceiling checks, atomic wallet credit, cache synchronization, and audit logging.

This keeps the current working payment channels unchanged and gives a later Alipay provider a clear sibling pattern without coupling two unrelated signature protocols.

## Backend components

### Provider service

Add `service/wechat_pay.go` around the official `github.com/wechatpay-apiv3/wechatpay-go` SDK. It is responsible for:

- Constructing the APIv3 client from production credentials.
- Creating Native transactions through `/v3/pay/transactions/native`.
- Querying an order by merchant order number.
- Closing an unpaid order.
- Verifying WeChat Pay response and callback signatures with the configured WeChat Pay public key.
- Decrypting callback resources with the APIv3 key.

Keep the SDK behind a small provider interface so controller tests can use deterministic fakes. Business settlement logic must not depend directly on SDK response structs.

### Controller

Add `controller/topup_wechat_pay.go` with three endpoints:

- `POST /api/user/wechat-pay/native`: create a signed-in user's Native top-up order.
- `GET /api/user/wechat-pay/orders/:trade_no`: return the owner-visible local state without synchronously querying the provider.
- `POST /api/wechat-pay/notify`: receive unauthenticated but cryptographically verified payment notifications.

The create and status endpoints require normal user authentication. The notification route is public and rejects any request that does not pass APIv3 signature verification and resource decryption. The status endpoint reads local state only; authenticated browser polling does not directly fan out into WeChat Pay API calls.

Add a master-node reconciliation task that scans recent pending WeChat orders at a bounded cadence. It queries provider state, closes unpaid expired orders, and calls the same settlement path used by callbacks. This keeps provider traffic independent of browser refresh frequency and remains correct in a multi-instance deployment.

### Model and settlement

Add provider and method constants such as `wechat_pay` and `wechat_native`. Keep `TopUp` as the single order and entitlement record; do not introduce a second generic payment-transaction table in this release. Expose one model-level settlement operation shared by callbacks and reconciliation:

1. Lock the `TopUp` row with `lockForUpdate(tx)`.
2. Require the expected provider and a `pending` or already-successful status.
3. Treat an already-successful order as an idempotent duplicate.
4. Require the provider transaction ID to be absent or identical to the current verified transaction.
5. Credit the stored quota through `creditTopUpQuota`.
6. Save provider transaction metadata, completion time, and `success` status in the same transaction.
7. Synchronize the user quota cache only after commit.

Do not use the process-local order lock as the correctness boundary. Database locking and the conditional quota update remain authoritative across multiple application instances.

## Exact CNY accounting

Provider-facing amounts use integer fen. Never construct a payment amount with binary floating-point arithmetic.

The order request accepts a decimal CNY string, validates at most two fractional digits, and converts it to `paid_amount_minor` as an integer. The backend, not the browser, computes every authoritative value.

Add exact order snapshot fields to `TopUp`:

- `currency`: `CNY` for this provider.
- `paid_amount_minor`: the authorized payment amount in fen.
- `credited_quota`: the exact quota that will be added after settlement.
- `provider_trade_no`: a nullable WeChat Pay transaction ID after it is known.
- `next_reconcile_time`: the next Unix timestamp at which this pending order is eligible for provider reconciliation.
- `reconcile_attempts`: the number of claimed provider reconciliation attempts.

Create the columns through GORM-compatible migration code that works on SQLite, MySQL, and PostgreSQL. Add a nullable composite unique index on `payment_provider` and `provider_trade_no` so one provider transaction cannot settle two local orders while existing rows can retain a null transaction ID. The reconciliation fields are provider-neutral bookkeeping; legacy providers leave them at their zero values. Existing rows and providers retain their current semantics.

For the existing quota wallet, compute credited quota at order creation with decimal arithmetic:

`credited quota = CNY amount / USD exchange rate × quota per unit`

Use the centralized checked quota conversion helper and enforce the existing int32 wallet ceiling before calling WeChat Pay. Store the resulting integer quota on the order so exchange-rate or pricing changes after checkout cannot change settlement. The confirmation dialog and order history show the exact paid CNY amount from the order snapshot; they never reconstruct it from the user's current quota balance.

The phrase `¥50到账` means `¥50.00` of quota value at the rate snapshotted for that order. Because the system has one quota ledger, changing the global wallet display rate can change the displayed CNY equivalent of an existing total balance. A true persistent CNY face-value wallet would require a separate multi-currency ledger and is deliberately not implied by this release.

The first release intentionally does not apply the legacy Epay `Price`, top-up group multiplier, or preset discount to WeChat CNY orders. The entered CNY amount, charged CNY amount, and purchased quota value at checkout are equal. Any future CNY promotion must be represented explicitly rather than silently changing the meaning of the amount field.

The existing floating `money` column may remain populated for backward-compatible order display, but it is not authoritative for signature validation or settlement. Security decisions use `paid_amount_minor`, `currency`, and `credited_quota` only.

## Create-order flow

1. Validate that the WeChat provider is enabled and all required configuration is present.
2. Parse the requested CNY amount as decimal text and convert it to fen.
3. Require a client-generated idempotency key and derive the merchant order number as `WXN` plus a truncated SHA-256 digest of the user ID and idempotency key, using only WeChat-allowed characters and at most 32 characters.
4. Look up that order number before calculating a new quote. If it exists, require the same owner, requested amount, and currency, then reuse its stored quota snapshot, creation-time-derived expiry, and all other immutable provider parameters. Reject conflicting reuse of an idempotency key.
5. For a new order, enforce configurable minimum and maximum amounts, positive quota, and the user's remaining quota capacity, then calculate and snapshot `credited_quota`.
6. Insert the local `pending` order before the provider call. A concurrent insert for the same order number resolves by loading and validating the winner rather than creating another order.
7. Call WeChat Native order creation with the configured AppID, merchant ID, fixed description, CNY amount, notification URL, and expiry derived from the stored creation time. Every retry for the same merchant order number uses byte-for-byte equivalent business parameters.
8. Mark the local order `failed` only for a definitive provider rejection such as a verified non-retryable parameter or permission error.
9. On a network timeout, connection failure, HTTP 5xx, or other ambiguous result, keep the local order `pending` and reconcile it by the same merchant order number. Never create a replacement order merely because the first response was lost.
10. On success, return only the local order number, QR `code_url`, CNY amount, currency, snapshotted credited quota, and expiry time.

If the first successful create response is lost, retry Native creation with the same merchant order number and identical parameters. If WeChat reports that the order already exists but does not return a recoverable `code_url`, query it first; close it only if it is still `NOTPAY`, then require a new idempotency key for a replacement order. At no point may two payable orders represent one browser action.

The browser never receives certificates, private keys, the APIv3 key, or decrypted provider payloads.

## Callback verification and settlement

The callback handler reads the raw request body once. It verifies `Wechatpay-Timestamp`, `Wechatpay-Nonce`, `Wechatpay-Serial`, and `Wechatpay-Signature` before decrypting the resource.

Before settlement it must require all of the following:

- Successful APIv3 signature verification.
- Successful AES-256-GCM resource decryption.
- Event type and transaction state representing a successful payment.
- Transaction trade type is exactly `NATIVE`.
- Callback merchant ID equals the configured merchant ID (`1295594101` for the confirmed production configuration).
- Callback AppID equals the configured AppID (`wxa0b1bc308cdea2b2` for the confirmed production configuration) when present in the transaction payload.
- Callback merchant order number resolves to an existing WeChat top-up owned by the expected user.
- Currency is exactly `CNY`.
- Callback `amount.total` exactly equals `paid_amount_minor`.
- The provider transaction ID is not already associated with another local order.

Only then may the model settlement transaction run. The callback performs no outbound provider call: it verifies, decrypts, validates, and executes one short local database settlement transaction. It returns `204` after a successful or idempotently repeated settlement. A transient local persistence failure returns a non-2xx response so WeChat retries. The complete path must stay below WeChat's five-second acknowledgement limit.

Invalid signatures and decryption failures never mutate order or wallet state, return a non-2xx acknowledgement, and generate a security warning with request correlation data. A cryptographically valid notification with an unknown order, identity mismatch, amount mismatch, or conflicting provider transaction ID is durably recorded as a reconciliation incident and acknowledged with `204`: retrying the same immutable business payload cannot make it valid, while returning non-2xx would create a retry storm. If the incident cannot be durably recorded, return non-2xx. Duplicate valid notifications return a successful acknowledgement without adding quota again.

## Status, delayed notifications, and expiry

The frontend polls the authenticated local status endpoint while the QR dialog is open. The endpoint returns only orders owned by the signed-in user and never synchronously calls WeChat Pay.

The master-node reconciliation task scans eligible pending orders every 30 seconds. Eligibility is persisted in `next_reconcile_time`; a database conditional update on status and due time claims each attempt and advances `reconcile_attempts`, so restarts, duplicate scheduler ticks, or accidental multiple master nodes cannot amplify provider traffic. The retry schedule targets 30 seconds, 1 minute, 3 minutes, 5 minutes, 10 minutes, and the 15-minute close boundary. A successful provider query goes through the same identity, trade type, currency, amount, transaction-ID uniqueness, and idempotent settlement checks as a callback.

Use the following local state rules:

- `pending → success` only after a verified callback or signed query response reports `SUCCESS` and all immutable fields match.
- `pending → failed` only after a definitive non-retryable create-order rejection.
- `pending → expired` only after the payment deadline and either a successful close call or a signed query response reports `CLOSED`.
- A signed `NOTPAY` response is not a failure and does not by itself change the local terminal state.
- Query or close results that are ambiguous leave the order `pending` for later reconciliation.
- `success`, `failed`, and `expired` are terminal. The close-before-expire rule removes the need for an ambiguous `expired → success` transition.

If payment and closing race, query authoritative provider state: settle on `SUCCESS`, expire on `CLOSED`, and otherwise retain `pending`. The reconciliation task stops querying terminal orders.

## Configuration and secret handling

Use server environment variables and read-only mounted files:

- `WECHATPAY_ENABLED`
- `WECHATPAY_MCH_ID`
- `WECHATPAY_APP_ID`
- `WECHATPAY_MCH_CERT_SERIAL`
- `WECHATPAY_MCH_PRIVATE_KEY_PATH`
- `WECHATPAY_API_V3_KEY_PATH`
- `WECHATPAY_PUBLIC_KEY_ID`
- `WECHATPAY_PUBLIC_KEY_PATH`
- Optional next-public-key ID and path for a controlled rotation overlap
- Configurable minimum and maximum CNY top-up amounts

The callback origin is derived from the trusted server address, producing `https://llmrouter.quukk.com/api/wechat-pay/notify` in production.

Private-key and APIv3-key files are mounted read-only with restrictive filesystem permissions. Secret values are never included in environment-variable values, database option rows, frontend settings payloads, error responses, or logs. Configuration readiness validates file presence, permissions, key parsing, APIv3-key length, merchant-certificate serial format, public-key ID, merchant ID, and AppID; it gates both API availability and frontend visibility.

Public-key rotation uses a controlled overlap: the verifier accepts the configured current and next public keys during the rotation window, then removes the retired key after deployment verification. Secret rotation takes effect through a controlled application restart.

Changing merchant ID, AppID, or the trusted callback origin is not an in-place secret rotation. Disable new orders and drain or explicitly close all pending WeChat orders before changing those immutable merchant-identity parameters, so retries and callbacks are always validated against the same identity used at creation.

## Wallet user experience

Split recharge choices by currency:

- **CNY payment**: amounts display in `¥`; suggested values are ¥50, ¥100, ¥200, and ¥500; the first release shows WeChat Pay only and labels the result as quota value rather than a separately held CNY cash balance.
- **International payment**: existing Waffo and Stripe behavior remains unchanged and continues to display its actual pricing currency.

Hide the current legacy Epay Alipay and WeChat entries while Epay credentials are absent. They are display configuration only and must not imply that a working payment rail exists.

Implement the CNY experience as an isolated card and hook instead of extending the existing international-payment amount hook, whose state and calculations use JavaScript numbers and USD-oriented assumptions. The CNY request keeps the amount as decimal text until the backend converts it to integer fen. This limits the blast radius and leaves Waffo and Stripe behavior unchanged.

Selecting WeChat opens a QR dialog containing the exact CNY amount, QR code, local order number, 15-minute countdown, and current local state. Success closes the dialog and refreshes wallet balance and order history. Closing the dialog does not cancel or settle the order; reopening order history shows its authoritative state. Frontend top-up status types include `failed` as well as `pending`, `success`, and `expired`.

When the future official Alipay provider is available, it joins the CNY section and reuses the same amount semantics without changing the WeChat implementation.

## Error handling and observability

- Reject malformed, fractional-overprecision, below-minimum, above-maximum, or quota-overflow amounts before creating an order.
- Return a stable user-facing error when provider configuration is incomplete.
- Distinguish definitive create rejection from ambiguous transport/provider failure; ambiguous results remain pending for reconciliation.
- Preserve pending state on transient query failures and allow callback retries.
- Record the WeChat response `Request-ID`, local merchant order number, provider transaction ID, state transition, and high-level error code.
- Never log authorization headers, signatures in full, private keys, APIv3 keys, or complete decrypted callback bodies.
- Emit warnings for signature probes, amount mismatches, identity mismatches, unknown orders, repeated query failures, and quota saturation.

## Reconciliation and refund operations

Callbacks and short-term provider queries are not the final accounting boundary. After 10:00 on T+1, operations must obtain the prior day's WeChat transaction bill and compare successful provider transactions with local successful top-ups. Any provider-success/local-non-success mismatch is resolved through a controlled top-up correction or refund decision with an audit record. The first release may use a documented manual comparison, but production launch must assign an owner and retain the reconciliation result; automation can follow without changing settlement semantics.

Self-service and automatic refunds remain out of scope. Operators must not issue an ad hoc refund in the WeChat merchant console while leaving credited quota untouched. A manual refund requires a correlated wallet adjustment and audit record under an approved runbook. If safe wallet reversal is not possible, the operational choice is to investigate or correct the top-up rather than refund silently.

## Test strategy

Use red-green TDD around observable payment and accounting contracts.

### Backend unit tests

- Exact yuan-to-fen parsing and rejection of invalid decimal forms.
- CNY-to-quota conversion at the configured exchange rate.
- Quota conversion rounding and int32 ceiling behavior.
- Create-order payload uses the exact AppID, merchant ID, CNY amount, notification URL, and unique order number.
- Definitive provider rejection marks a local order failed, while ambiguous create failures remain pending and reuse the same merchant order number.
- Callback signature/decryption dependencies are required before payload processing.
- Merchant ID, AppID, currency, and amount mismatches cannot settle.
- Successful settlement credits the stored quota.
- Duplicate callback and callback/query races credit only once.
- A provider transaction ID cannot settle two local orders.
- Status lookup enforces order ownership and never directly calls the provider.
- Persisted reconciliation claims bound provider queries across browser polling, restarts, and duplicate scheduler ticks.
- `NOTPAY` remains pending; only a successful close or authoritative `CLOSED` state produces `expired`.
- Payment-versus-close races resolve to exactly one terminal state without double credit.
- A validly signed but semantically conflicting callback records one incident and returns `204`; a transient incident-recording failure returns non-2xx.

Use `testify/require` for fatal setup assertions and `testify/assert` for non-fatal comparisons. Provider behavior is represented by deterministic fakes; tests must not call live payment APIs.

### Database compatibility

Exercise the GORM migration and settlement path without dialect-specific raw SQL. Preserve the shared `lockForUpdate` behavior so SQLite skips unsupported syntax while MySQL and PostgreSQL use row locking.

### Frontend tests

- CNY and international sections show the correct units and providers.
- Entering 50 creates a ¥50 order, never ¥365.
- QR dialog renders pending, success, failure, and expiry states.
- Polling stops on terminal state and component unmount.
- Successful payment refreshes balance and order history.
- Legacy unconfigured Epay entries are hidden.
- CNY requests preserve decimal text and do not pass through the existing floating-point international-payment amount hook.

### Production acceptance

WeChat Pay APIv3 does not provide a general-purpose sandbox that reproduces the complete production callback. After automated tests and deployment with the provider disabled by default:

1. Verify configuration readiness without exposing secret values.
2. Enable the provider for the administrator test account.
3. Create one explicitly confirmed small real CNY order.
4. Scan the Native QR code and complete payment.
5. Verify callback signature success, exact amount match, `pending → success`, one-time quota increase, cache synchronization, wallet display increase, and order history.
6. Replay or re-query the completed order and confirm no second credit occurs.
7. Simulate an ambiguous create timeout with the provider fake and confirm retrying the same idempotency key returns or reconciles the original order rather than creating a second payable order.
8. Verify an unpaid expired order is queried and closed before the local state becomes `expired`.
9. Execute and retain the initial transaction-bill reconciliation result.

The real financial transaction requires explicit user confirmation at the moment of payment.

## Rollout and rollback

- Default the provider to disabled.
- Deploy schema and code before mounting secrets or enabling the channel.
- Verify existing Waffo and Stripe top-up paths after deployment.
- Enable WeChat only when every readiness check passes.
- Keep a configuration flag that immediately hides the channel and rejects new orders without affecting already-created order records.
- Rollback disables new WeChat orders first, then restores the previous application image if necessary. Existing successful wallet credits are financial records and must never be reversed by deployment rollback.

## Out of scope

- Alipay implementation or credentials.
- Epay gateway onboarding or protocol changes.
- Recurring subscriptions.
- Refund APIs and automated wallet debit on refund; manual refunds remain governed by the reconciliation runbook above.
- A separate CNY-denominated stored-value ledger or a promise that total wallet display remains invariant when the global display rate changes.
- JSAPI, Mini Program, APP, H5, or combined payments.
- Changes to Waffo, Stripe, or Creem settlement semantics.
- Publication or production enablement without the corresponding implementation, secret configuration, deployment checks, and explicit real-payment confirmation.
