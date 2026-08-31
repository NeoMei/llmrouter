# Native WeChat Pay Top-Up Design

## Goal

Add a first-party WeChat Pay APIv3 Native payment channel to LLMRouter so a signed-in user can enter a CNY amount, scan a WeChat QR code, and receive the same CNY value in wallet balance after a verified payment.

The approved user contract is exact and simple: entering `50` means paying `¥50.00` and increasing the displayed wallet balance by `¥50.00`. The new channel does not use the legacy Epay `amount × 7.3` payment calculation.

## Approved scope

The first release covers one-time wallet top-ups through WeChat Native payment:

- Create a CNY top-up order.
- Obtain a Native payment `code_url` from WeChat Pay.
- Render the payment QR code and poll local order state.
- Verify and decrypt WeChat Pay APIv3 callbacks.
- Validate payment identity, currency, and amount before settlement.
- Credit the wallet exactly once.
- Query WeChat Pay as a fallback when notification delivery is delayed.
- Close or expire unpaid orders after 15 minutes.

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
- `GET /api/user/wechat-pay/status`: return the owner-visible state and perform a throttled provider query fallback for eligible pending orders.
- `POST /api/wechat-pay/notify`: receive unauthenticated but cryptographically verified payment notifications.

The create and status endpoints require normal user authentication. The notification route is public and rejects any request that does not pass APIv3 signature verification and resource decryption.

### Model and settlement

Add provider and method constants such as `wechat_pay` and `wechat_native`. The WeChat completion path follows the existing safe transaction pattern:

1. Lock the `TopUp` row with `lockForUpdate(tx)`.
2. Require the expected provider and a `pending` or already-successful status.
3. Treat an already-successful order as an idempotent duplicate.
4. Credit the stored quota through `creditTopUpQuota`.
5. Save provider transaction metadata, completion time, and `success` status in the same transaction.
6. Synchronize the user quota cache only after commit.

Do not use the process-local order lock as the correctness boundary. Database locking and the conditional quota update remain authoritative across multiple application instances.

## Exact CNY accounting

Provider-facing amounts use integer fen. Never construct a payment amount with binary floating-point arithmetic.

The order request accepts a decimal CNY string, validates at most two fractional digits, and converts it to `paid_amount_minor` as an integer. The backend, not the browser, computes every authoritative value.

Add exact order snapshot fields to `TopUp`:

- `currency`: `CNY` for this provider.
- `paid_amount_minor`: the authorized payment amount in fen.
- `credited_quota`: the exact quota that will be added after settlement.
- `provider_trade_no`: the WeChat Pay transaction ID after it is known.

Create the columns through GORM-compatible migration code that works on SQLite, MySQL, and PostgreSQL. Existing rows and providers retain their current semantics.

For a CNY wallet display, compute the credited quota at order creation with decimal arithmetic:

`credited quota = CNY amount / USD exchange rate × quota per unit`

Use the centralized checked quota conversion helper and enforce the existing int32 wallet ceiling before calling WeChat Pay. Store the resulting integer quota on the order so exchange-rate or pricing changes after checkout cannot change settlement.

The first release intentionally does not apply the legacy Epay `Price`, top-up group multiplier, or preset discount to WeChat CNY orders. The entered CNY amount, charged CNY amount, and displayed wallet increase are equal. Any future CNY promotion must be represented explicitly rather than silently changing the meaning of the amount field.

The existing floating `money` column may remain populated for backward-compatible order display, but it is not authoritative for signature validation or settlement. Security decisions use `paid_amount_minor`, `currency`, and `credited_quota` only.

## Create-order flow

1. Validate that the WeChat provider is enabled and all required configuration is present.
2. Parse the requested CNY amount as decimal text and convert it to fen.
3. Enforce configurable minimum and maximum amounts, positive quota, and the user's remaining quota capacity.
4. Calculate and snapshot `credited_quota`.
5. Create a unique local merchant order number that satisfies WeChat's 6–32 character format.
6. Insert the local `pending` order before the provider call.
7. Call WeChat Native order creation with the configured AppID, merchant ID, description, CNY amount, notification URL, and 15-minute expiry.
8. On provider failure, mark the local order `failed` and return a non-secret error.
9. On success, return only the local order number, QR `code_url`, CNY amount, currency, and expiry time.

The browser never receives certificates, private keys, the APIv3 key, or decrypted provider payloads.

## Callback verification and settlement

The callback handler reads the raw request body once. It verifies `Wechatpay-Timestamp`, `Wechatpay-Nonce`, `Wechatpay-Serial`, and `Wechatpay-Signature` before decrypting the resource.

Before settlement it must require all of the following:

- Successful APIv3 signature verification.
- Successful AES-256-GCM resource decryption.
- Event type and transaction state representing a successful payment.
- Callback merchant ID equals `1295594101`.
- Callback AppID equals `wxa0b1bc308cdea2b2` when present in the transaction payload.
- Callback merchant order number resolves to an existing WeChat top-up owned by the expected user.
- Currency is exactly `CNY`.
- Callback `amount.total` exactly equals `paid_amount_minor`.

Only then may the model settlement transaction run. Duplicate valid notifications return a successful acknowledgement without adding quota again. Invalid signatures, decryption failures, identity mismatches, and amount mismatches never mutate order or wallet state and generate a security warning with request correlation data.

## Status, delayed notifications, and expiry

The frontend polls the authenticated status endpoint while the QR dialog is open. The endpoint returns only orders owned by the signed-in user.

For a pending order, the server may query WeChat Pay after a short delay and at a bounded frequency. A successful provider query goes through the same identity, currency, amount, and idempotent settlement checks as a callback. Client polling must not produce an unbounded stream of upstream queries.

After 15 minutes, an unpaid order is closed with WeChat when possible and marked `expired`. A late successful result is handled from authoritative provider state rather than blindly rejected: settlement remains possible only when WeChat confirms success and all immutable order fields match.

## Configuration and secret handling

Use server environment variables and read-only mounted files:

- `WECHATPAY_ENABLED`
- `WECHATPAY_MCH_ID`
- `WECHATPAY_APP_ID`
- `WECHATPAY_MCH_CERT_SERIAL`
- `WECHATPAY_MCH_PRIVATE_KEY_PATH`
- `WECHATPAY_API_V3_KEY`
- `WECHATPAY_PUBLIC_KEY_ID`
- `WECHATPAY_PUBLIC_KEY_PATH`
- Configurable minimum and maximum CNY top-up amounts

The callback origin is derived from the trusted server address, producing `https://llmrouter.quukk.com/api/wechat-pay/notify` in production.

Private-key files are mounted read-only with restrictive filesystem permissions. Secret values are never included in database option rows, frontend settings payloads, error responses, or logs. Configuration readiness gates both API availability and frontend visibility. Secret rotation takes effect through a controlled application restart.

## Wallet user experience

Split recharge choices by currency:

- **CNY payment**: amounts display in `¥`; suggested values are ¥50, ¥100, ¥200, and ¥500; the first release shows WeChat Pay only.
- **International payment**: existing Waffo and Stripe behavior remains unchanged and continues to display its actual pricing currency.

Hide the current legacy Epay Alipay and WeChat entries while Epay credentials are absent. They are display configuration only and must not imply that a working payment rail exists.

Selecting WeChat opens a QR dialog containing the exact CNY amount, QR code, local order number, 15-minute countdown, and current state. Success closes the dialog and refreshes wallet balance and order history. Closing the dialog does not cancel or settle the order; reopening order history shows its authoritative state.

When the future official Alipay provider is available, it joins the CNY section and reuses the same amount semantics without changing the WeChat implementation.

## Error handling and observability

- Reject malformed, fractional-overprecision, below-minimum, above-maximum, or quota-overflow amounts before creating an order.
- Return a stable user-facing error when provider configuration is incomplete.
- Mark local orders failed when Native order creation fails after insertion.
- Preserve pending state on transient query failures and allow callback retries.
- Record the WeChat response `Request-ID`, local merchant order number, provider transaction ID, state transition, and high-level error code.
- Never log authorization headers, signatures in full, private keys, APIv3 keys, or complete decrypted callback bodies.
- Emit warnings for signature probes, amount mismatches, identity mismatches, unknown orders, repeated query failures, and quota saturation.

## Test strategy

Use red-green TDD around observable payment and accounting contracts.

### Backend unit tests

- Exact yuan-to-fen parsing and rejection of invalid decimal forms.
- CNY-to-quota conversion at the configured exchange rate.
- Quota conversion rounding and int32 ceiling behavior.
- Create-order payload uses the exact AppID, merchant ID, CNY amount, notification URL, and unique order number.
- Provider failure marks a local order failed.
- Callback signature/decryption dependencies are required before payload processing.
- Merchant ID, AppID, currency, and amount mismatches cannot settle.
- Successful settlement credits the stored quota.
- Duplicate callback and callback/query races credit only once.
- Status lookup enforces order ownership and throttles provider queries.
- Expired orders close without changing wallet balance.

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

### Production acceptance

WeChat Pay APIv3 does not provide a general-purpose sandbox that reproduces the complete production callback. After automated tests and deployment with the provider disabled by default:

1. Verify configuration readiness without exposing secret values.
2. Enable the provider for the administrator test account.
3. Create one explicitly confirmed small real CNY order.
4. Scan the Native QR code and complete payment.
5. Verify callback signature success, exact amount match, `pending → success`, one-time quota increase, cache synchronization, wallet display increase, and order history.
6. Replay or re-query the completed order and confirm no second credit occurs.

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
- Refund APIs and automated wallet debit on refund.
- JSAPI, Mini Program, APP, H5, or combined payments.
- Changes to Waffo, Stripe, or Creem settlement semantics.
- Publication or production enablement without the corresponding implementation, secret configuration, deployment checks, and explicit real-payment confirmation.
