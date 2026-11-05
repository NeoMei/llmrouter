# Managed text input reservation

`UNIHUB_TEXT_INPUT_POLICIES` opts an explicitly certified model and wire into
`full_body_bytes_v1`. Unconfigured models keep the existing input limit and
full-limit reservation. Image/document input, opaque provider history and other
wire formats retain their existing reservation contracts.

For a configured plain-text Chat or Responses request, including system text,
history, tools, schemas and metadata, the input reservation is
`B = 2 * len(admitted UTF-8 JSON body)`. The technical limit is
`H = min(max_input_tokens, context_tokens - requested_output_tokens)`. Admission
rejects `B > H`; it never clamps B and sends the larger request. Requested output
must also fit the policy and deployment output limits. Durable API-key requests
that omit output limits receive the policy/deployment minimum; original JSON
bytes are preserved when adding that field, and the added bytes count too.

This is the existing conservative byte bound, not a provider tokenizer. The
policy freezes its version, input/output quantities and route certificate in
`platform_model_calls.text_input_snapshot`, alongside reservation quantities.
The monetary hold uses B with the existing tariff. No key/account cap, financial
profile, model alias or rate changes. A confirmed provider input total (including
cache dimensions) above B cannot settle: the call stays UNKNOWN. The same applies
to output totals above the frozen output limit.

The certificate binds the exact channel ID/type, upstream model and final HTTPS
endpoint. Both the selected adapter and final HTTP request are checked, including
method/Host and TLS verification. Existing route selection and group/ability
checks remain in force; a different selected route fails closed, without fallback
to a larger limit. Only enable a policy after checking every selectable route.

## Configuration

The environment value is strict JSON keyed by wire and then public model:

```json
{
  "chat.completions.create": {
    "deepseek-v4-flash": {
      "version": "full_body_bytes_v1",
      "channel_id": 3,
      "channel_type": 43,
      "upstream_model": "deepseek-v4-flash",
      "endpoint": "https://api.deepseek.com/v1/chat/completions",
      "max_input_tokens": 262144,
      "context_tokens": 1048576,
      "max_output_tokens": 4096,
      "provider_reference": "https://api-docs.deepseek.com/api/list-models/"
    }
  }
}
```

This example's route identifiers require deployment-specific verification. The
[official model metadata](https://api-docs.deepseek.com/api/list-models/) defines
context as input plus output, and documents 1048576 for DeepSeek Flash and V4 Pro.
The deployment safety cap above deliberately remains 262144 and output 4096.
The old flash alias must be verified against the selected provider route before
using that certificate. A 99750-byte request reserves 199500 input units; a small
request reserves its own B, not the full 262144.

Set `UNIHUB_MAX_BODY_BYTES=1048576` to admit the larger transport body, retain
`UNIHUB_MAX_INPUT_TOKENS=65536` for unconfigured models, and retain output 4096.
Both transport and input checks must pass. A transport rejection is HTTP 413
with `error.code=platform_request_body_too_large`, `stage=pre_admission` and
`max_body_bytes`; it creates no local call, reserve or provider attempt. A body
read failure is a separate HTTP 400 `platform_request_body_read_failed`.

## Replay and rollback

Request digests retain their existing format. Duplicate calls compare their
original immutable identity and request, then return the existing-call result;
they do not recompute the saved amount or reserve again. Policy-bound metered
Chat replay occurs before evaluating mutable input policies, as Responses
already does. Legacy Chat also checks existing calls before the new input bound.
API-key retries without an explicit output limit restore the saved output and
the original normalization format before comparing the request. Historical
legacy rows with no saved output still rely on the original deployment default;
this rollout therefore retains the existing output default of 4096.

The migration adds one nullable TEXT column supported by SQLite, MySQL and
PostgreSQL. Empty snapshots retain old semantics. Recovery still sends only
saved settle/release intents and never resends UNKNOWN provider requests. Keep
the same database, service identity and knowledge-result keyring across upgrades
or rollback. A rollback must not delete the new column or outstanding calls.

## Reserve errors

Only a reserve HTTP 402 whose entire JSON object is exactly
`{"error":"insufficient_funds"}` or `{"error":"insufficient_credits"}` is a
definitive rejection. It records REJECTED and task Chat returns
`platform_insufficient_funds`. Malformed/ambiguous 402, transport uncertainty or
mismatched success receipts remain RESERVE_UNKNOWN; they never authorize a
provider dispatch. Durable-key errors retain their existing unified budget code.
