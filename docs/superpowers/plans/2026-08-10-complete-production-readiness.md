# Complete Production Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every public gateway route deterministic and production-ready, reject impossible provider operations before billing, and ship reproducible backend, web, CLI, container, and Electron artifacts.

**Architecture:** Delivery is split into five independently testable vertical batches. A central capability registry is evaluated before channel selection; durable Files and Fine-tuning resources use tenant-owned database records plus an `ObjectStore`; release gates exercise all supported databases, Redis, S3-compatible storage, browsers, containers, and desktop packages.

**Tech Stack:** Go 1.25.1, Gin, GORM, SQLite/MySQL/PostgreSQL, Redis, S3-compatible object storage, React/TypeScript/Rsbuild, Node test runner, Electron, Docker Compose, GitHub Actions.

## Global Constraints

- Every documented public route has stable request, response, authorization, and error behavior.
- No reachable production path returns placeholder `not implemented` behavior.
- Unsupported provider operations fail before channel selection, pre-consume, cooldown, automatic disablement, or adapter execution.
- Public errors use the repository OpenAI-compatible envelope and the exact codes `unsupported_operation`, `resource_not_found`, `invalid_resource_state`, `file_too_large`, `invalid_file`, `upstream_error`, and `no_capable_channel`.
- SQLite, MySQL, and PostgreSQL remain supported; Redis and S3-compatible storage are mandatory for multi-instance production.
- Production artifacts use the current revision through immutable versions and digests, never an unrelated `latest` image.
- Missing live provider credentials block production certification without weakening simulated provider contracts.
- Existing unrelated worktree content, including `.codebase-memory/`, is preserved.

---

## File Map and Stable Interfaces

The implementation is governed by these focused units:

- `relay/capability/operation.go`: protocol and operation enums plus request-path normalization.
- `relay/capability/registry.go`: immutable `ChannelType × Protocol × Operation` registry.
- `relay/capability/defaults.go`: built-in channel-type declarations.
- `relay/capability/registry_test.go`: uniqueness, completeness, and route consistency.
- `middleware/capability.go`: request capability resolution before distribution.
- `service/objectstore/store.go`: storage-neutral interface.
- `service/objectstore/local.go`: traversal-safe persistent local implementation.
- `service/objectstore/s3.go`: S3-compatible implementation.
- `model/api_file.go`: tenant-owned file metadata repository.
- `controller/file.go`: Files API handlers.
- `service/fine_tuning.go`: fine-tuning provider contract and orchestration.
- `model/fine_tuning.go`: jobs/events repository and state transitions.
- `controller/fine_tuning.go`: fine-tuning and model-deletion handlers.
- `pkg/readiness/readiness.go`: dependency readiness probes.
- `web/src/lib/security/iframe-policy.ts`: shared iframe URL/sandbox/permission policy.
- `web/src/features/capabilities/`: typed capability query and UI guards.
- `scripts/release-preflight.sh`: required configuration and migration preflight.
- `scripts/backup.sh`, `scripts/restore.sh`, `scripts/rollback.sh`: operator lifecycle commands.

Stable backend interfaces:

```go
package capability

type Protocol string
type Operation string

type Key struct {
    ChannelType int
    Protocol    Protocol
    Operation   Operation
}

type Decision struct {
    Supported bool   `json:"supported"`
    Reason    string `json:"reason,omitempty"`
}

type Registry interface {
    Resolve(channelType int, protocol Protocol, operation Operation) Decision
    Supports(channelType int, protocol Protocol, operation Operation) bool
    Snapshot() map[Key]Decision
    Ready() error
}

func ResolveRequest(method, path string) (Protocol, Operation, error)
```

```go
package objectstore

type PutOptions struct {
    ContentType string
    Size        int64
    ChecksumSHA256 string
}

type ObjectInfo struct {
    Key, ContentType, ETag string
    Size int64
}

type Store interface {
    Put(ctx context.Context, key string, body io.Reader, opts PutOptions) (ObjectInfo, error)
    Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
    Delete(ctx context.Context, key string) error
    Health(ctx context.Context) error
}
```

```go
package service

type FineTuningProvider interface {
    Create(ctx context.Context, channel *model.Channel, req dto.FineTuningCreateRequest) (FineTuningUpstreamJob, error)
    Retrieve(ctx context.Context, channel *model.Channel, upstreamID string) (FineTuningUpstreamJob, error)
    Cancel(ctx context.Context, channel *model.Channel, upstreamID string) (FineTuningUpstreamJob, error)
    ListEvents(ctx context.Context, channel *model.Channel, upstreamID, after string, limit int) ([]FineTuningUpstreamEvent, error)
    DeleteModel(ctx context.Context, channel *model.Channel, modelID string) error
}
```

## Batch 1: Release Foundations and Backend Correctness

### Task 1: Establish deterministic local release commands

**Files:**
- Create: `scripts/verify-backend.sh`
- Create: `scripts/verify-web.sh`
- Create: `scripts/verify-cli.sh`
- Modify: `web/package.json`
- Modify: `cli/package.json`
- Test: `scripts/release_scripts_test.sh`

**Interfaces:**
- Produces: `npm test` in `web/` and `cli/`, and shell entrypoints used unchanged by CI.

- [x] **Step 1: Write the failing script contract test**

```bash
#!/usr/bin/env bash
set -euo pipefail
grep -q '"test"' web/package.json
grep -q '"test"' cli/package.json
test -x scripts/verify-backend.sh
test -x scripts/verify-web.sh
test -x scripts/verify-cli.sh
```

- [x] **Step 2: Run the test and verify it fails**

Run: `bash scripts/release_scripts_test.sh`

Expected: non-zero because the committed scripts/test commands do not yet exist.

- [x] **Step 3: Add deterministic verification commands**

`verify-backend.sh` runs `gofmt -l`, `go test ./...`, `go vet ./...`, `go test -race ./...`, and `go build -trimpath ./...`. `verify-web.sh` runs `npm run format:check`, `npm run copyright:check`, `npm run lint`, `npm run build:check`, `npm test`, `npm run build`, and `npm run knip`. `verify-cli.sh` runs `npm test` and `npm run build`. Add `"test": "tsx --test 'src/**/*.test.ts'"` to both packages, adding `tsx` as a pinned dev dependency where absent.

- [x] **Step 4: Run the contract and package tests**

Run: `bash scripts/release_scripts_test.sh && (cd web && npm test) && (cd cli && npm test)`

Expected: PASS, including the existing 80 web tests.

- [x] **Step 5: Commit**

```bash
git add scripts web/package.json web/package-lock.json cli/package.json cli/package-lock.json
git commit -m "build: add deterministic verification commands"
```

### Task 2: Remove backend vet and race failures

**Files:**
- Modify: `logger/logger.go`
- Test: `logger/logger_test.go`
- Modify: `common/custom-event.go`
- Test: `common/custom-event_test.go`
- Modify: `common/email_test.go`
- Modify: `relay/channel/api_request_test.go`
- Modify: the twelve adapter files reported by `go vet ./...`
- Modify: `model/task.go`
- Modify: `service/task_polling.go`
- Test: `model/task_race_test.go`

**Interfaces:**
- Produces: `logger.rotateIfNeeded()`, `(*Task).Snapshot() Task`, and no value-copy API for `CustomEvent`.

- [x] **Step 1: Add race-focused tests**

```go
func TestConcurrentLoggingDoesNotRace(t *testing.T) {
    t.Parallel()
    var wg sync.WaitGroup
    for i := 0; i < 32; i++ {
        wg.Add(1)
        go func() { defer wg.Done(); LogInfo("race probe") }()
    }
    wg.Wait()
}

func TestTaskSnapshotIsIndependent(t *testing.T) {
    task := &Task{TaskId: "a", Status: TaskStatusInProgress}
    snapshot := task.Snapshot()
    task.Status = TaskStatusSuccess
    if snapshot.Status != TaskStatusInProgress { t.Fatal("snapshot mutated") }
}
```

- [x] **Step 2: Reproduce the failures**

Run: `go vet ./... && go test -race ./logger ./model ./service ./relay/channel`

Expected: vet reports unreachable code/value-copy/IPv6 issues or race reports logger/task/Gin global state.

- [x] **Step 3: Implement synchronization and snapshot isolation**

Guard logger rotation state with one package-level mutex; keep log emission outside the lock. Change `CustomEvent` helpers to accept pointers. Replace `fmt.Sprintf("%s:%d", host, port)` with `net.JoinHostPort`. Set Gin mode once from `TestMain`. Make polling workers pass `task.Snapshot()` into goroutines and update rows with `map[string]any`/ID-scoped GORM statements so no update mutates a shared task struct. Delete statements after unconditional returns in the twelve adapters.

- [x] **Step 4: Prove vet and race cleanliness**

Run: `go vet ./... && go test -race ./...`

Expected: exit 0 with no race report.

- [x] **Step 5: Commit**

```bash
git add logger common model service relay
git commit -m "fix: eliminate backend vet and race failures"
```

### Task 3: Correct health, readiness, secrets, and rate limiting

**Files:**
- Modify: `pkg/health/health.go`
- Test: `pkg/health/health_test.go`
- Create: `pkg/readiness/readiness.go`
- Test: `pkg/readiness/readiness_test.go`
- Modify: `router/main.go`
- Modify: `middleware/distributed_rate_limit.go`
- Test: `middleware/distributed_rate_limit_test.go`
- Create: `common/production_config.go`
- Test: `common/production_config_test.go`

**Interfaces:**
- Produces: `readiness.Checker.Check(context.Context) error` and `common.ValidateProductionConfig() error`.

- [x] **Step 1: Add failing liveness/readiness/config tests**

```go
func TestHealthzIgnoresDependencyLoss(t *testing.T) { /* db probe fails; expect 200 */ }
func TestReadyzFailsWhenObjectStoreFails(t *testing.T) { /* expect 503 object_store_error */ }
func TestProductionConfigRequiresStableSecrets(t *testing.T) { /* unset SESSION_SECRET; expect named error */ }
```

- [x] **Step 2: Run focused tests**

Run: `go test ./pkg/health ./pkg/readiness ./common ./middleware`

Expected: FAIL because readiness/config APIs do not exist and health currently probes dependencies.

- [x] **Step 3: Implement the probes and production startup validation**

Make `/healthz` return process liveness only. Make `/readyz` probe DB, configured Redis, object store, migration version, and capability registry with a two-second aggregate timeout and stable component status. If `RUN_MODE=production`, require non-default `SESSION_SECRET`, `GATEWAY_AES_KEY`, DB credentials, Redis credentials when configured, object-store credentials for S3, exact trusted origins, secure cookies, and trusted proxy ranges. Attach `RateLimitMiddleware` automatically to authenticated relay groups when Redis/multi-instance mode is enabled.

- [x] **Step 4: Run focused and full backend checks**

Run: `go test ./pkg/health ./pkg/readiness ./common ./middleware && go test ./...`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add pkg common router middleware
git commit -m "feat: separate liveness from production readiness"
```

### Task 4: Fix versioning, immutable deployment, and operator lifecycle

**Files:**
- Modify: `.github/workflows/release.yml`
- Create: `.github/workflows/ci.yml`
- Modify: `docker-compose.yml`
- Create: `docker-compose.test.yml`
- Create: `.env.production.example`
- Create: `scripts/release-preflight.sh`
- Create: `scripts/backup.sh`
- Create: `scripts/restore.sh`
- Create: `scripts/rollback.sh`
- Test: `scripts/release_config_test.sh`
- Modify: `VERSION`

**Interfaces:**
- Produces: `APP_IMAGE`, `APP_IMAGE_DIGEST`, and operator commands with `--check` dry-run mode.

- [x] **Step 1: Write static release configuration tests**

```bash
grep -q 'github.com/QuantumNous/new-api/common.Version' .github/workflows/release.yml
! grep -q 'calciumion/new-api:latest' docker-compose.yml
grep -q '${APP_IMAGE:?APP_IMAGE is required}@${APP_IMAGE_DIGEST:?APP_IMAGE_DIGEST is required}' docker-compose.yml
```

- [x] **Step 2: Run and verify failure**

Run: `bash scripts/release_config_test.sh`

Expected: FAIL on the wrong linker symbol/current `latest` compose image.

- [x] **Step 3: Implement reproducible release/deployment files**

Set `VERSION` to the next release candidate, inject `-X github.com/QuantumNous/new-api/common.Version=${VERSION}`, build the current revision, publish digest-pinned images, checksums, SBOM/provenance/signatures, and make Compose require image/digest plus all production secrets. Preflight verifies required values, connectivity, migration status, backup destination, and rollback image. Backup/restore/rollback support SQLite, MySQL, and PostgreSQL with explicit DB-type branches and refuse destructive restore without `--confirm`.

- [x] **Step 4: Validate static configuration and local image marker**

Run: `bash scripts/release_config_test.sh && bash scripts/release-preflight.sh --check && go build -ldflags "-X github.com/QuantumNous/new-api/common.Version=$(cat VERSION)" -o /tmp/codex-gateway . && strings /tmp/codex-gateway | grep -F "$(cat VERSION)"`

Expected: all commands exit 0.

- [x] **Step 5: Commit**

```bash
git add .github docker-compose.yml docker-compose.test.yml .env.production.example scripts VERSION
git commit -m "build: make releases immutable and operationally reversible"
```

## Batch 2: Capability Registry and Existing Protocols

### Task 5: Implement the immutable capability registry

**Files:**
- Create: `relay/capability/operation.go`
- Create: `relay/capability/registry.go`
- Create: `relay/capability/defaults.go`
- Test: `relay/capability/registry_test.go`
- Modify: `constant/channel.go`

**Interfaces:**
- Produces: the `capability.Registry` interface defined above and `capability.DefaultRegistry()`.

- [x] **Step 1: Add table-driven path and registry tests**

```go
func TestResolveRequest(t *testing.T) {
    cases := []struct{ method, path string; protocol Protocol; operation Operation }{
        {"POST", "/v1/chat/completions", ProtocolOpenAI, OperationChatCreate},
        {"POST", "/v1/files", ProtocolOpenAI, OperationFileCreate},
        {"GET", "/v1/fine_tuning/jobs/job-1/events", ProtocolOpenAI, OperationFineTuningEventsList},
        {"DELETE", "/v1/models/model-1", ProtocolOpenAI, OperationModelDelete},
    }
    /* assert exact resolution */
}
```

- [x] **Step 2: Run tests and verify missing package failure**

Run: `go test ./relay/capability`

Expected: FAIL because the package is absent.

- [x] **Step 3: Implement enums, path resolution, defaults, and readiness**

Declare every registered public relay route as an operation. Reject duplicate keys during registry construction. Populate defaults for every channel type; unsupported entries are explicit `Decision{Supported:false, Reason:"upstream does not expose ..."}` rather than absent. `Ready` fails on duplicates, missing known channel types, or missing public operations.

- [x] **Step 4: Run registry tests**

Run: `go test ./relay/capability -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add relay/capability constant/channel.go
git commit -m "feat: add provider capability registry"
```

### Task 6: Enforce capability before distribution and billing

**Files:**
- Create: `middleware/capability.go`
- Test: `middleware/capability_test.go`
- Modify: `middleware/distributor.go`
- Modify: `service/channel.go`
- Modify: `router/relay-router.go`
- Modify: `types/error.go`

**Interfaces:**
- Consumes: `capability.ResolveRequest`, `Registry.Supports`.
- Produces: `service.RetryParam.Operation capability.Operation` and `middleware.RequireCapability(registry capability.Registry)`.

- [x] **Step 1: Add ordering tests with spies**

```go
func TestUnsupportedOperationStopsBeforeSelectionAndBilling(t *testing.T) {
    selected, billed := atomic.Bool{}, atomic.Bool{}
    /* send unsupported request through middleware chain */
    if selected.Load() || billed.Load() { t.Fatal("side effect occurred") }
    /* assert 400 and error.code == "unsupported_operation" */
}
```

- [x] **Step 2: Run focused tests**

Run: `go test ./middleware ./service -run 'Capability|UnsupportedOperation'`

Expected: FAIL because capability is not part of selection.

- [x] **Step 3: Filter explicit and random channels by operation**

Resolve protocol/operation immediately after authentication and request normalization. A token-pinned unsupported channel returns 400. Random selection receives `RetryParam.Operation` and excludes incapable channels before affinity/health mutations. If the operation exists but no eligible channel remains, return 503 `no_capable_channel`. Store the resolved operation in Gin context for billing, metrics, and handlers.

- [x] **Step 4: Run middleware/service and full tests**

Run: `go test ./middleware ./service ./router ./...`

Expected: PASS; capability ordering spy remains false on rejection.

- [x] **Step 5: Commit**

```bash
git add middleware service router types
git commit -m "feat: enforce capabilities before channel side effects"
```

### Task 7: Expose capabilities and verify existing protocol declarations

**Files:**
- Create: `controller/capability.go`
- Test: `controller/capability_test.go`
- Modify: `router/api-router.go`
- Create: `relay/capability/consistency_test.go`
- Modify: `docs/openapi/relay.json`
- Modify: `docs/openapi/api.json`

**Interfaces:**
- Produces: authenticated `GET /api/capabilities` returning registry version and per-channel-type operations.

- [x] **Step 1: Add response and consistency tests**

```go
func TestCapabilitySnapshotDoesNotExposeSecrets(t *testing.T) { /* no keys/base URLs */ }
func TestEveryRelayRouteHasCapabilityOperation(t *testing.T) { /* compare router manifest */ }
func TestEverySupportedCapabilityHasContractFixture(t *testing.T) { /* fixture key match */ }
```

- [x] **Step 2: Run and observe missing route/manifest failures**

Run: `go test ./controller ./relay/capability`

Expected: FAIL.

- [x] **Step 3: Implement the API and consistency manifest**

Return only public channel type/name/protocol/operation/reason fields. Generate the route manifest from the same operation definitions used by `ResolveRequest`; compare registry supported entries to adapter contract fixtures. Update OpenAPI with the capability endpoint and stable error schemas.

- [x] **Step 4: Verify contracts**

Run: `go test ./controller ./relay/capability && git diff --exit-code -- docs/openapi`

Expected: PASS and generated documents clean after the documented generation command.

- [x] **Step 5: Commit**

```bash
git add controller router relay/capability docs/openapi
git commit -m "feat: publish and verify capability contracts"
```

## Batch 3: Files, Fine-tuning, Image Variations, and Model Deletion

### Task 8: Add object storage and file persistence

**Files:**
- Create: `service/objectstore/store.go`
- Create: `service/objectstore/local.go`
- Create: `service/objectstore/s3.go`
- Test: `service/objectstore/contract_test.go`
- Create: `model/api_file.go`
- Test: `model/api_file_test.go`
- Modify: `model/main.go`
- Create: `migrations/003_api_files.sql`

**Interfaces:**
- Consumes/Produces: `objectstore.Store` defined in File Map.
- Produces: `model.CreateAPIFile`, `ListAPIFiles`, `GetOwnedAPIFile`, `MarkAPIFileDeleted`.

- [x] **Step 1: Add a reusable storage contract and ownership repository tests**

```go
func RunStoreContract(t *testing.T, store Store) { /* put/open/checksum/delete/health/path traversal */ }
func TestGetOwnedAPIFileHidesOtherTenant(t *testing.T) { /* expect gorm.ErrRecordNotFound */ }
```

- [x] **Step 2: Run focused tests**

Run: `go test ./service/objectstore ./model -run 'Store|APIFile'`

Expected: FAIL because implementations/models do not exist.

- [x] **Step 3: Implement local/S3 stores and `api_files`**

Local keys are generated server-side and resolved under one configured root after `filepath.Clean`; S3 uses path-style/region/endpoint configuration and verifies SHA-256 while streaming. The model records tenant, owner, purpose, filename, size, checksum, media type, storage key, channel/provider mapping, upstream ID, status, created/expiry/deleted timestamps. Add migration and AutoMigrate registration.

- [x] **Step 4: Run local and MinIO contracts plus all DB repository tests**

Run: `go test ./service/objectstore ./model -run 'Store|APIFile'`

Expected: local contract passes; S3 contract passes when `TEST_S3_ENDPOINT` is set and otherwise skips with an explicit reason.

- [x] **Step 5: Commit**

```bash
git add service/objectstore model migrations
git commit -m "feat: add tenant-owned object storage resources"
```

### Task 9: Implement the complete Files API

**Files:**
- Create: `dto/file.go`
- Create: `service/api_file.go`
- Test: `service/api_file_test.go`
- Create: `controller/file.go`
- Test: `controller/file_test.go`
- Modify: `router/relay-router.go`
- Modify: `docs/openapi/relay.json`

**Interfaces:**
- Produces: list/upload/metadata/content/delete handlers for `/v1/files`.

- [x] **Step 1: Add E2E handler tests for all five routes and error codes**

```go
func TestFilesLifecycle(t *testing.T) { /* upload -> list -> metadata -> content -> delete -> 404 */ }
func TestFilesRejectCrossTenantAccess(t *testing.T) { /* all resource routes return 404 */ }
func TestFilesRejectOversizeAndMimeMismatch(t *testing.T) { /* 413 then 422 */ }
```

- [x] **Step 2: Run tests against placeholder routes**

Run: `go test ./controller ./service -run Files`

Expected: FAIL because routes return 501.

- [x] **Step 3: Implement streaming validation and handlers**

Authorize token/user before multipart parsing. Apply `http.MaxBytesReader`, stream to object storage while hashing, verify media type/extension/archive expansion/path safety, persist only after storage succeeds, and delete the object on DB failure. List is owner-scoped and cursor-paginated; content streams with safe headers; delete is idempotent for the owner while invisible resources remain 404. Replace all Files `RelayNotImplemented` registrations.

- [x] **Step 4: Run API tests and OpenAPI consistency**

Run: `go test ./controller ./service ./router -run 'Files|RelayRoute'`

Expected: PASS and no Files route points to `RelayNotImplemented`.

- [x] **Step 5: Commit**

```bash
git add dto service controller router docs/openapi/relay.json
git commit -m "feat: implement the OpenAI-compatible Files lifecycle"
```

### Task 10: Implement fine-tuning persistence and worker

**Files:**
- Create: `model/fine_tuning.go`
- Test: `model/fine_tuning_test.go`
- Create: `migrations/004_fine_tuning.sql`
- Modify: `model/main.go`
- Create: `service/fine_tuning.go`
- Create: `service/fine_tuning_worker.go`
- Test: `service/fine_tuning_worker_test.go`

**Interfaces:**
- Consumes: `service.SystemTaskHandler` and the `FineTuningProvider` interface.
- Produces: CAS state transitions and ordered event pagination.

- [x] **Step 1: Add transition, ownership, ordering, and duplicate-poll tests**

```go
func TestFineTuningTransitionCAS(t *testing.T) { /* queued->running succeeds; stale transition fails */ }
func TestFineTuningEventsAreStableBySequence(t *testing.T) { /* sequence then ID cursor */ }
func TestFineTuningWorkerClaimsOnceAcrossInstances(t *testing.T) { /* one provider poll */ }
```

- [x] **Step 2: Run focused tests**

Run: `go test ./model ./service -run FineTuning`

Expected: FAIL because resources/worker are absent.

- [x] **Step 3: Implement resources and scheduled polling**

Persist owner, file IDs, provider/channel, model, upstream ID, normalized state, billing snapshot, timestamps, result model, and failure JSON. Events use monotonically increasing per-job sequence plus unique `(job_id, upstream_event_id)`. Register a distinct `fine_tuning_poll` `SystemTaskHandler`; claim jobs with DB CAS, load an isolated value snapshot, append new events idempotently, settle terminal billing once, and reschedule nonterminal jobs with bounded backoff.

- [x] **Step 4: Verify repository and worker behavior**

Run: `go test -race ./model ./service -run FineTuning`

Expected: PASS without duplicate poll/event/settlement.

- [x] **Step 5: Commit**

```bash
git add model service migrations
git commit -m "feat: add durable fine-tuning jobs and polling"
```

### Task 11: Implement fine-tuning and model deletion routes

**Files:**
- Create: `dto/fine_tuning.go`
- Create: `controller/fine_tuning.go`
- Test: `controller/fine_tuning_test.go`
- Modify: `router/relay-router.go`
- Modify: `docs/openapi/relay.json`

**Interfaces:**
- Produces: create/list/retrieve/cancel/events routes and `DELETE /v1/models/:model`.

- [x] **Step 1: Add lifecycle and authorization E2E tests**

```go
func TestFineTuningLifecycle(t *testing.T) { /* create/list/retrieve/events/cancel */ }
func TestFineTuningRejectsInvalidState(t *testing.T) { /* terminal cancel -> 409 */ }
func TestDeleteModelRequiresOwnedCompletedJob(t *testing.T) { /* otherwise 404 */ }
```

- [x] **Step 2: Run tests against placeholders**

Run: `go test ./controller -run 'FineTuning|DeleteModel'`

Expected: FAIL with 501/missing handlers.

- [x] **Step 3: Implement handlers and provider dispatch**

Create validates owned training/validation files and a capable channel before billing; local job creation and upstream ID persistence are transactional around compensating failure state. Retrieve/list/events use local ownership first. Cancel uses CAS and capability check. Model deletion resolves the completed owned job/result model before invoking a provider declaring `model.delete`. Replace every Fine-tuning and model-delete placeholder route.

- [x] **Step 4: Run handler, route, and race tests**

Run: `go test -race ./controller ./service ./model -run 'FineTuning|DeleteModel'`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add dto controller router docs/openapi/relay.json
git commit -m "feat: implement fine-tuning and model deletion APIs"
```

### Task 12: Implement image variations and eliminate reachable adapter placeholders

**Files:**
- Modify: `controller/relay.go`
- Modify: `relay/image_handler.go`
- Create: `relay/image_variation_test.go`
- Modify: `router/relay-router.go`
- Modify: provider adapter files discovered by the capability consistency test
- Create/Modify: `relay/channel/*/*_contract_test.go`

**Interfaces:**
- Consumes: shared upload validator and capability registry.
- Produces: executable `POST /v1/images/variations` and no supported registry entry backed by placeholder behavior.

- [x] **Step 1: Add image variation contract and placeholder reachability tests**

```go
func TestImageVariationMultipartContract(t *testing.T) { /* image + optional mask/n/size/response_format */ }
func TestSupportedCapabilitiesDoNotReturnNotImplemented(t *testing.T) { /* generated adapter fixtures */ }
```

- [x] **Step 2: Run and confirm placeholder failures**

Run: `go test ./relay/... ./router -run 'Variation|SupportedCapabilities'`

Expected: FAIL on the 501 route or supported placeholder adapters.

- [x] **Step 3: Implement variation and align declarations**

Reuse the upload validator, normalize the OpenAI multipart request, and dispatch only to provider adapters with declared image-variation support. For each supported declaration, implement conversion/error/timeout/retry/billing fixtures; for upstream-impossible combinations, declare unsupported so dispatch is unreachable. Remove public `RelayNotImplemented` registration and production `errors.New("not implemented")` behavior.

- [x] **Step 4: Verify the entire relay surface**

Run: `go test -race ./relay/... ./router ./controller && ! rg -n 'RelayNotImplemented' router/relay-router.go`

Expected: PASS and no public route references the placeholder handler.

- [x] **Step 5: Commit**

```bash
git add controller relay router
git commit -m "feat: complete public relay operations and provider contracts"
```

## Batch 4: Frontend, CLI, and Electron

### Task 13: Close iframe and HTML rendering security boundaries

**Files:**
- Create: `web/src/lib/security/iframe-policy.ts`
- Test: `web/src/lib/security/iframe-policy.test.ts`
- Modify: `web/src/routes/_authenticated/chat/$chatId.tsx`
- Modify: `web/src/components/ai-elements/web-preview.tsx`
- Modify: `web/src/components/layout/components/footer.tsx`
- Modify: `web/src/lib/utils/render.ts`

**Interfaces:**
- Produces: `resolveIframePolicy(url, preset, env): IframePolicy` and sanitized footer rendering.

- [x] **Step 1: Add URL/sandbox/permission/sanitization tests**

```ts
assert.equal(resolveIframePolicy('javascript:alert(1)', preset, env).allowed, false)
assert.equal(policy.sandbox.includes('allow-same-origin') && policy.sandbox.includes('allow-scripts'), false)
assert.equal(sanitizeConfiguredHtml('<img src=x onerror=alert(1)>').includes('onerror'), false)
```

- [x] **Step 2: Run security tests**

Run: `cd web && npm test -- --test-name-pattern='iframe|sanitize'`

Expected: FAIL because the shared policy is absent/current iframe attributes are unsafe.

- [x] **Step 3: Implement the shared policy**

Permit HTTPS in production and explicit localhost HTTP in development. Default sandbox excludes camera, microphone, forms, popups, downloads, presentation, and same-origin. Scripts plus same-origin are permitted only on a separately configured preview origin. Chat launch uses a short-lived server credential bound to user/preset/origin; raw long-lived key interpolation requires an admin compatibility flag and warning. Route configured footer HTML through the existing DOMPurify utility.

- [x] **Step 4: Run web tests and lint on changed files**

Run: `cd web && npm test && npx eslint 'src/lib/security/**' 'src/routes/_authenticated/chat/$chatId.tsx' 'src/components/ai-elements/web-preview.tsx' 'src/components/layout/components/footer.tsx'`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add web/src
git commit -m "fix: enforce iframe and configured HTML security policies"
```

### Task 14: Add capability-driven Files and Fine-tuning UI

**Files:**
- Create: `web/src/features/capabilities/api.ts`
- Create: `web/src/features/capabilities/types.ts`
- Create: `web/src/features/files/`
- Create: `web/src/features/fine-tuning/`
- Create: `web/src/routes/_authenticated/files/index.tsx`
- Create: `web/src/routes/_authenticated/fine-tuning/index.tsx`
- Modify: navigation configuration under `web/src/components/layout/`
- Test: feature-local `*.test.tsx` files

**Interfaces:**
- Consumes: `GET /api/capabilities` and the completed resource APIs.
- Produces: capability-hidden actions, full file lifecycle UI, and fine-tuning job lifecycle UI.

- [x] **Step 1: Add component tests**

```tsx
it('hides create when no capable channel exists', async () => { /* mock capability response; query absent */ })
it('uploads and deletes an owned file', async () => { /* assert API sequence */ })
it('renders ordered fine-tuning events and cancellation state', async () => { /* assert stable UI */ })
```

- [x] **Step 2: Run focused tests**

Run: `cd web && npm test -- --test-name-pattern='capability|files|fine-tuning'`

Expected: FAIL because features/routes do not exist.

- [x] **Step 3: Implement typed queries and workflows**

Use the repository request client/query conventions. Cache the capability snapshot by registry version. File UI supports purpose, validation errors, progress, list, download, and delete. Fine-tuning UI supports owned training/validation file selection, capable model/channel display, create/list/detail/events/cancel/result model, and stable error-code copy. Admin views expose provider declaration/certification state without secrets.

- [x] **Step 4: Run tests/type/build**

Run: `cd web && npm test && npm run build:check && npm run build`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat: add capability-driven file and fine-tuning workflows"
```

### Task 15: Make frontend and CLI quality gates clean

**Files:**
- Modify: files reported by `npm run lint` using their local rule violations
- Modify: `web/knip.config.ts`
- Modify/Delete: files proven unused by `npm run knip`
- Create: `cli/src/config.test.ts`
- Create: `cli/src/connect.test.ts`
- Create: `cli/src/errors.test.ts`

**Interfaces:**
- Produces: zero-error lint, explicit dead-code policy, CLI configuration/connectivity/error contracts.

- [x] **Step 1: Add CLI behavior tests and capture lint/knip baselines**

```ts
test('never logs API secrets on connection failure', async () => { /* redact token */ })
test('writes deterministic config with mode 0600', async () => { /* stat and compare */ })
```

- [x] **Step 2: Run failing gates**

Run: `(cd web && npm run lint && npm run knip) && (cd cli && npm test)`

Expected: FAIL with the recorded lint/dead-code issues and missing CLI tests.

- [x] **Step 3: Fix, do not suppress, actionable findings**

Resolve all lint errors. For Knip, remove truly unused files/dependencies/exports, convert intentional generated entrypoints to exact config entries, and document each retained exclusion in `knip.config.ts`; do not add blanket ignores. Implement CLI tests using temporary directories and a local HTTP test server, ensuring secret redaction and deterministic exit codes.

- [x] **Step 4: Run all frontend/CLI gates**

Run: `bash scripts/verify-web.sh && bash scripts/verify-cli.sh`

Expected: exit 0 with zero lint errors and no unexplained Knip finding.

- [x] **Step 5: Commit**

```bash
git add web cli
git commit -m "chore: make web and CLI release gates clean"
```

### Task 16: Reproducible cross-platform Electron packaging

**Files:**
- Modify: `electron/build.sh`
- Modify: `electron/package.json`
- Modify: `electron/main.js`
- Create: `electron/test/startup.test.js`
- Create: `electron/scripts/package-probe.js`
- Create: `.github/workflows/electron.yml`
- Modify: `electron/README.md`

**Interfaces:**
- Produces: `npm run build:all`, `npm test`, and `npm run probe -- <artifact>` on Windows/macOS/Linux.

- [x] **Step 1: Add packaged startup probe test**

```js
test('wrapper starts backend, waits for readyz, and terminates children', async () => {
  // fake backend process + temporary userData; assert lifecycle and no orphan
})
```

- [x] **Step 2: Run Electron test**

Run: `cd electron && npm test`

Expected: FAIL because startup lifecycle is not testable/current package has no test script.

- [x] **Step 3: Implement one build pipeline and CI matrix**

Make the wrapper accept injected backend path/port/readiness URL for tests, terminate child processes on all exit signals, and surface startup errors. `build:all` builds web/backend/wrapper from the same version. CI matrix builds Windows/macOS/Linux, runs startup probe against unpacked package, then emits SHA-256 checksums.

- [x] **Step 4: Run local Electron verification**

Run: `cd electron && npm test && npm run build:all && npm run probe -- dist`

Expected: tests/build/probe exit 0 on the current platform.

- [x] **Step 5: Commit**

```bash
git add electron .github/workflows/electron.yml
git commit -m "build: add reproducible cross-platform desktop packaging"
```

## Batch 5: Integration, Certification, and Release Decision

### Task 17: Add database, Redis, S3, API, and provider contract matrices

**Files:**
- Modify: `docker-compose.test.yml`
- Create: `tests/integration/`
- Create: `tests/contracts/`
- Create: `scripts/run-integration-matrix.sh`
- Create: `.github/workflows/integration.yml`

**Interfaces:**
- Produces: identical test command locally and in merge CI; provider fixtures keyed by capability registry entries.

- [x] **Step 1: Add a matrix completeness test**

```go
func TestEverySupportedCapabilityHasProviderFixture(t *testing.T) { /* exact key-set equality */ }
func TestAllPublicResourceRoutesHaveE2ECase(t *testing.T) { /* exact operation-set equality */ }
```

- [x] **Step 2: Run completeness tests**

Run: `go test ./tests/contracts/...`

Expected: FAIL until every supported capability/resource operation has a fixture.

- [x] **Step 3: Implement the integration stack and fixtures**

Compose starts gateway, MySQL, PostgreSQL, Redis, and MinIO with isolated credentials/volumes. The runner executes migrations/repositories on SQLite/MySQL/PostgreSQL, object-store contracts against MinIO, distributed rate limiting and worker claims across two gateway instances, simulated upstream success/error/timeout/malformed/retry/billing cases for every supported capability, and Files/Fine-tuning/variation/model-delete E2E.

- [ ] **Step 4: Run the full merge matrix**

Run: `bash scripts/run-integration-matrix.sh`

Expected: all database, Redis, MinIO, provider-contract, and API-E2E suites pass; containers are removed by trap.

Verification note (2026-08-10): attempted locally; blocked before startup because this host has no Docker/Compose runtime. The command exits with the explicit infrastructure blocker `integration matrix requires Docker with the Compose plugin`.

- [x] **Step 5: Commit**

```bash
git add docker-compose.test.yml tests scripts/run-integration-matrix.sh .github/workflows/integration.yml
git commit -m "test: add production dependency and provider matrices"
```

### Task 18: Browser, failover, performance, and operational runbooks

**Files:**
- Create: `web/e2e/`
- Create: `tests/fault/`
- Create: `tests/performance/`
- Create: `.github/workflows/nightly.yml`
- Create: `docs/operations/production-runbook.md`
- Create: `docs/operations/backup-restore.md`
- Create: `docs/operations/key-rotation.md`
- Create: `docs/operations/incident-response.md`
- Create: `docs/operations/rollback.md`

**Interfaces:**
- Produces: `npm run e2e`, fault/soak commands, and validated operator runbooks.

- [x] **Step 1: Add browser and failover acceptance tests**

```ts
test('file and fine-tuning journey', async ({ page }) => { /* upload, create, event, cancel */ })
test('unsupported capability never issues upstream request', async ({ page }) => { /* assert counter zero */ })
```

- [x] **Step 2: Run acceptance tests and verify missing coverage**

Run: `cd web && npm run e2e`

Expected: FAIL before E2E configuration/specs exist.

- [x] **Step 3: Implement nightly suites and executable runbooks**

Cover authentication, capability-hidden UI, Files/Fine-tuning, iframe policy, and admin state in browsers. Fault tests remove DB/Redis/MinIO/upstream independently and assert liveness/readiness, no double billing, recovery, and worker failover. Performance tests define pass thresholds for relay latency/error rate, streaming first-token time, upload throughput/memory, and worker lag. Runbooks embed commands verified in an ephemeral test deployment for alerts, capacity, backup/restore, credential revocation/rotation, incident response, and rollback.

- [ ] **Step 4: Run nightly acceptance locally where supported**

Run: `bash scripts/run-integration-matrix.sh --nightly && cd web && npm run e2e`

Expected: PASS; fault recovery and performance thresholds are recorded as machine-readable artifacts.

Verification note (2026-08-10): browser acceptance passed independently (8/8 desktop and mobile projects); the nightly matrix remains blocked by the same unavailable Docker/Compose runtime.

- [x] **Step 5: Commit**

```bash
git add web/e2e web/package.json tests/fault tests/performance .github/workflows/nightly.yml docs/operations
git commit -m "test: add browser failover performance and operations gates"
```

### Task 19: Release certification and final no-placeholder gate

**Files:**
- Create: `scripts/certify-providers.sh`
- Create: `scripts/release-gate.sh`
- Create: `docs/operations/provider-certification.md`
- Modify: `.github/workflows/release.yml`
- Test: `scripts/release_gate_test.sh`

**Interfaces:**
- Produces: signed certification manifest bound to commit, registry version, provider/channel, test result, and timestamp.

- [x] **Step 1: Add release-gate fixture tests**

```bash
# Fails for missing credential result, public 501, placeholder adapter, dirty generated docs,
# unsigned image, failed restore rehearsal, or missing desktop platform checksum.
```

- [x] **Step 2: Run gate against incomplete fixture**

Run: `bash scripts/release_gate_test.sh`

Expected: fixture failures enumerate every missing certification item with a non-zero exit.

- [x] **Step 3: Implement credential-gated live certification and aggregate gate**

Generate live test cases from production-certified registry entries. Redact credentials and store only result metadata. `release-gate.sh` runs all PR/merge/nightly gates, scans route registrations and supported adapter paths for placeholders, verifies migration/backup/restore/rollback rehearsal, image digest/signature/SBOM/provenance, container smoke, and Windows/macOS/Linux package checksums/startup probes. A missing live credential is a hard incomplete result, not a skip.

- [ ] **Step 4: Run the complete release gate**

Run: `bash scripts/release-gate.sh`

Expected: exit 0 only when every completion criterion is satisfied; otherwise it prints an exact external credential/infrastructure blocker list and exits non-zero.

Verification note (2026-08-10): all local gates executed by the aggregate command passed; it correctly stopped on seven external evidence blockers (live provider manifest, signed image, restore rehearsal, Linux/macOS/Windows desktop evidence, nightly/source evidence) plus Docker/Compose certification.

- [x] **Step 5: Commit**

```bash
git add scripts .github/workflows/release.yml docs/operations/provider-certification.md
git commit -m "release: enforce complete production certification"
```

### Task 20: Security remediation and signed release enforcement

- [x] **Step 1: Run an independent standard security scan**

Snapshot `680e859` was reviewed across authentication, authorization, credentials, outbound I/O, Electron, operations, restore, and release surfaces. The sealed report contains 17 validated findings (7 high, 8 medium, 2 low) with partial architecture-led coverage.

- [x] **Step 2: Close code-level runtime and tenant boundaries**

Fixed desktop loopback binding and exact-origin navigation, refresh-token predecessor replay, Midjourney media ownership, HTTP/pprof/management exposure, subscription target-role enforcement, video response-header/MIME/size/rate controls, and restore checksum confusion. Every behavior has a regression test that failed before the change and passed afterward.

- [x] **Step 3: Bind release evidence to trusted inputs and exact candidate bytes**

Provider certification now requires an externally injected public-key trust anchor. The aggregate gate recomputes backend and Electron package checksums from the downloaded candidate tree and rejects modified bytes. macOS signing/notarization and Windows Authenticode are mandatory and verified before their evidence can report success.

- [ ] **Step 4: Complete external trust and credential architecture**

Remaining release blockers require external/product inputs that must not be guessed or simulated: migrate legacy channel/PAT/secret-option plaintext storage to versioned encrypted or hashed persistence; move provider certification into independently pinned trusted tooling with short-lived credentials/non-exportable signing; define and certify a proxy/worker SSRF contract that validates every redirect and actual dial peer; provision Apple/Windows signing identities and provider-certification credentials.

- [x] **Step 5: Re-run all locally available gates**

The latest aggregate run passed backend, web, CLI, route/provider contracts, and 8/8 browser acceptance tests. It remains correctly blocked only by six external evidence bundles plus unavailable Docker/Compose certification.

## Execution Order and Checkpoints

Execute Tasks 1–4, then run `bash scripts/verify-backend.sh` and commit the batch. Execute Tasks 5–7 and run capability/route/OpenAPI consistency. Execute Tasks 8–12 and run all resource/provider contracts. Execute Tasks 13–16 and run web/CLI/Electron gates. Execute Tasks 17–19 and run the aggregate release gate.

At each checkpoint, update this plan’s checkboxes only after the exact verification command has passed. Never mark a credential-gated certification complete from simulated fixtures.
