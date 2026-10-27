# Nonbreaking unified users repair

The user requires UniHub integration to preserve the complete original LLMRouter experience. This corrective contract supersedes the October 27 blanket-public-native-denial acceptance criteria. It retains UniHub identity and financial ownership; disabling integration is not a repair.

## Binding boundaries

- Existing native accounts, login, admin permissions, API keys, quota, payment flows, relay protocols and history continue unchanged when native compatibility is enabled alongside UniHub. `NATIVE_PUBLIC_ACCESS=true` permits the existing authenticated routes even when both UniHub flags are true. False continues to isolate the native surface. Normal route authentication and authorization remain mandatory.
- UniHub subjects map by verified issuer and immutable subject, never by matching username/email. A projected user cannot use native passwords, PATs, native API tokens, native wallet or numeric administrator privileges. Existing native identities, keys and balances are never automatically merged or rewritten.
- New unified users register/login through UniHub and receive product access by existing default-member/JIT rules. Product roles are assigned through UniHub user management; central user administrators, product administrators and payer finance roles remain separate authorities. Product settings remain in LLMRouter.
- Unified developer API keys are durable credentials, separate from short-lived task credentials. They bind actor, payer account, origin product, instance, allowed models, funding mode and asset. Secrets are returned once, hashed at rest and revocable. Revoked or expired keys cannot authenticate any public call, task/result or receipt read; service-authenticated settlement of already dispatched work and browser-authenticated history remain available. Creation/revocation and payer/role checks are server enforced.
- UniHub remains the sole financial authority for unified calls. Key limits and account/product/instance budgets constrain existing reserve/top-up/settle transactions; they do not grant money. Legacy calls retain legacy billing. A call cannot debit both ledgers.
- Standard API clients use the original `/v1` paths with durable unified keys. The server supplies per-call task identity and an idempotency ID when the client omits one; an explicit idempotency key must never dispatch the same logical call twice. Unsupported unified operations return a clear capability error and never fall back to local billing. Legacy keys retain all existing protocols.
- Technical monitoring reads durable platform calls/attempts using verified actor/account scope and includes identity, key, model, status, trusted usage, channel and latency. Central monetary quantities stay labeled with their actual asset; zero native quota is never presented as zero actual consumption. No local wallet/quota/user counters are mutated to populate unified charts.

## Additive protocol

`GET/POST /v1/accounts/{account}/api-keys`, `DELETE /v1/accounts/{account}/api-keys/{key}`. POST: `name`, `origin_product_id`, `instance_id`, `models`, optional `funding_mode`, `expires_at`, `max_total`, `max_per_call`. Amounts are decimal integer strings in the explicit asset's millionth units; empty limit is unlimited and zero disallows new holds. Key secret prefix is `uh_key_`. List never returns a secret.

`GET/PUT /v1/accounts/{account}/usage-budget?product_id=...&instance_id=...&funding_mode=...`. PUT is finance-role gated and version checked. The budget shares one account/product/instance/funding-mode scope. Responses expose `held`, `used`, optional `remaining`, limits, asset and version. Default absent budget is unlimited, subject to current payer membership, product access, funding policy and available funds.

Credential claims add `credential_kind` and `instance_id`. Internal verify and reserve receive the configured instance. Existing task credentials retain their exact task binding and expiry. Durable keys allow server-generated nonempty task IDs while preserving immutable actor/payer/product/instance scope. Product sessions derive product and instance from their verified client registry and reject conflicting caller fields.

## Required acceptance

1. Native operator login, user/token lifecycle, wallet/quota readback and existing key relay work with UniHub enabled; projection credentials cannot access those native financial/identity paths.
2. A newly registered unified member enters LLMRouter without duplicate registration; explicit product authorization changes take effect without native admin promotion.
3. Select payer, set a budget, create a model-scoped durable key, invoke a standard API path against a controlled supplier, verify exactly one central reserve/settle and no native debit, then read the same usage in monitoring.
4. Revoked/expired keys, disabled user/product access, lost payer membership, wrong product/instance/model and exhausted limits prevent supplier dispatch. Concurrent calls and duplicate requests cannot overspend or double-charge.
5. Account switching cannot show a previous payer's keys/budget; key secrets never persist in browser storage or logs. The original native history and settings survive.

Source tests, isolated full-chain acceptance, published artifacts and deployed production readback are separate evidence. Page rendering alone is insufficient.
