# UniHub integration

LLMRouter serves its own `/unihub` interface and model routing; UniHub supplies identity, personal/team authorization, product subscriptions, recharge/wallets and the model ledger. The local operator console and pre-existing users/API keys keep their original identity/billing paths. No identity or balance is automatically migrated.

This integration is opt-in. The product account UI and trusted gateway have separate configuration switches; the checked-in example leaves both disabled. See [configuration example](unihub.env.example). No service token, client secret, password or provider key belongs in browser configuration or committed files.

## Identity and product registration

Register a UniHub product client for product `llmrouter`, exact client ID, exact public Origin and redirect URI ending `/unihub`. Use UniHub's `scripts/product_client.py prepare` / reviewed `apply` / `readback` procedure. These operations use the identity management secret only on the trusted operator host. This is a headless product client; no client secret is shipped to the product browser. `PRODUCT_CLIENTS_FILE` contains a JSON array of public client policies. One entry is:

```json
{
  "product_id":"llmrouter",
  "client_id":"llmrouter-web",
  "application":"app-llmrouter-web",
  "organization":"unihub",
  "redirect_uris":["https://llmrouter.example.com/unihub"],
  "origins":["https://llmrouter.example.com"],
  "scopes":["accounts:read","accounts:write","members:read","members:write","catalog:read","subscriptions:read","credits:read","wallets:read","recharges:read","recharges:write","operations:read","orders:read","orders:write","usage:read","usage:all-products","model-credentials:write"],
  "billing_products":["llmrouter"],
  "password_login":true,
  "registration":true
}
```

`usage:all-products` requires the new UniHub model-pool reporting implementation (integration commit `90a18fb` or a subsequent release containing it). Published UniHub v0.2.0 alone does not contain these additional endpoints. Its presence is optional for the UI: missing scope returns an explicitly labeled own-origin usage view. It does not affect ordinary product-origin isolation. Pool reports require both a registered llmrouter client scope and the account's current owner/billing_admin role; team admin does not imply finance authority.

Public config is `GET /api/unihub/config`. Password/S256 PKCE calls go directly to the fixed UniHub public URL. `POST /api/unihub/identity` receives only a product access bearer and verifies exact capabilities kind/product/client plus immutable user ID/accounts against the fixed server endpoint. It creates no local LLMRouter user, session, wallet or operator privilege. Local dashboard, product access, model grant, service token and supplier key remain distinct capabilities.

Product tokens and all product queries stay in memory. Reload/expiry/logout requires a new UniHub login; the current UniHub product protocol does not supply refresh tokens or server logout. Existing local dashboard cookies cannot restore product identity. Role changes are reauthorized by UniHub for every operation; explicit account refresh reloads current views. Select the payer before any money/model action.

## Funding profiles and rates

Publish the product's versioned profile and immutable offers in UniHub, not a second LLMRouter local subscription catalog:

- `hybrid`: paid membership with monthly CREDIT allowances and permitted addons; annual memberships grant monthly. Exhaustion stops new calls.
- `prepaid`: direct CNY debit from that account's llmrouter product wallet; no required membership.
- `subscription`: functional membership only; no model usage.
- Explicit combinations may enable both funding modes, but one call selects one asset. No silent CREDIT/CNY fallback or conversion.

Use exact `CREDIT/6` and `CNY/6` model rates with `exclusive_v1` usage semantics. Provider configuration/routing remains in LLMRouter; platform selling rates and final ledger charges come from UniHub. Existing LLMRouter native price settings continue serving legacy local traffic and are not a substitute for published UniHub rates. Pin rates per model/asset with `UNIHUB_MODEL_ASSET_RATE_IDS`; the older `UNIHUB_MODEL_RATE_IDS` pins CREDIT only. Reject an ambiguous or missing rate.

Create a dedicated enabled local routing principal with the configured fixed group, zero local quota and no operator authority. Give that group only approved models/channels. This principal routes traffic; it is never the payer. A source product cannot select a channel/group or submit its own account identity to the trusted gateway.

## Model calls and recovery

1. The source product uses its scoped UniHub user bearer and explicit personal/team account to issue a short-lived model/task grant. UniHub fixes actor, account, origin, models, task, funding mode, asset and profile version.
2. Send `POST /platform/v1/chat/completions` with that grant, stable `Idempotency-Key` (call ID), `X-UniHub-Task-ID`, explicit allowed model, text messages and positive `max_tokens`/`max_completion_tokens`.
3. LLMRouter verifies the grant with UniHub's trusted internal endpoint, persists the immutable payer/rate/funding snapshot, confirms reservation, dispatches to the supplier and settles its observed usage. Local user/token/subscription quota is never charged for platform traffic.
4. Read `GET /platform/v1/calls/{callID}` using an authorized task grant. Replaying a logical model POST returns a conflict rather than resending to the supplier. Unknown dispatch/settlement is retained for reconciliation; restarting replays only durable terminal intent, never the original model request.

The currently funded route supports ordinary/streaming text Chat Completions and function tools. Responses, image/audio/video/OCR, built-in web search and unsupported surcharge/opaque extensions are rejected here. Their existing native routes keep working for legacy credentials; they require separate usage-contract integration before UniHub funding. Task grants are short-lived and revocable, not durable API keys. Existing local keys cannot be transparently converted into platform financial authority.

`UNIHUB_MODEL_SERVICE_TOKEN` is a server-only independent secret matching UniHub's model-service configuration. Public reverse proxies intentionally block `/internal`; use loopback on a shared host or a separately protected private service network/TLS endpoint. Do not expose the trusted internal interface publicly or put this token into the SDK/browser.

## Recharge and status

The interface accepts exact decimal yuan and converts to canonical integer micro-units without JavaScript Number conversion. All finance values remain strings; CREDIT and CNY are displayed separately. Only owner/billing_admin sees finance amounts/actions. Creating a recharge/order does not prove payment or ledger posting. Read the original object/operation and show payment and fulfillment separately. `APPLIED` is the confirmed ledger operation state. Never automatically invoke sandbox settlement.

Financial intents preserve account/payload/idempotency key in the current tab across uncertain requests and account switches. Query or retry the original operation rather than making a new one. Reload/logout clears memory state, so keep the original object ID for later readback. Real merchant integration remains unavailable until UniHub explicitly configures and validates a real payment provider.

## Verification and rollout

Use isolated source/data for acceptance. The implementation report [local acceptance](unihub-validation-2026-10-25.md) separates real identity/database/ledger checks from controlled model responses and browser protocol fixtures. Relevant commands:

```sh
GOMAXPROCS=4 go test -p 4 ./internal/platformbilling ./internal/platformaccount ./service ./router -count=1
GOMAXPROCS=4 go test -race ./internal/platformbilling ./internal/platformaccount ./service/accountpool ./router -count=1
cd web
bun run test src/features/unihub/__tests__/*.test.ts
bun run typecheck
bun run build
# Browser fixtures: use the checked-in Playwright configuration and existing browser executable override if required.
bun x playwright test --config playwright.unihub.config.ts
```

Build real frontend assets before the root Go executable, which embeds `web/dist`. For actual UniHub HTTP/PostgreSQL/Formance gateway acceptance, follow UniHub's opt-in `TestComposableBillingActualGatewayAcceptance` instructions with this checkout explicitly selected.

Before production cutover: back up each database and ledger, deploy the required UniHub server update, register/readback the exact product client, publish approved profiles/offers/rates, configure the private service path, and accept normal/streaming calls plus role/account boundaries using designated test users. Then enable product/gateway switches. Prices and merchant/provider operation require their own approved configuration. Disabling switches prevents new platform traffic; keep the durable call database and original rates for pending reconciliation. Do not delete calls or turn unresolved charges into local fallback. Existing local account/balance migration requires explicit account binding, asset-aware reconciliation and an independently reviewable migration.
