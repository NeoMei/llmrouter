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
- `/usage/token` reports the same token totals;
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
