# Complete Production Readiness Design

**Date:** 2026-08-10

**Status:** Approved

## Objective

Bring the entire gateway repository to a production-release standard. Every public API route must have a real implementation. Provider and protocol combinations that the upstream cannot support must be rejected by a single capability system before adapter execution, billing, or channel mutation. The release must be reproducible across backend, web, CLI, containers, databases, multi-instance deployments, and Electron packages for Windows, macOS, and Linux.

## Product Boundary

“Complete” means:

- Every documented public route is implemented and has stable request, response, authorization, and error behavior.
- No reachable production path returns a placeholder `not implemented` response.
- Every provider/protocol/operation combination is declared in a capability registry.
- A supported combination has an implementation and contract coverage.
- An upstream-impossible combination is rejected before channel selection and billing with a stable `unsupported_operation` response.
- The gateway never silently substitutes a different provider for an unsupported operation.
- Production release artifacts and deployment configurations execute the current repository revision, not an unrelated `latest` image.

This boundary does not require pretending an upstream supports a capability it does not have. It requires complete gateway behavior for both supported and unsupported combinations.

## Delivery Strategy

Implementation proceeds by protocol-oriented vertical slices. Each slice completes route registration, request normalization, capability checks, authorization, persistence, provider conversion, response conversion, UI behavior, OpenAPI documentation, and automated tests before the next protocol begins.

The protocol order is:

1. Existing Chat, Responses, Embeddings, Audio, Rerank, Gemini, and Claude paths.
2. Files lifecycle.
3. Fine-tuning lifecycle and events.
4. Image variations.
5. Model deletion.
6. Provider capability completion and removal of reachable adapter placeholders.

Cross-cutting production foundations are completed before protocol expansion so every slice inherits working CI, race detection, migrations, object storage, secrets, and release packaging.

## Workstreams

### 1. Release Foundation

- Add mandatory PR gates for backend formatting, unit tests, vet, race detection, frontend formatting, lint, type checks, tests, production build, CLI build, capability consistency, and generated documentation consistency.
- Fix version injection to use `github.com/QuantumNous/new-api/common.Version` everywhere.
- Build immutable images from the current revision and refer to them by version and digest.
- Generate checksums, SBOMs, provenance, and signatures for release artifacts.
- Require production secrets through deployment-time validation instead of documented default passwords.
- Provide upgrade preflight, backup, restore, and rollback commands.

### 2. Backend Correctness

- Protect logger rotation state with atomic or locked state.
- Prevent GORM updates from mutating task objects concurrently read by other goroutines; worker code operates on isolated task snapshots.
- Set Gin global test mode once rather than from parallel tests.
- Stop copying `CustomEvent` values that contain a mutex.
- Make `go vet ./...` and `go test -race ./...` release-blocking.
- Preserve SQLite, MySQL, and PostgreSQL behavior through a database test matrix.

### 3. Capability Registry

The registry is the single source of truth for `ChannelType × Protocol × Operation` support. It is consumed by backend distribution, API validation, UI forms, provider selection, OpenAPI consistency tests, and certification test generation.

Request processing is:

`authentication → normalization → capability resolution → channel selection → billing preflight → adapter conversion → upstream call → response conversion and settlement`

Rules:

- Channel selection requires both model eligibility and operation capability.
- Capability failure occurs before pre-consume, channel mutation, cooldown, or automatic disablement.
- The web UI queries capability data and does not offer invalid actions.
- Adapter methods for unsupported combinations are unreachable by construction.

### 4. Files and Object Storage

Add an `ObjectStore` interface with two implementations:

- Persistent local-volume storage for development and single-node testing.
- S3-compatible object storage for production and multi-instance deployments.

The `api_files` resource records tenant, owner, purpose, filename, byte size, checksum, media type, storage reference, provider/channel mapping, upstream file identifier, status, creation time, and expiration time.

All file list, upload, metadata, content, and delete operations authorize against the local resource mapping. Upstream identifiers never act as authorization evidence. Upload processing enforces byte limits, MIME and extension consistency, archive/decompression limits, path safety, SSRF protection, checksum validation, and deterministic cleanup.

### 5. Fine-Tuning Resources

The `fine_tuning_jobs` resource records owner, training/validation files, provider and channel, base model, upstream job identifier, normalized status, billing snapshot, timestamps, result model, and normalized failure data.

The `fine_tuning_events` resource stores ordered job events and supports deterministic pagination. Create, list, retrieve, cancel, and event-list routes first verify ownership and state, then invoke only a provider declaring fine-tuning capability.

Fine-tuning polling reuses the system-task scheduling infrastructure but uses a distinct fine-tuning repository and worker contract. It does not overload video-task persistence or share mutable task objects across workers.

### 6. Remaining Public APIs

- Image variations use the shared multipart upload validator and capability resolution.
- Model deletion resolves ownership of a gateway-created fine-tuned model and invokes only a provider declaring model-deletion capability.
- Existing Files, Fine-tunes, Images, and Models OpenAPI entries are converted from “not implemented” descriptions to executable contracts.
- `RelayNotImplemented` is removed from public route registration.

### 7. Provider Completion

Each provider receives an explicit capability declaration. For each declared supported operation:

- Request conversion is implemented.
- Streaming and buffered response conversion are implemented where applicable.
- Provider errors, rate limits, authentication errors, and malformed responses are normalized.
- Billing inputs are bounded and use the shared safe quota conversion path.
- Contract tests cover success, unsupported parameters, upstream errors, timeouts, and retry behavior.

Unsupported operations are rejected before adapter dispatch. Provider adapters must not use `errors.New("not implemented")` as production behavior.

## Error Contract

- `400 unsupported_operation`: the selected provider type cannot perform the requested operation.
- `404 resource_not_found`: the resource does not exist or is not visible to the caller.
- `409 invalid_resource_state`: the requested transition is invalid for the current state.
- `413 file_too_large`: the upload exceeds configured limits.
- `422 invalid_file`: content, checksum, MIME type, or archive constraints fail validation.
- `502 upstream_error`: the upstream violates its declared protocol or returns a normalized failure.
- `503 no_capable_channel`: the operation exists but no currently eligible channel can serve it.

Errors use the repository’s OpenAI-compatible envelope and stable machine-readable codes. Capability errors do not charge quota or affect channel health.

## Security Design

- Every iframe URL passes a shared URL policy. Production permits HTTPS, with explicit localhost exceptions for development.
- Iframes are sandboxed. Camera, microphone, forms, popups, downloads, and presentation permissions default to off and are enabled per trusted preset.
- Web preview never combines `allow-scripts` and `allow-same-origin` on content sharing the application origin. Full-capability preview content is served from a separately configured origin.
- Long-lived API keys are not embedded into third-party iframe URLs by default. Chat launches use short-lived credentials bound to user, preset, and allowed origin. Legacy key interpolation requires an explicit administrator-controlled compatibility setting and visible warning.
- Configured footer HTML uses the existing DOMPurify-based rendering boundary.
- File content and URLs use the existing SSRF protections plus upload-specific limits.
- Resource identifiers are tenant-scoped and ownership-checked before any upstream operation.

## Deployment and Operations

- Production Compose uses the current repository image with an immutable tag/digest.
- `SESSION_SECRET`, `GATEWAY_AES_KEY`, database credentials, Redis credentials, and object-store credentials are required production inputs.
- Secure cookies, exact trusted origins, and explicit trusted proxy ranges are production defaults.
- Redis-backed distributed rate limiting is wired automatically for multi-instance deployments.
- `healthz` reports process liveness without treating dependency loss as process death.
- `readyz` checks the primary database, configured Redis, object storage, migration version, and initialized capability registry.
- Metrics cover request rate, latency, error classes, provider capability rejection, object-store failures, fine-tuning states, key-pool health, billing saturation, and worker lag.
- Operational documentation covers alerts, backups, restore validation, key rotation, credential revocation, capacity, incident response, and rollback.

## Frontend, CLI, and Electron

- Frontend lint, type, test, format, copyright, production build, and dead-code checks become tracked release gates.
- A committed test script runs all existing and new Node test files without ad hoc package installation.
- UI availability is driven by the capability API rather than hard-coded assumptions.
- Files and fine-tuning receive complete user and administrator workflows.
- The CLI receives automated tests for configuration generation, connection verification, and error handling.
- Electron builds backend, frontend, and wrapper through one reproducible script.
- Windows, macOS, and Linux packages execute a packaged-startup probe and publish checksums.

## Verification Architecture

### Pull Request

- Backend format, unit tests, vet, race detection, and build.
- Frontend format, copyright, lint, type checks, tests, production build, and dead-code policy.
- CLI tests and build.
- Capability registry, route, adapter, UI, and OpenAPI consistency.

### Merge Branch

- SQLite, MySQL, and PostgreSQL migrations and repository tests.
- Redis and S3-compatible object-storage integration.
- Simulated upstream contract suites for every supported capability.
- Files, fine-tuning, image variation, and model-deletion API E2E.

### Nightly

- Browser E2E.
- Multi-instance rate limiting, worker coordination, and failover.
- Fault injection for database, Redis, object storage, upstream timeouts, and partial responses.
- Performance and soak testing of relay, streaming, uploads, and background jobs.

### Release

- Credential-gated live provider certification generated from the capability registry.
- Migration, backup, restore, and rollback rehearsal.
- Immutable image build, signature verification, and container smoke test.
- Windows, macOS, and Linux Electron package build and startup probe.

Missing live credentials keep the corresponding provider certification incomplete and block a production-certified release; they do not weaken simulated contract requirements.

## Completion Criteria

The program is complete only when:

- No public route returns a placeholder 501.
- No reachable adapter path contains placeholder implementation behavior.
- Every capability declaration has matching implementation and contract tests.
- Unsupported combinations are rejected before billing and channel mutation.
- All repository and release gates pass, including vet and race detection.
- SQLite, MySQL, PostgreSQL, Redis, and S3-compatible storage matrices pass.
- Browser and multi-instance E2E pass.
- Current-revision container artifacts and all three Electron platform artifacts are reproducible.
- Live certification passes for every provider declared production-certified.
- Upgrade, restore, key rotation, monitoring, and rollback runbooks are validated.

## Compatibility and Migration

- Existing channel records receive capabilities through channel-type defaults; no manual record rewrite is required for basic migration.
- Existing user-facing routes retain their paths and OpenAI-compatible response envelopes.
- Legacy chat key interpolation remains opt-in during a documented transition period.
- Database migrations are forward-versioned, idempotent where required by existing startup behavior, and tested for upgrade and rollback on all supported databases.
- Unsupported operations fail explicitly instead of reaching provider-specific placeholder code.
