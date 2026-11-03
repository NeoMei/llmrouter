# Original LLMRouter console with UniHub identity and funds

## Confirmed product contract

UniHub users must see the original LLMRouter interface, navigation, tables,
drawers, filters and operation sequence immediately after login.

The user's latest clarification supersedes the earlier central-owned Key plan:

- LLMRouter owns model aggregation, channels, routing, protocol conversion,
  product API Key issuance/management, pricing and usage fee calculation.
- UniHub owns unified identity/authentication, unified money accounts and the
  general ledger. LLMRouter submits its calculated reserve, settlement, debit
  and refund amounts to the ledger through the product runtime.
- A UniHub login must not select a different product console, Key model, relay
  implementation or pricing engine.

## Architecture

Keep the original authenticated shell and page components for all identities.
Remove whole-page ProductConsoleView branches in overview, keys, wallet and
common logs. Reuse the original product API contracts, with existing runtime
adapters for central identity, authorization and financial posting.

Existing product runtime source already contains LLMRouter Token CRUD and
original relay/pricing feeding ProductFundingStore. Verify these paths before
adding abstractions. A projected quota is a display/compatibility value, never
an independent spendable wallet. Missing or uncertain central authority fails
closed. Register product routes individually; do not rely on enabling all native
public APIs. Central product usage logs must reflect actual product runtime
calls while preserving identity/funding realm boundaries.

## Identity and authority

Keep explicit identity mappings and product role/capability checks. Never merge
by username/email, inherit same-name local root, or fall back to local login
after a central failure. Password and MFA remain central; product preferences
retain LLMRouter storage. Preserve the original profile layout. Compact central
controls may be embedded without weakening UniHub whole-site frame restrictions.

## API Keys and historical data

The original Key table, policy form, status, copy/export, search, pagination and
batch operations call /api/token and retain LLMRouter's existing contract. Real
product Token rows belong to LLMRouter and are not surrogates for central keys.

Preserve existing uh_key_ credentials, their managed gateway and financial
history. Do not silently convert, revoke, delete or reissue them. Any legacy
compatibility view is subordinate to the original default product UI. The prior
one-time-secret/rotation choice concerned hash-only UniHub credentials and does
not authorize changing existing LLMRouter secret storage semantics.

## Financial contract

LLMRouter pricing determines each product call's charge. Preserve reserve →
dispatch → settlement/refund, durable intent and idempotent recovery. Only the
UniHub ledger spends unified funds; never double charge a local balance or
apply a second model price. Financial recovery never replays supplier requests.

Wallet retains its original composition and operations. Product top-up,
redemption, subscription and refund adapters must post through central funding.
Native-only identities/keys/balances remain independent pending an explicitly
reviewed migration. No migration is authorized by this UI restoration.

The user explicitly selected direct shared CNY funds. New calls in
UNIHUB_PRODUCT_FUNDING_MODE=unified_cash submit CNY/6 with lr-cny-v1.
The product freezes exact QuotaPerUnit and USDExchangeRate conversion as a
rational amount, with cumulative-ceiling rounding and immutable correction
segments. Existing LR_QUOTA/0 calls and receipts finish in their original asset.
No existing balance is migrated. General grants and legacy rewards remain LR
quota; they cannot issue shared cash. Verified new CNY payment receipts use a
separate cash:payment service capability and credit actual paid fen × 10000.
Cash spending requires the explicit cash:charge service capability. Exact cash
amounts are separate from product quota in the original cards and order history.

## Acceptance

1. Central direct entry to overview/keys/wallet/common logs shows original
   controls without selecting Local or a second console.
2. Central and native users use original product Key endpoints and policy form.
3. Runtime tests prove LR-calculated reserve/settlement, insufficient funds,
   refund and idempotency without duplicate local debit. Wallet readback is
   authoritative and asset-correct.
4. Original product Key usage appears in scoped original logs/statistics.
5. Native-only identities and legacy uh_key_ calls remain compatible. Denied
   access or central failure never widens authority or changes financial realm.
6. Browser/API checks, builds, deployments, production readback, real supplier
   and financial acceptance, and user confirmation remain separate evidence.
