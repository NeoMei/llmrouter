# Production runbook

This runbook assumes an immutable image reference (`APP_IMAGE@APP_IMAGE_DIGEST`), PostgreSQL, authenticated Redis, and an S3-compatible object store. Never deploy from a floating tag.

## Preflight and deploy

1. Export the variables required by `scripts/release-preflight.sh`. Keep secrets in the deployment platform's secret store, not a checked-in `.env` file.
2. Verify the image digest and configuration:

```bash
docker pull "${APP_IMAGE}@${APP_IMAGE_DIGEST}"
docker image inspect "${APP_IMAGE}@${APP_IMAGE_DIGEST}" --format '{{json .RepoDigests}}'
bash scripts/release-preflight.sh
```

3. Back up the database and object-store inventory using `backup-restore.md`.
4. Start one canary, wait for readiness, then replace instances one at a time:

```bash
curl --fail --silent --show-error "${CANARY_URL}/healthz"
curl --fail --silent --show-error -H "Authorization: Bearer ${MANAGEMENT_TOKEN}" "${CANARY_URL}/readyz"
curl --fail --silent --show-error -H "Authorization: Bearer ${MANAGEMENT_TOKEN}" "${CANARY_URL}/metrics" > /tmp/codex-gateway-metrics.txt
```

5. Run authenticated smoke requests for `/v1/models`, one non-streaming request, one streaming request, Files, and any enabled Fine-tuning provider. Save request IDs with the release evidence.

For direct CNY wallet top-ups, follow [WeChat Pay Native operations](wechat-native-pay.md). Keep the channel disabled until its credential, callback, exact-settlement, and one-time-credit checks pass.

## Required alerts

- Page immediately: no healthy instances, readiness below 50%, database unavailable, authentication failure spike, billing invariant violation, or sustained 5xx above 5% for five minutes.
- Page within 15 minutes: p95 latency over the certified threshold for ten minutes, Redis/object-store errors, worker lag over two polling intervals, or storage above 85%.
- Ticket: storage above 70%, certificate/key expiry under 30 days, backup or restore rehearsal missed.

Capacity action is mandatory at 70% sustained CPU, 70% DB connections, 70% Redis memory, or 70% object-store quota. Scale stateless gateways first; do not increase worker concurrency until DB and upstream rate limits are checked.

## Routine checks

```bash
curl --fail --silent "${BASE_URL}/healthz"
curl --fail --silent -H "Authorization: Bearer ${MANAGEMENT_TOKEN}" "${BASE_URL}/readyz"
curl --fail --silent -H "Authorization: Bearer ${MANAGEMENT_TOKEN}" "${BASE_URL}/metrics" | grep -E 'http|relay|worker|object|redis|database'
```

Run `bash scripts/run-integration-matrix.sh --nightly` in the scheduled CI environment. The local command intentionally fails if Docker Compose is unavailable. Review `artifacts/nightly/fault-recovery.json`, `performance.json`, and Playwright traces.

## Stop conditions

Stop rollout and execute `rollback.md` if readiness fails, schema migration is incompatible, login/session refresh fails, billing differs from the pre-release sample, or error/latency budgets are breached. Do not continue a rollout while relying on manual retries.
