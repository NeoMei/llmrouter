# Standalone LLMRouter acceptance

Use this mode when LLMRouter must provide API keys, model access and local usage
billing without depending on UniHub availability.

## Required deployment settings

```dotenv
UNIHUB_PRODUCT_ENABLED=false
UNIHUB_PLATFORM_ENABLED=false
NATIVE_PUBLIC_ACCESS=true
```

All three values are required. Missing or malformed values fail closed at
startup. Do not expose the private native management listener through a public
proxy.

## One-key model access

1. Sign in through `/sign-in?native=1`.
2. Open `/keys` and create one API key.
3. Leave model limits empty. The saved token must have
   `model_limits_enabled=false`; this makes the key valid for every model that
   is enabled in the configured channels and abilities.
4. Keep the token quota finite when the deployment must enforce spending. An
   unlimited token records usage but cannot reject a request for insufficient
   quota.

Verify the public model list:

```bash
curl -fsS -H "Authorization: Bearer $LLMROUTER_KEY" \
  "$LLMROUTER_ORIGIN/v1/models"
```

The response must include every model required by the SuperWagie deployment.
An empty or incomplete list is a channel/ability configuration failure and must
be fixed before connecting SuperWagie.

## Usage and quota checks

Run one ordinary and one streaming request through the same key. Then verify:

- the provider received exactly one request per logical call;
- the token's `remain_quota` decreased and `used_quota` increased;
- `/api/usage/token/` reports the same token totals;
- `/api/log/self` contains the model, token counts and charge;
- a request after exhausting the token quota is rejected before provider dispatch.

Use the native top-up flow for local balance funding. UniHub recharge and
central settlement can be enabled later, but they are not prerequisites for
this standalone API path.

## SuperWagie connection

Configure SuperWagie with the same LLMRouter `/v1` origin and the single native
key. Keep the model IDs returned by `/v1/models`; do not copy upstream supplier
keys into SuperWagie settings.

This runbook proves the LLMRouter gateway and local accounting contract. It does
not certify supplier availability, SuperWagie's installed skill runtimes, or
real payment collection.

## Console state and credential boundaries

`/keys` manages native **LLMRouter product API keys**, including after UniHub
sign-in. It still writes the native `/api/token/` contract; it does not issue or
convert managed `uh_key` credentials. Budget, expiration and model access remain
visible. Batch quantity, routing groups, cross-group retry and IP restrictions
are retained under Advanced Settings, with the existing defaults preserved.

The wallet distinguishes a pending read, an unavailable read, a successful zero
and a failed refresh. Failed refreshes may show explicitly labeled previous
values. Financial controls are unavailable until the current wallet read
succeeds. Switching user, session or authentication realm clears the visible
wallet, its open confirmations and unfinished native-key drafts; responses from the previous identity cannot
populate the new identity's finance queries. Order History remains a separate
action, and history/statistics failures provide safe read retries instead of
claiming that records or usage are zero. Unresolved wallet funding limits even
administrators to personal order history until the funding source is confirmed.

A native account's local quota and a native key's `unified_cash` funding are
different sources. The shared cash display preserves exact CNY quantities and
does not identify it as the SuperWagie managed-call product wallet. `LR_QUOTA`
and native receipt values are product measurements; confirmed UniHub cash
charges belong to the financial receipt. A model/pricing directory entry does
not certify that a particular key, permission set or client supports the model.
Configured payment availability does not certify real collection.

For isolated UI regression (no backend required), run from `web/`:

```bash
bun x playwright test --config playwright.settings.config.ts
```

This uses the real app entry and components, intercepts all API requests, and
adds a separate test entry exposing the real auth boundary for controlled
account transitions. The production build does not import that test entry.
The fixture's token and accounts are synthetic and never call a real provider,
create a real key, or submit a real payment.
