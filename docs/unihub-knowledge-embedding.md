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
snapshots currently follow call-record retention. Result recovery is not yet
registered as an API route. A task capability alone does not prove a current
logged-in user or account membership, and central authorization must also address
the interval between result authorization and a concurrent revocation.
