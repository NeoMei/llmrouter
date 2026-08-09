# Incident response

## First 15 minutes

1. Assign incident commander, operations lead, communications lead, and scribe.
2. Record start time, release digest, affected regions/providers, symptoms, and request IDs.
3. Freeze deploys and destructive administration. Preserve logs, metrics, traces, database audit records, and object versions.
4. Check `/healthz` and `/readyz` on each node. Distinguish process failure from DB, Redis, object-store, or upstream failure.
5. Contain: remove unhealthy nodes, disable a failing channel, revoke compromised credentials, or roll back. Never delete evidence.

## Diagnosis

```bash
curl --silent --show-error "${NODE_URL}/healthz"
curl --silent --show-error "${NODE_URL}/readyz"
curl --silent --show-error "${NODE_URL}/metrics" > "incident-${INCIDENT_ID}-metrics.txt"
docker compose ps
docker compose logs --since=30m gateway > "incident-${INCIDENT_ID}-gateway.log"
```

Correlate by request ID. Compare authentication failures, relay attempts, upstream responses, quota reservations/refunds, and final billing records. If double billing is suspected, stop the affected relay path and preserve both the usage log and upstream invoice evidence before correction.

## Recovery and closure

Recover one canary, run login/refresh, relay, streaming, Files, and billing smoke checks, then restore traffic gradually. Monitor for at least one full alert window. The incident closes only after customer impact, data integrity, credential exposure, and billing reconciliation are known, the timeline is complete, and follow-up owners/dates are assigned.

For a security event, rotate scoped credentials in dependency order and retain forensic evidence according to policy. For data loss, follow `backup-restore.md`. For release regression, follow `rollback.md`.
