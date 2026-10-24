# Domestic multimodal account pools implementation plan

> **For agentic workers:** Use subagent-driven-development to implement this plan task-by-task, with one implementer at a time and independent task review. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide persistent, scoped upstream account pooling and the domestic model interfaces needed by SuperWagie's library plus image, speech, video-understanding and multimodal-vector consumers.

**Architecture:** Retain provider adapters, channel identities, model permissions and existing atomic billing. Add a shared persisted account-health owner, integrate bounded member selection into actual HTTP attempts, expose capability metadata and provider-compatible knowledge endpoints, and bind asynchronous resources to their creating member. Existing frontend model and key management pages expose the same contracts.

**Tech Stack:** Go 1.26.10, Gin, GORM, SQLite/MySQL/PostgreSQL, optional Redis; React 19, TypeScript, Bun.

**Spec:** `docs/superpowers/specs/2026-10-24-domestic-account-pool-design.md`, approved by the user's "继续" after the SuperWagie requirements review on 2026-10-24.

## Global Constraints

- Implementation base `9dd64bb77`; current isolated worktree `/Users/neomei/.codex/worktrees/llmrouter-upstream-sync-20261024/llmrouter`, branch `codex/upstream-sync-20261024`. Original checkout must retain its untracked legal documents and sidecar helper.
- Preserve Gateway channel 59 and every existing channel identity, BYOK isolation, scoped Responses continuity, tool policy, atomic wallet/token preconsume/refund, saturation audit and payment behavior.
- JSON encoding/decoding uses `common.*`; optional scalars preserve explicit zero/false. New or substantially rewritten backend tests use testify require/assert.
- New tables/queries work on SQLite, MySQL >=5.7.8, PostgreSQL >=9.6; use GORM and `model.lockForUpdate`, with SQLite fallback. No SKIP LOCKED dependency.
- One request has the existing `RetryTimes + 1` actual-send budget. No member repeat, infinite retry, cross-tenant/group/model authorization bypass, upstream retry-veto bypass, replay after business output or unsafe asynchronous create replay.
- Rate-limit fallback 60 seconds, ordinary maximum 10 minutes; authenticated declared quota reset maximum 32 days; un-timed confirmed billing refusal 1 hour. Manual pause outranks automatic recovery. Account, quota-group and operation/model scopes remain distinct.
- Account keys stay in existing credential stores. State rows and public/admin projections expose no raw credential, raw body, provider account identity or credential fingerprint. Member indices/opaque IDs are safe management handles only.
- Embedding model identity/version, dimensions and input semantics remain stable across account rotation. Existing SuperWagie indexes are not rebuilt or silently migrated.
- PaddleOCR requires v2 multipart job creation, polling and JSONL/result preservation, with owner/member binding. Ordinary OCR chat is a different contract.
- Supplier model recommendations are not evidence of provider/account availability. Add real local HTTP fixtures for every advertised operation; actual purchased-account acceptance remains separately reported.
- No changes to SuperWagie source/configuration, no real provider charges, deployment, shared remote push or user data migration. Local implementation, tests, commits and integration are authorized.
- Root owns ledger, integration and source commits unless a task explicitly delegates committing; workers never spawn subagents. Source ownership is sequential. Reviews do not rerun unchanged passing tests without a concrete new concern.

## Task 1: Persistent scoped account health and selection core

**Files:** Create `model/upstream_account.go`, `model/upstream_account_test.go`, `service/accountpool/policy.go`, `service/accountpool/policy_test.go`; modify `model/main.go` migration lists. Add dialect integration tests beside the model.

**Interfaces:** Produce public Go types `model.UpstreamAccountRef` (pool/member/channel/key-index/quota-group, no credential), `model.UpstreamAccountScope` (operation/model, shared group), `model.UpstreamAccountSelection` (ref, selected key index, selected timestamp), and `model.UpstreamAccountUnavailable` (earliest retry time). Provide deterministic selection/state methods taking context and explicit `now`, with a repository DB parameter where needed for tests. Expose `service/accountpool.ParseRetryAfter(value string, now time.Time) time.Duration` and structured failure classification without a Gin dependency. Record the exact finalized signatures in the report for Task 2.

- [ ] Add a deterministic SQLite fixture with 3 opaque members; select A/B/C, cool A for one operation, prove text still eligible, expiry re-admits A, pause persists, invalid credential isolates only A, shared quota group blocks configured siblings, stale success cannot clear a newer failure.

```go
require.NoError(t, db.AutoMigrate(&model.UpstreamAccountState{}))
assert.NotContains(t, string(publicJSON), "fixture-secret")
assert.Equal(t, wantMember, selected.Ref.MemberID)
```

- [ ] Run the focused tests red before implementing persistence and selection. Use explicit clock values, real GORM state, no sleeps/random stress.
- [ ] Implement safe stable member IDs, scoped state, compatible atomic updates, pause/resume/recovery, shared quota settings, and cursor/least-recent-selection coordination across independent DB handles. Do not hold a DB transaction during network I/O; use bounded index lengths that fit minimum MySQL configuration.
- [ ] Implement Retry-After numeric/date parsing and saturation, fixed structured refusal categories, and no failure on generic 403. Make persistence failures observable and fail closed; do not silently disable healthy siblings.
- [ ] Run focused tests and race; report signatures, migration changes, red/green evidence and safe pool/group ID derivation. Root reviews and commits.

## Task 2: Actual relay member selection, pool failover and resource ownership

**Files:** Modify `model/channel.go`, `middleware/distributor.go`, `controller/relay.go`, `relay/channel/api_request.go`, `relay/channel/gateway/{keypool.go,adaptor.go}`, `relay/common/relay_info.go`, `constant/context_key.go`, `model/task.go`, `service/task_polling.go`; create focused selection, HTTP failover and task-owner tests. New lifecycle logic belongs in focused `service/accountpool`/relay helper files, not a large controller block.

**Interfaces:** Consume Task 1 types and policy. Selection produces an immutable sent-member receipt in request context/RelayInfo. Before non-2xx early return, shared transport records the actual member outcome using the receipt, operation and upstream response headers. Controller asks whether an untried authorized member remains before advancing a route target. Task private data stores stable account reference and safe credential ownership necessary for polling, not browser-readable data.

- [ ] Reproduce with loopback HTTP: A returns 429 with Retry-After, B returns success. Use real middleware/controller/provider path, real billing session and SQLite. Assert exactly A/B sends, one final response and one settlement; next request skips A.
- [ ] Exercise explicit route with one channel/two members, random mode with attempted-member exclusion, all-cooled Retry-After, retry budget zero, no retries after body output, cancellation, local quota failure and explicit upstream `X-Should-Retry:false`.
- [ ] Make native multi-key and Gateway platform keys consume the same health policy, retain raw App-ID/token credential representation, and repair Gateway's error-before-DoResponse path. Replace unsupported SQL locking with compatible model-owned GORM selection. A BYOK rejection must not choose a platform key.
- [ ] Bind existing async tasks to the sent member and resolve refreshed same-identity credentials. Never concatenate a multi-key blob into polling auth or rotate a task to an unrelated account. For unsafe create failures return execution uncertainty rather than automatically resubmit.
- [ ] Verify focused packages/race, exact balances/refunds, state across cache refresh and two selector instances. Report added files and integration branches; root reviews and commits.

## Task 3: Multimodal model metadata and vector-space admission

**Files:** Modify `service/modelcatalog/{catalog.go,runtime.go}`, `controller/client_model_route.go`, relevant DTO/relay validation helpers; new metadata tests and real vector response tests; modify model-route frontend fields later in Task 9.

**Interfaces:** Existing route `Metadata` JSON gains optional `input_modalities`, `output_modalities`, `operations`, `streaming`, `async`, `media_limits` and `embedding_space` with fixed model/version/provider/declared dimensions and input semantics. Absent fields preserve legacy behavior; explicit declarations are enforced before preconsume/send. Expose normalized capability metadata through the authorized catalog only.

- [ ] Add fixtures rejecting image input for explicitly text-only routes, invalid/negative limits, advertised unsupported operation, and embedding route candidates with different model/declared vector-space identities. Metadata omission remains backwards compatible.
- [ ] Add exact embedding fixtures for requested 1024 dimensions, a batch with stable indices, invalid dimensions, missing/duplicated indices, nonfinite values and declared-space mismatch. Preserve existing numeric output and usage billing.

```go
assert.Equal(t, []int{0, 1}, []int{result.Data[0].Index, result.Data[1].Index})
require.Len(t, result.Data[0].Embedding, 1024)
```

- [ ] Implement typed metadata normalization and operation/media admission, including video/audio-in-chat distinctions and bounds. Retain auth/pricing model identity and routing scope. Actual opaque provider weight revision cannot be inferred: enforce declared configuration and known version, never fabricate proof.
- [ ] Run metadata/embedding/tool-policy and controller regressions; root reviews and commits.

## Task 4: SuperWagie Embedding, Ali rerank and compatible routes

**Files:** Modify `relay/capability/{operation.go,defaults.go}`, `router/relay-router.go`; create `middleware/knowledge_compat.go` or equivalent focused translator, tests; modify `relay/channel/ali/{adaptor.go,rerank.go}` and affected DTO validation only as needed.

**Interfaces:** Serve protected standard `/v1/embeddings`, `/v1/rerank` plus `/compatible-mode/v1/embeddings`, `/compatible-mode/v1/chat/completions`, and Ali `/api/v1/services/rerank/text-rerank/text-rerank`. Native Ali request maps to canonical Rerank request before existing distribution/admission, canonical response maps back to `output.results` only for the compatibility path. Preserve query/document ordering, top_n and return_documents.

- [ ] Red fixtures send exact SuperWagie shapes to a real Gin router and loopback Ali server. Assert native input/output compatibility and common bearer auth; unauthorized tokens/groups and malformed nested input must reject before sends.
- [ ] Enable Ali rerank only after exercising its actual converter; replace direct encoding/json calls in touched business paths. Return structured errors consistently, keep proper usage and quota. Embedding dimensions must pass through unchanged.
- [ ] Probe results must parse actual vectors/rank indices, not merely list models. Run affected router/middleware/relay/controller and billing tests; root reviews and commits.

## Task 5: PaddleOCR supplier adapter and durable owned jobs

**Files:** Create `relay/channel/paddleocr/` protocol client and tests; `model/ocr_job.go` and tests; migrations in `model/main.go`; new `service/ocr/` submission/status/result logic. Add one new explicit PaddleOCR channel identity after 59 without renumbering existing types, with constant/channel map coverage. Public routing belongs to Task 6.

**Interfaces:** A bounded protocol client submits multipart to the configured v2 jobs endpoint, polls by provider job ID and reads JSONL output using the creating account. Persist opaque platform job ID, owning user/token/channel/member, configured model, provider job ID, execution state, result metadata and existing billing-context reference; never expose provider credentials. Use model CAS transitions for submit/settle/refund and idempotent retries. Read exact pinned SuperWagie/backend and official PaddleOCR protocol before implementing field names.

- [ ] Fixture multipart local PDF/URL submission, returned provider job, running/success/failure, multi-page JSONL retaining tables/formulas/page order, credential rotation for the same member, cross-user denial, and deleted member/no substitute.
- [ ] Submission uncertainty persists a nonterminal uncertain state; do not recreate the provider job after a timeout. Duplicate polls/callbacks cannot double-settle/refund. Bound pages/input/result bytes and validate download destinations/redirects through existing controlled HTTP rules.
- [ ] Integrate checked page/request billing using an explicit configured price and existing atomic preconsume session; missing required pricing fails before supplier call. Read `pkg/billingexpr/expr.md` before touching any expression paths. No guessed page counts or invented confidence/bbox fields.
- [ ] Run protocol/model/service tests, race and model migrations. Report exact caller-facing interfaces for Task 6; root reviews and commits.

## Task 6: OCR public compatibility, canonical API and billing integration

**Files:** Create `controller/ocr.go`, `router/ocr-router.go`, OCR DTOs and tests; modify route/capability manifests and managed operation permission/billing setup; register router in `router/main.go`.

**Interfaces:** Protected PaddleOCR `/api/v2/ocr/jobs` creation/status/result paths preserve the fixed downstream contract, with platform IDs and scoped access; canonical LLMRouter `/v1/ocr` exposes declared synchronous/async behavior over the same owner service. Capability selection and billing derive from authorized user/model/channel, never client-selected arbitrary provider URL.

- [ ] Red router tests replay SuperWagie's submit/poll/download sequence; assert multipart, model, auth and returned JSONL. Add canonical OCR typed-result fixture. No raw upstream account IDs/keys may reach caller or logs.
- [ ] Exercise correct fixed member across A/B rotation, task owner identity, token permissions, 400 bounds, insufficient wallet, 429 all-cooled, uncertain submit and repeated settlement.
- [ ] Run server/router/controller/service/race and validate both API contracts independently. Root reviews and commits.

## Task 7: Domestic images, audio, video-understanding adapters and safe media replay

**Files:** Modify appropriate `relay/channel/{ali,minimax,volcengine,zhipu_4v}` adapters, `relay/capability/defaults.go`, media DTOs/converters, audio/image relay helpers. Create protocol-level fixtures in owning packages; preserve generic adaptor interface.

**Interfaces:** At least one fully tested domestic path per image understanding/generation/editing, ASR, audio understanding, TTS and video understanding. Qwen-compatible ASR routes `/v1/audio/transcriptions` to the actual supported provider audio contract; native MiniMax TTS preserves voice/encoding/stream options; Chat/Responses media retain video/audio data and supported fps/detail fields. All use Task 2 receipts and operation-level health.

- [ ] Verify current official primary-source contracts before selecting model/version-specific native payloads; record immutable fixtures/docs links. Do not make registration alone a support claim.
- [ ] Red actual HTTP fixtures: image understanding vs output image; generation plus reference edit; WAV multipart to ASR text/timestamp outputs; TTS MP3 and streaming; audio semantic question; video URL+fps+question with preserved input, separately declared audio-track support.
- [ ] Add exact operation declarations only for tested branches; reject unsupported format/size/duration/model before charging/sending. Unsafe image creation replay is denied after unknown outcome; authorized media body replay remains bounded.
- [ ] Native streaming must use shared stop/join/writer/delivery policy and no post-output account switch. Run owning adapter, helper/controller/capability/converter suites and race; root reviews and commits.

## Task 8: Multimodal Embedding contract

**Files:** Create multimodal-vector DTO/validator and relay helper; extend Ali adaptor or focused native client, route/capability declarations and tests. Do not weaken text Embedding parsing to accept arbitrary nontext input silently.

**Interfaces:** Explicit `/v1/multimodal/embeddings` and documented Ali-compatible path accept declared text/image/video items with model/dimensions, return ordered vectors with a separate embedding-space identity, and use account selection and existing checked usage billing. Text vector routes never silently switch to this space.

- [ ] Read current official Qwen-VL embedding contract, implement bounded types/allowed parameters and actual native response mapping. Fixture text+image, video-supported declaration, unsupported audio/model, batch/index preservation and dimension mismatch.
- [ ] Verify existing text-embedding-v4/1024 tests unchanged; different spaces require new public IDs, no user index mutation.
- [ ] Run vector/controller/adapter/price-boundary tests and race; root reviews and commits.

## Task 9: Admin account-pool and multimodal catalog UI

**Files:** Create focused `controller/account_pool.go`, protected admin routes in `router/channel-router.go`, API tests; enhance existing `web/src/features/channels/components/dialogs/multi-key-*`, channel API/types; update `web/src/features/client-model-routes` metadata editor/projection; locale JSON/static keys and owning test directories.

**Interfaces:** Admin-only list/recovery/quota-group actions use stable opaque member handles, return available/cooling/isolated/paused counts, reason codes and retry times. Model metadata fields expose input/output, supported operation/constraints and embedding-space configuration. Never return fingerprint/key or provider error body. Respect existing ChannelRead/Operate/Write permissions and audit.

- [ ] Red API permission tests and frontend user behavior tests cover status counts, expiry display, resume mutation failure, loading/empty/error states, quota group edits and embedding identity validation. User-facing strings use English-source i18n keys.
- [ ] Read relevant UI/i18n project skills and `web/AGENTS.md`; reuse existing UI components and API/query patterns, with no new dependency unless demonstrably required.
- [ ] Run affected test files, `bun run typecheck`, changed-file lint/format and i18n checks. Root reviews and commits.

## Task 10: Cross-database, SuperWagie contract certification and local delivery

**Files:** New focused cross-dialect test harness under `scripts/` and integration tests beside owners; `docs/operations/domestic-model-account-pools.md`; this plan/spec status. Preserve per-task evidence in this plan's SDD workspace.

**Interfaces:** Use unique temporary Docker databases without touching existing user services; test account state, selection, pause/recovery, new job CAS/migrations on SQLite/MySQL/PostgreSQL. Loopback supplier servers replay exact SuperWagie wire contracts, never real customer documents or keys.

- [ ] Run end-to-end document/OCR -> embedding -> query -> rerank -> cited passages contract, no mutation of the real SuperWagie installation. Include failed account then healthy sibling, fixed embedding identity, OCR ownership and one settlement.
- [ ] Validate minimum database dialect features through MySQL 5.7/PostgreSQL 9.6 where images run; if platform runtime prevents these versions, record exact tested newer version and remaining limit, do not fake a minimum-version pass.
- [ ] Run `GOTOOLCHAIN=go1.26.10 bash scripts/verify-backend.sh`, frontend build/typecheck/lint/format, affected release checks and two-instance state fixtures. Stop for real failures and fix them through task owners.
- [ ] Obtain final whole-branch independent review on the most capable available model, resolve findings, record exact evidence and runtime limits. Mark task completion only with passing checks and acceptance mapping.
- [ ] Commit delivery docs and locally fast-forward original `codex/wechat-native-pay` only if it remains at the known unchanged base with no tracked edits; retain original untracked files. No remote push, deployment or live paid calls.

## Spec coverage self-review

| Spec requirement | Tasks |
| --- | --- |
| Stable identity, scoped health, shared quota, expiry, two instances | 1, 2, 9, 10 |
| Bounded safe retries, Gateway early errors, BYOK, continuity, async ownership | 2, 5, 6, 7 |
| Model/operation/media catalog and admission | 3, 7, 8, 9 |
| SuperWagie text Embedding, Ali Rerank, PaddleOCR jobs | 3, 4, 5, 6, 10 |
| Images, ASR, audio understanding, TTS, video understanding | 7, 9, 10 |
| Multimodal vectors and index stability | 3, 8, 10 |
| Accounting, database compatibility, admin auth/i18n, full verification | Every relevant owner, then 9 and 10 |
