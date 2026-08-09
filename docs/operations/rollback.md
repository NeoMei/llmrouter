# Rollback

Rollback changes application binaries/configuration, not blindly the database. Determine whether the release ran a backward-incompatible migration before changing traffic.

## Procedure

1. Freeze the rollout and record current and target image digests.
2. Confirm the previous image's schema compatibility. If uncertain, stop writes and restore into an isolated environment first.
3. Deploy the previous immutable digest to one canary:

```bash
docker pull "${APP_IMAGE}@${PREVIOUS_IMAGE_DIGEST}"
APP_IMAGE_DIGEST="${PREVIOUS_IMAGE_DIGEST}" docker compose up -d --no-deps gateway
curl --fail --silent --show-error "${CANARY_URL}/healthz"
curl --fail --silent --show-error "${CANARY_URL}/readyz"
```

4. Verify login/refresh, `/v1/models`, non-streaming and streaming relay, Files, worker progress, and a billing sample.
5. Replace remaining instances one at a time and monitor the full alert window.

## Data rollback

Never run a down migration or restore over production merely to match an old binary. Stop writes, create a fresh snapshot, and follow `backup-restore.md`. Reconcile object versions and database records together. If credentials cannot be decrypted with the restored `GATEWAY_AES_KEY`, stop and restore the matching secret version.

## Completion criteria

All nodes report the intended digest, readiness is stable, error/latency rates return to baseline, workers are progressing once, billing reconciliation passes, and the incident/release record links the rollback evidence.
