# Release Readiness CI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the current release candidate into a repository-level releasable build by making CI, the integration matrix, and nightly resilience deterministic and green, while keeping credential-bound publication gates fail-closed.

**Architecture:** Add a small standard-library workflow contract suite that guards action pins, artifact paths, test-container health checks, shared image builds, and resource ordering. Replace the test upstream's unavailable in-container probe with a deterministic Python HTTP stub, build the gateway test image once for both nodes, and defer browser installation until Docker-heavy checks have released their resources. GitHub remains the authoritative remote verification environment.

**Tech Stack:** GitHub Actions YAML, Docker Compose, Python 3 standard library, Bash, Go, Bun/Rsbuild.

## Global Constraints

- Preserve all protected new-api and QuantumNous identities and metadata.
- Do not modify or commit the unrelated untracked `scripts/prepare-sidecar-env.py`.
- Use pinned GitHub Actions revisions already accepted by the repository.
- Keep release certification fail-closed when provider, signing, or notarization credentials are absent.
- Do not weaken backend, frontend, integration, nightly, provider, signing, SBOM, provenance, or rollback gates to obtain a green result.
- Every repository change must have a deterministic regression test or an exact workflow verification command.

---

### Task 1: Guard and repair CI workflow contracts

**Files:**
- Create: `scripts/release_ci_contract_test.py`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: repository workflow and Compose files as UTF-8 text.
- Produces: `python3 -m unittest scripts/release_ci_contract_test.py -v`, a fast release-configuration regression gate.

- [x] **Step 1: Write failing contract tests**

  Add tests that require every `actions/setup-go` use to reference `924ae3a1cded613372ab5595356fb5720e22ba16`, require the web manifest assertion to execute from `web/`, require a deterministic upstream probe, require a single shared gateway test image, and require the Nightly integration matrix to run before Chromium installation.

- [x] **Step 2: Verify RED**

  Run: `python3 -m unittest scripts/release_ci_contract_test.py -v`

  Expected: failures identify the invalid setup-go SHA, root-relative `dist/install/manifest.json`, `wget` in the http-echo container, duplicate gateway builds, and browser installation before the Docker matrix.

- [x] **Step 3: Repair the CI action and artifact path**

  Replace only the mistyped setup-go SHA in the onboarding job. Set `working-directory: web` on the manifest check so the asserted path is the build output produced by `scripts/verify-web.sh`. Run the contract suite early in the web job so future workflow regressions remain observable even if another job cannot resolve an action.

- [x] **Step 4: Verify the focused contracts**

  Run: `python3 -m unittest scripts/release_ci_contract_test.py -v`

  Expected: only the still-unimplemented Compose and Nightly contracts remain red.

### Task 2: Make the integration upstream and gateway build deterministic

**Files:**
- Create: `tests/upstream_server.py`
- Create: `tests/upstream_server_test.py`
- Modify: `docker-compose.test.yml`
- Modify: `scripts/run-integration-matrix.sh`

**Interfaces:**
- Consumes: `UPSTREAM_PORT` with default `8080`; arbitrary GET and POST paths.
- Produces: a JSON `{"status":"ok"}` response, an in-container Python health probe, and one shared `llmrouter-test-gateway:local` image for both gateway nodes.

- [x] **Step 1: Write the failing upstream behavior test**

  Start `tests/upstream_server.py` with an ephemeral port, send GET and POST requests, and require HTTP 200, `application/json`, and the exact status payload.

- [x] **Step 2: Verify RED**

  Run: `python3 -m unittest tests/upstream_server_test.py -v`

  Expected: fail because `tests/upstream_server.py` does not exist.

- [x] **Step 3: Implement the minimal deterministic upstream**

  Use `ThreadingHTTPServer` and a top-level `BaseHTTPRequestHandler` subclass. Do not log request bodies or headers. Serve the same bounded status response for GET and POST and read the port from `UPSTREAM_PORT`.

- [x] **Step 4: Replace the broken Compose health boundary**

  Run the stub in `python:3.12-alpine`, mount only `tests/upstream_server.py` read-only, and probe it with Python's `urllib.request` from inside the same container. Tag the gateway test image as `llmrouter-test-gateway:local`; build it only from `gateway-a`, and let `gateway-b` reuse it.

- [x] **Step 5: Build once and clean test resources**

  In `scripts/run-integration-matrix.sh`, build `gateway-a` once, start the matrix with `--no-build`, and remove the shared test image during cleanup without changing persistent developer data.

- [x] **Step 6: Verify GREEN without Docker and defer Docker proof to Actions**

  Run:

  ```bash
  python3 -m unittest tests/upstream_server_test.py -v
  python3 -m unittest scripts/release_ci_contract_test.py -v
  bash -n scripts/run-integration-matrix.sh
  ```

  Expected: PASS. The GitHub Integration Matrix is the authoritative Docker execution because Docker is unavailable in the local workspace.

### Task 3: Prevent Nightly and certification disk exhaustion

**Files:**
- Modify: `.github/workflows/nightly.yml`
- Modify: `.github/workflows/release.yml`
- Test: `scripts/release_ci_contract_test.py`

**Interfaces:**
- Consumes: the existing integration matrix and Playwright commands.
- Produces: Docker-heavy gates before Chromium installation, followed by browser acceptance after Docker build cache cleanup on ephemeral GitHub runners.

- [x] **Step 1: Keep the ordering contract red until workflow changes**

  Run: `python3 -m unittest scripts/release_ci_contract_test.py -v`

  Expected: Nightly and release certification ordering tests fail while Chromium is installed before the Docker matrix.

- [x] **Step 2: Reorder Nightly resource use**

  Run the integration matrix before installing web dependencies and Chromium. After the matrix succeeds, prune the ephemeral runner's Docker build cache, then install Chromium, build the web app, and run Playwright.

- [x] **Step 3: Apply the same ordering to release certification**

  Run backend, web, and CLI deterministic gates first; run the Docker integration matrix; prune its build cache; then install Chromium and run browser acceptance. Preserve live provider certification and aggregate release gates unchanged.

- [x] **Step 4: Verify GREEN**

  Run:

  ```bash
  python3 -m unittest scripts/release_ci_contract_test.py -v
  python3 -m unittest tests/upstream_server_test.py -v
  git diff --check
  ```

  Expected: PASS with no workflow contract failures.

### Task 4: Prove repository release readiness on GitHub

**Files:**
- Modify: `docs/operations/side-by-side-migration-status.md`
- Modify: this plan, checking completed steps and recording immutable evidence.

**Interfaces:**
- Consumes: local verification output and GitHub Actions run URLs for the exact commit.
- Produces: a release-readiness record that distinguishes green repository gates from credential-bound publication gates.

- [x] **Step 1: Run the full local completion gate**

  Run:

  ```bash
  python3 -m unittest scripts/release_ci_contract_test.py tests/upstream_server_test.py scripts/onboarding_assets_test.py -v
  bash scripts/verify-backend.sh
  bash scripts/verify-web.sh
  bash scripts/verify-cli.sh
  go vet ./...
  git diff --check
  ```

  Expected: PASS. If Bun is unavailable directly, use the configured bundled Node runtime for the existing Rsbuild-compatible checks and record the limitation; do not mark the Bun gate passed without GitHub evidence.

- [ ] **Step 2: Commit and push the release-readiness branch**

  Commit only the plan, tests, workflows, Compose file, integration script, and readiness documentation. Push `codex/release-readiness` to `NeoMei/llmrouter`.

- [ ] **Step 3: Run authoritative GitHub checks**

  Open a PR using `.github/PULL_REQUEST_TEMPLATE.md`, disclose AI assistance if required by the repository author check, and require fresh successful CI and Integration Matrix runs for the exact head commit. Manually dispatch Nightly on that same commit and require browser evidence upload.

- [ ] **Step 4: Merge only after all repository gates are green**

  Merge to `main`, confirm `main` and `upstream-sync` point to the intended release commit, and rerun or confirm push-triggered CI and Integration Matrix on `main`.

- [ ] **Step 5: Record remaining credential-bound publication gates**

  Verify GitHub repository/environment secret names without exposing values. If Apple/Windows signing credentials and production provider-certification inputs are absent, retain a fail-closed NO-GO for signed GA publication while declaring the repository code eligible for RC publication. Do not extract provider credentials from the production server or create signing identities without explicit authorization.
