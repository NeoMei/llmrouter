# Managed Ali embedding preparation

The Ali `text-embedding-v4` and `qwen3-rerank` paths remain **not ready for
production activation**. Do not set `UNIHUB_ALI_EMBEDDING_ENABLED=true` or
`UNIHUB_ALI_RERANK_ENABLED=true` until authenticated result
recovery, host and background-worker integration, and a complete end-to-end
settlement/revocation review have been completed.

For an isolated integration environment, the explicit opt-in requires:

- The corresponding explicit opt-in and the existing platform settings.
- `UNIHUB_KNOWLEDGE_RESULT_KEYS`: JSON mapping of key version IDs to distinct,
  canonical base64-encoded 32-byte random keys. Supply through the deployment's
  secret mechanism; do not commit, print, or put these keys in examples.
- `UNIHUB_KNOWLEDGE_RESULT_ACTIVE_KEY`: one of those version IDs.
- A certified knowledge policy, explicit channel and supplier key fingerprint,
  matching published operation rate and model authorization. Rerank sends are
  restricted to the certified DashScope HTTPS endpoint.

All nodes must share the complete keyring. A database marker binds the active
writer generation, and registered versions cannot be silently replaced or
removed. Rotating keys requires coordinated shutdown of old writers, an explicit
generation transition, and retaining old keys for existing ciphertext and
backups; restarting with a different active version does not rotate it.

The request snapshot and captured result are encrypted and bound to the call's
identity and policy. Result payloads have an expiry, but encrypted input
snapshots currently follow call-record retention. Result recovery is separately
gated by `UNIHUB_KNOWLEDGE_RESULT_READ_ENABLED`, disabled by default, and requires
an enabled producer and versioned keyring. A task capability alone does not prove a current
logged-in user or account membership, and central authorization must also address
the interval between result authorization and a concurrent revocation.

## Settled result recovery retention

Captured output has a one-hour default expiry. The optional deployment setting
`UNIHUB_KNOWLEDGE_SETTLED_RESULT_RECOVERY_GRACE_SECONDS` accepts a canonical
integer from `0` through `82800` (23 hours); it defaults to `0`. With the current
one-hour capture TTL, the largest effective recovery window is 24 hours after
capture. This is an explicit retention policy for encrypted, already paid
results, not a change to an old row's expiry. Deploy it consistently across nodes.

Only the authenticated result-read path can use the additional window, and only
for a binding-v2 `SETTLED` result with intact ciphertext. It rechecks the current
actor and task credential, authoritative settlement and exact charge, original
attempt and financial AAD, and the unchanged active managed policy/route binding.
Its new receipt expires within 30 seconds and is clipped to the current actor,
credential and effective recovery deadline. Expired credentials or receipts do
not become valid. Original `expires_at`, ciphertext and AAD remain unchanged.
Legacy/internal capture reads retain their original expiry. UNKNOWN, missing
usage, unbound, conflicting or already cleared results cannot be recovered or
redispatched through this policy.

Bounded expiry cleanup retains a verified local paid-v2 result only while this
window remains open, after checking its AEAD, financial intent and original
attempt. Cleanup never contacts a provider or changes a call, hold or charge;
content delivery still requires the separate current central authority checks.
At the effective deadline, normal ciphertext cleanup applies. Enabling a grace
cannot restore ciphertext already removed. Cleanup scheduling remains separate.

This nonsecret setting changes the operator approval configuration fingerprint,
but does not alter the durable managed knowledge pin's policy/route fingerprint
or authorize a new activation. A deployment must read back its configured value
and existing bindings before accepting a recovered result.

## Certified catalog and identity origins

A valid knowledge certificate supplies the fixed `provider_reported_v1` usage
policy when no generic operation policy is configured. Its certified body/token
bounds and rate IDs remain authoritative. An explicitly configured different
policy is refused; an invalid certificate never enables the operation. Catalog
availability still requires account authorization before execution.

Index-read authorization compares the central verified product-access issuer
with `UNIHUB_PRODUCT_PUBLIC_ORIGIN`, the existing public identity origin. Missing
public-origin configuration retains the single-origin `UNIHUB_ORIGIN` fallback.
The separate billing/API `UNIHUB_ORIGIN` may retain a private port and is still
used for service requests. No request claim can select the expected issuer, and
an arbitrary issuer port is never removed to force a match. The public issuer
is validated as an HTTPS origin (HTTP is limited to loopback fixtures).

This identity field changes the operator approval configuration fingerprint.
It does not change an existing knowledge pin's durable policy/route/target/
channel/ability/group/principal fingerprint or mint replacement pins. Existing
activated pins must be verified through the actual resolver after deployment;
their original source-bound activation receipts remain historical evidence.


### Compatible embedding usage and safe diagnostics

Certified Ali embedding usage charges the positive integer `total_tokens` once.
An optional `prompt_tokens` must be the same canonical integer; an optional
`completion_tokens` must be the canonical integer zero. Unknown counters,
duplicates, contradictory aliases, nonzero completions and malformed numbers
remain invalid. This accepts compatible input aliases without estimating tokens
or summing the same input twice. The [Alibaba Cloud compatible response schema](https://www.alibabacloud.com/help/tc/model-studio/text-embedding-synchronous-api)
lists the input and total counters; the [mainland schema](https://help.aliyun.com/zh/model-studio/text-embedding-synchronous-api)
also documents the total-only variant. Documentation variants do not establish
which response a historical request actually returned.

Knowledge response failures expose only fixed `knowledge_result_*` stages for
request/attempt/policy binding, JSON/shape/counter validation, persistence and
settlement/readback. Supplier bodies, credentials and database error text are
excluded. A rejected response retains its existing unresolved call and held
reservation. No retry, release or historical-result recovery follows from a
parser change; without saved validated response evidence, an existing UNKNOWN
call remains unresolved.
