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
