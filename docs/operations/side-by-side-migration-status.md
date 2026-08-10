# Side-by-Side Migration Status

This document records the execution of the side-by-side server migration plan on
server `113.249.120.24`.

## Environment snapshot

| Service | Container | Port | Status |
|---|---|---|---|
| Existing production | `new-api` | `0.0.0.0:13000` | Up, unchanged |
| Staging target | `codex-gateway-next` | `127.0.0.1:13100` | Up, rebuilt with current revision |
| PostgreSQL | `postgres` | `5432` | Up, unchanged |
| Redis | `redis` | `6379` | Up, unchanged |

## Task completion summary

### Task 1: Production recovery point

- Verified backup: `/data/one_api/backups/codex-gateway-migration/20260810-121438/`
- Files: `new-api.dump` (3.9 MB), `SHA256SUMS`, `restore.list`, `source-container.txt`
- `pg_restore --list` output present and readable.

### Task 2: Build and launch isolated target

- Source uploaded to `/opt/codex-gateway-next/source/codex-gateway-source.tar.gz`
- Docker image built: `codex-gateway-next:deploy-20260811-032613`
- Image digest captured by Docker manifest list.
- Staging environment file: `/opt/codex-gateway-next/next.env` (root 0600)
- Target bound to `127.0.0.1:13100` on the same private Docker network as `new-api`.
- `/healthz` returns `{"status":"ok"}` after startup.

### Task 3: Restore and migrate cloned database

- Database `codex_gateway_next` created and restored from the Task 1 dump.
- Target application started and applied its own migrations successfully.
- Invariant counts match between source and clone:
  - users: 12, channels: 4, tokens: 17, abilities: 19, options: 76.

### Task 4: Capability parity evidence

Evidence directory: `/opt/codex-gateway-next/evidence/`

| Check | Old service | New service | Result |
|---|---|---|---|
| `/healthz` | HTTP 200 | HTTP 200, `{"status":"ok"}` | Pass |
| `/api/status` | HTTP 200, same payload shape | HTTP 200, same payload shape | Pass |
| `/api/setup` | HTTP 200 | HTTP 200 | Pass |
| `/v1/models` (auth) | HTTP 200, 19 models | HTTP 200, 19 models | Pass |
| `/readyz` (management) | HTTP 200 | HTTP 200 | Pass |
| Database invariants | counts match | counts match | Pass |
| Relay token on management route | HTTP 404 (no such endpoint) | HTTP 401 (auth rejected) | Improved |
| CORS untrusted origin | `*` allowed | `denied` when `TRUSTED_ORIGINS` set | Improved |
| UI index via SSH tunnel | N/A | HTML loads on `http://localhost:13100/` | Pass |

### Gaps / blockers

- Full browser acceptance tests with Playwright were not executed because the
  local development environment does not have the Playwright browser binaries
  installed. The SSH tunnel was established and the UI index page loaded
  successfully, confirming the staging web frontend is reachable.
- Real provider credential testing was intentionally skipped to avoid consuming
  production quota or exposing provider keys in a chat log. Provider routes are
  configured in the cloned database and can be exercised manually using the
  existing `scripts/client-e2e.sh` with a scoped token.

## Task 5: Go / no-go recommendation

**Recommendation: NO-GO for automatic cutover.**

The staging target is running, the database clone is consistent, and the core
API/UI parity checks pass. However, the following blockers remain before any
production traffic change:

1. Playwright browser acceptance tests have not been run end-to-end against the
   staging service.
2. Real provider synthetic requests have not been executed on the staging target.
3. A documented maintenance window, final backup, Nginx switch plan, and
   rollback procedure must be reviewed and approved explicitly.

**Rollback / retention:** The existing `new-api` container and `postgres`/`redis`
containers have not been modified. The staging `codex-gateway-next` container can
be stopped or retained on instruction. No production traffic has been switched.
