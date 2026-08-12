# Side-by-Side Server Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy the hardened gateway beside the existing Quukk New API service on the same server, migrate a consistent production database clone, and prove capability parity without changing production traffic.

**Architecture:** Preserve the running `new-api` container and `new-api` PostgreSQL database. Create a timestamped backup, restore it into `codex_gateway_next`, build the current checkout into a versioned Docker image, and expose the new container only on `127.0.0.1:13100`. Compare database invariants, public/API behavior, provider routes, and rendered UI before any cutover decision.

**Tech Stack:** Docker 29, Docker Compose 5, PostgreSQL 15, Redis, Go, Bun/Vite, Playwright, Nginx.

## Global Constraints

- Do not stop, restart, rename, or modify the existing `new-api`, `postgres`, or `redis` containers during staging.
- Do not change Nginx, DNS, UFW, or public traffic before parity evidence is complete.
- Never print or copy database passwords, session secrets, channel keys, provider keys, or token values into logs or chat.
- Store generated deployment secrets only in a root-owned `0600` environment file.
- Bind the new service to `127.0.0.1:13100` only.
- Every destructive database action is limited to the new `codex_gateway_next` clone.

---

### Task 1: Create a consistent production recovery point

**Files:**
- Create remotely: `/data/one_api/backups/codex-gateway-migration/<timestamp>/new-api.dump`
- Create remotely: `/data/one_api/backups/codex-gateway-migration/<timestamp>/SHA256SUMS`

**Interfaces:**
- Consumes: running `postgres` container and its internal `POSTGRES_USER`/`POSTGRES_DB` variables.
- Produces: verified custom-format PostgreSQL dump and checksum.

- [x] Record current container image IDs, database size, table counts, and service health without reading secrets.
- [x] Run `pg_dump --format=custom` from inside the PostgreSQL container into the timestamped backup directory.
- [x] Generate and verify SHA-256 for the dump.
- [x] Run `pg_restore --list` to prove the dump is readable.

### Task 2: Build and launch the isolated target system

**Files:**
- Create remotely: `/opt/codex-gateway-next/source/`
- Create remotely: `/opt/codex-gateway-next/next.env`
- Create remotely: `/opt/codex-gateway-next/data/`
- Create remotely: `/opt/codex-gateway-next/logs/`

**Interfaces:**
- Consumes: current local Git revision and the existing Docker network.
- Produces: `codex-gateway-next:<git-sha>` and container `codex-gateway-next` on `127.0.0.1:13100`.

- [x] Package the tracked checkout while excluding `.git`, `.codebase-memory`, build caches, artifacts, and local secrets.
- [x] Upload and verify the source archive checksum.
- [x] Build the target Docker image and capture the image digest.
- [x] Generate root-only staging secrets and an environment file that points to the cloned database and Redis database 15.
- [x] Start the new container on the existing private Docker network with no public bind.
- [x] Poll `/healthz` and capture startup logs if initialization fails.

### Task 3: Restore and migrate the cloned database

**Files:**
- Modify only remotely: PostgreSQL database `codex_gateway_next`.

**Interfaces:**
- Consumes: Task 1 dump.
- Produces: automatically migrated target schema containing all legacy data.

- [x] Create `codex_gateway_next` with the same PostgreSQL owner as the source database.
- [x] Restore the verified custom-format dump.
- [x] Start the target application so its migration layer adds target schema changes.
- [x] Compare exact counts for users, channels, tokens, models, options, subscriptions, orders, quota data, and logs.
- [x] Check invalid indexes, orphaned foreign keys where applicable, and application migration errors.

### Task 4: Prove API, provider, and UI capability parity

**Files:**
- Create remotely: `/opt/codex-gateway-next/evidence/`
- Create locally: `artifacts/migration-parity/` only if evidence needs to be downloaded.

**Interfaces:**
- Consumes: old service at `127.0.0.1:13000`, new service at `127.0.0.1:13100`, cloned credentials, and repository test suites.
- Produces: machine-readable parity evidence and an explicit gap list.

- [x] Compare normalized `/api/status`, `/api/setup`, frontend entrypoint, static assets, and security headers.
- [x] Exercise authentication rejection, health, readiness authorization, model listing, token validation, and representative OpenAI-compatible routes.
- [x] Test each configured provider using synthetic low-token requests without printing credentials or response content.
- [x] Establish an SSH tunnel and run browser acceptance/UI interaction tests against the new service.
- [x] Run backend, web, CLI, Electron, release configuration, and migration-specific tests from the exact deployed revision.
- [x] Re-run database invariants after provider/API tests to detect unintended production-clone mutations.
- [x] Produce a parity matrix: covered, intentionally changed, blocked by missing credentials, or unsupported.

### Task 5: Decide cutover, with no implicit production change

**Files:**
- No production files are changed in this task without a separate explicit approval.

**Interfaces:**
- Consumes: Task 4 evidence.
- Produces: go/no-go recommendation and exact rollback/cutover commands.

- [x] Confirm all critical capabilities pass and list every remaining blocker.
- [x] If any critical capability is missing, leave production untouched and retain or remove staging on instruction.
- [x] If parity passes, present the downtime, final backup, Nginx switch, monitoring, and rollback procedure for explicit approval.
