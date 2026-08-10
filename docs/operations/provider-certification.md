# Provider certification

Every capability declared as supported by the release source must pass against a real, authorized upstream credential before release. A missing credential, missing case, transport failure, response assertion failure, or HTTP 501 is a hard failure—not a skip.

## Trust boundary

Certification targets a dedicated gateway built from the release commit. Its public status response's `X-New-Api-Version` header must equal `CERTIFICATION_BUILD_VERSION`; the expected capability registry is generated directly from the checked-out release source. Use scoped test accounts, restricted gateway tokens, low quotas, synthetic prompts/files, and provider-side spend alerts. Do not run destructive cases against customer resources.

Credential values exist only in environment variables named by the case file. `certify-providers.sh` never writes request credentials or response bodies to the manifest. The committed case definition may name secret variables, but must not contain secret values.

In CI, `PROVIDER_CERTIFICATION_TOKENS_JSON_B64` decodes to a protected JSON object mapping each `token_env` reference to its scoped gateway token. This permits a distinct channel-pinned token per provider without placing dynamic secret names in the workflow.

## Case file

The protected JSON case file contains one case for every supported `(channel_type, protocol, operation)` tuple returned by `/api/capabilities`:

```json
{
  "cases": [
    {
      "channel_type": 1,
      "protocol": "openai",
      "operation": "chat.completions.create",
      "token_env": "CERT_OPENAI_CHANNEL_TOKEN",
      "method": "POST",
      "path": "/v1/chat/completions",
      "body": {
        "model": "certification-model",
        "messages": [{ "role": "user", "content": "Reply with OK" }],
        "max_tokens": 8
      },
      "response_jq": ".choices | length > 0"
    }
  ]
}
```

Use a channel-pinned gateway token when the same operation is supported by multiple channel types. Cases that create files, jobs, images, video, or models must include cleanup cases and use a unique release/run prefix.

## Run and sign

The signing key must be an Ed25519 private key held by the protected `production-certification` environment:

```bash
umask 077
openssl genpkey -algorithm ED25519 -out provider-certification-key.pem
export CERTIFICATION_GATEWAY_URL=https://certification-gateway.example.com
export CERTIFICATION_BUILD_VERSION="$(git rev-parse HEAD)"
export CERTIFICATION_CASES_FILE=/secure/provider-cases.json
export CERTIFICATION_SIGNING_KEY=/secure/provider-certification-key.pem
export CERTIFICATION_OUTPUT_DIR=artifacts/release
export CERT_OPENAI_CHANNEL_TOKEN='sk-redacted-channel-pin'
bash scripts/certify-providers.sh
bash scripts/certify-source.sh
```

Outputs are commit-bound `provider-certification.json`, detached signature, public key, and `source.json`. Verify without accessing secrets:

```bash
openssl pkeyutl -verify -pubin \
  -inkey artifacts/release/provider-certification.pub -rawin \
  -in artifacts/release/provider-certification.json \
  -sigfile artifacts/release/provider-certification.sig
```

## Review and retention

Two reviewers verify commit, registry hash, time, full supported tuple count, all-pass status, cleanup completion, provider account scope, and signature. Retain the manifest, signature, release image digest, SBOM/provenance, nightly evidence, restore rehearsal, and desktop checksums for the release retention period. Re-run after any adapter, route, registry, billing, retry, authentication, or dependency change.
