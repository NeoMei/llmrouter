# Native Metering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development. Controller runs execution and review in this session.

**Goal:** Ship the six approved billing modes in LLMRouter local and UniHub platform paths.
**Architecture:** A dependency-free metering contract carries validated quantities and frozen unit prices. Supplier evidence feeds it; existing funding owners execute durable idempotent settlement. UniHub remains platform money authority.
**Tech Stack:** Go/GORM/Gin, PostgreSQL/pgx, Redis, React/TypeScript/Bun.
**Spec:** ../specs/2026-10-25-native-metering-design.md (approved 2026-10-26).

## Global Constraints

- Actual usage missing, invalid, or conflicting stays pending; it never becomes a final estimate or automatic refund.
- Explicit zero usage is valid and can settle to zero.
- Price, unit, scale, rounding, funding source, subscription period, and reservation IDs are frozen at admission.
- Existing legacy token and per-request configurations keep their current meaning until an explicit unit version is enabled.
- Local quota conversions use common Quota*Checked helpers and retain saturation audit markers.
- Platform traffic never debits LLMRouter local wallet, subscription, token quota, or request counters.
- LLMRouter business JSON uses common wrappers. GORM changes support SQLite, MySQL >= 5.7.8, PostgreSQL >= 9.6; TEXT stores portable JSON.
- LLMRouter Go tests use testify require/assert. Follow UniHub's existing test harness without gratuitous dependency changes.
- OCR is successful-request billing in this release. Page billing remains disabled.
- No live prices, provider requests, payments, deployments, pushes or shared-branch merges are implied by local implementation.

## Shared wire contract

Create dependency-free `pkg/metering` in LLMRouter and `internal/metering` in UniHub with identical JSON wire shapes and golden fixtures. Do not import one repository or its business packages from the other.

```go
type Quantity struct {
 Value string `json:"value"` // canonical nonnegative integer, <= 38 digits
 Scale int `json:"scale"` // 0..9; value/10^scale
}
type Rational struct {
 Numerator string `json:"numerator"`
 Denominator string `json:"denominator"`
}
type Term struct {
 Key string `json:"key"`
 Unit string `json:"unit"`
 ProviderUnit string `json:"provider_unit,omitempty"`
 Price Rational `json:"price"` // per base unit, authority's monetary unit
 Step *Quantity `json:"step,omitempty"`
 Minimum *Quantity `json:"minimum,omitempty"`
}
type Price struct {
 Version string `json:"version"` // metered_v2
 Operation string `json:"operation"`
 Policy string `json:"policy"`
 Terms []Term `json:"terms"`
}
type Measurement struct {
 Key string `json:"key"`
 Unit string `json:"unit"`
 ProviderUnit string `json:"provider_unit,omitempty"`
 Quantity Quantity `json:"quantity"`
}
type Usage struct {
 Version string `json:"version"`
 Operation string `json:"operation"`
 State string `json:"state"` // confirmed, missing, invalid, incomplete
 Source string `json:"source"`
 Measurements []Measurement `json:"measurements"`
 ProviderRequestID string `json:"provider_request_id,omitempty"`
}
func ValidatePrice(Price) error
func ValidateUsage(Price, Usage) error
func Calculate(Price, Usage) (*big.Rat, error)
```

Allowed units: token, character, audio_second, image, request, video_second, provider_unit. Operations: chat, responses, embedding, speech, transcription, image_generation, image_edit, ocr, video. No OCR page term. Keys are stable tariff dimensions (input/output/cache_read/cache_write/reasoning for tokens; characters/audio/images or size-tier keys/requests/video/provider). Unique keys; non-token single-unit operation policy may have image tier terms. Required terms must be reported including explicit zero; no unknown dimensions. Provider unit requires a nonempty namespace. Integers (token, character, image, request) require scale=0. Quantities, positive denominators, bounded terms (<=64), known operations and operation/unit compatibility are validated.

Calculator sums exact rationals. Step/minimum only affect positive quantity, never confirmed zero. Rounding to money happens once at each authority's boundary. UniHub prices are in asset micro-units, local prices are currency base units with frozen QuotaPerUnit/group conversion. Shared fixtures include characters 1500*1/1000=3/2; seconds 1001/1000*2=1001/500; zero; invalid mismatch; unknown/missing/duplicate term.

## Task 1: Exact metering contract in both repositories

**Files:** LLMRouter `pkg/metering/{metering.go,metering_test.go,testdata/v2.json}`; UniHub `internal/metering/{metering.go,metering_test.go,testdata/v2.json}`.
**Produces:** Shared wire contract above. Avoid dependency cycles with common/types/model.

- [ ] RED: add table tests for six billing families, scale preservation, step/minimum, zero, duplicate/missing/unknown keys, invalid unit/operation, 39 digits, negative, and zero denominator.
- [ ] Run `go test ./pkg/metering` (LLMRouter) or `go test ./internal/metering` (UniHub); record expected failure.
- [ ] GREEN: implement parsing with big.Int/big.Rat, validation and pure Calculate. Preserve exact fractions.
- [ ] Run the focused suite and compare both golden fixtures byte-for-byte.
- [ ] Commit each repository independently.

Example assertion:
```go
got, err := Calculate(price, usage)
require.NoError(t, err)
assert.Equal(t, "3/2", got.RatString())
```

## Task 2: UniHub metered price publication, settlement, SDK and API

**Files:** `internal/billing/{types.go,calendar.go,catalog.go,credits.go,query.go}`, `internal/httpapi/{billing.go,model.go}`, `packages/sdk`, OpenAPI owner.
**Consumes:** internal/metering.Price, Usage, Calculate.
**Produces:** Optional `Rate.Metered *metering.Price json:"metered,omitempty"` and `Usage.Metered *metering.Usage json:"metered,omitempty"`. New rates use Semantics="metered_v2"; exclusive_v1 bytes and idempotency remain compatible.

- [ ] RED: extend charge/publication tests for metered_v2 and all old exclusive_v1 cases; reject mixed legacy/new payloads, wrong operation, duplicate terms, invalid decimals.
- [ ] Validate complete rate at publish; settle from the frozen rate, ceil exact summed charge once, preserve existing held cap/platform difference and independent provider cost.
- [ ] Preserve nil-usage MISSING_USAGE and immutable settlement digest; repeated same command returns original receipt.
- [ ] Expose typed rate/measurement fields in OpenAPI/SDK/query receipts. No silent conversion between CNY/6 and CREDIT/6.
- [ ] Run `go test ./internal/metering ./internal/billing ./internal/httpapi ./internal/modelaccess -count=1`, SDK checks, and existing opt-in database harness when available.
- [ ] Commit and report verification and any unavailable integration prerequisites.

## Task 3: LLMRouter native pricing snapshots and durable funding

**Files:** `setting/billing_setting/native_metering.go`; option registration/controller; `relay/common/relay_info.go`; `relay/helper/price.go`; `model/native_billing.go`; `service/native_billing.go`; tests.
**Consumes:** pkg/metering.
**Produces:** Versioned option `ModelNativePricing` keyed by model then operation, with frozen Price and measurement policy. RelayInfo holds NativeBilling snapshot/evidence; service provides BeginNativeBilling, RecordNativeUsage, FinishNativeBilling. Platform delegates evidence without local funding.

- [ ] RED: test settings validation, quota exactness/zero, frozen group/price, missing usage hold, duplicate settle/refund, restart and partial commit recovery.
- [ ] Implement stable call/attempt IDs, frozen contract/funding/subscription generation, durable intention and per-component receipt. Reuse existing reservation interfaces carefully; do not retry ordinary non-idempotent updates blindly.
- [ ] Wire admission before upstream send; errors after uncertain dispatch keep a visible pending hold. Definite pre-dispatch failure releases.
- [ ] Add bounded recovery and admin-only list/read/resolve endpoints for confirmed measurement, definite no output, and audited operator loss release; permissions/idempotency tests.
- [ ] Route native logs through actual receipts; legacy paths remain unchanged without explicit new config.
- [ ] Run `go test ./setting/billing_setting ./model ./service ./controller ./relay/helper -count=1`.
- [ ] Commit.

Example behavior:
```go
// A missing final observation cannot change the reserved balance.
require.NoError(t, recordMissing(callID))
assert.Equal(t, "pending_measurement", reload(callID).State)
assert.Equal(t, heldBalance, userBalance())
```

## Task 4: Actual token evidence for chat and embedding

**Files:** dto billing_usage, service billing_usage/text_quota, protocol raw usage parsers OpenAI/Responses/Claude/Gemini, ordinary/multimodal embedding; tests.
**Consumes:** Task 3 snapshot and RecordNativeUsage/FinishNativeBilling.
**Produces:** Confirmed canonical exclusive token measurements preserving original provider semantics.

- [ ] RED: actual != estimate, explicit all-zero, absent/null/negative/overflow/duplicate, cache-only Claude and Gemini reasoning, SSE cumulative usage, embedding batch total once.
- [ ] Capture presence at raw provider boundary before any fallback or conversion; use existing BillingUsage originals. Native mode never charges estimates.
- [ ] Persist incomplete/conflicting evidence and avoid refund on client failure after provider-confirmed usage.
- [ ] Keep billingexpr v1 normalization intact; configured token-native paths use frozen prices and evidence.
- [ ] Run `go test ./dto ./service ./relay ./relay/channel/openai ./relay/channel/claude ./relay/channel/gemini -count=1`.
- [ ] Commit.

## Task 5: Audio and image measurements

**Files:** relay audio/image handlers, helper domestic_audio/domestic_image, provider OpenAI/MiniMax/Volcengine/Ali audio and image adapters, native measurement helpers; tests.
**Consumes:** Task 3 native service; shared contract.
**Produces:** character/audio_second/image dimension measurements.

- [ ] RED: multilingual scalar counts including emoji, provider-vs-request character delta, STT 0.1/1/1.001 seconds; 4 requested/2 completed images, zero, duplicates/partial events, auto and mismatched dimensions.
- [ ] Count final outbound TTS text only under unicode_scalar_v1; provider_reported_v1 cannot silently fall back. Capture validated terminal MiniMax characters.
- [ ] Decode/validate STT input duration or use provider seconds under the frozen policy; no conversion to fake tokens.
- [ ] Record successful image outputs and actual size tiers, dedupe IDs; native zero is not OtherRatio n=0. Incomplete output stays pending; only finalized partial success releases remaining hold.
- [ ] Match provider contracts against official docs where source semantics are uncertain; bounded media measurement for absent dimensions and duration.
- [ ] Run provider and relay/helper/service suites and commit.

## Task 6: Video actual units and OCR funding integration

**Files:** relay task adapters and TaskInfo, model TaskBillingContext, service task_polling/task_billing; OCR billing/service stores; tests.
**Consumes:** Task 3 durable funding/evidence.
**Produces:** Provider-unit/video-second actual evidence and request-unit OCR; asynchronous recovery independent of generation status.

- [ ] RED: 10s reserved→5s actual; exact small decimal provider units; multiple outputs; zero/missing; price changes; crash between terminal and money; subscription reset; duplicate/colliding terminal observations.
- [ ] Parse actual Sora/Ali duration, Doubao token units, Kling fractional native units; validate echoed-vs-actual semantics using official sources. Veo actual media probe where no receipt duration exists.
- [ ] Persist generation terminal plus settlement intent atomically; recovery includes successful jobs with unfinished billing. No reread of new model/group prices.
- [ ] OCR current unit is one successful request. Separate funding adapter so platform OCR never uses local quota before UniHub settlement; preserve existing OCR idempotency/resource rights.
- [ ] Run `go test ./relay/channel/task/... ./service ./model ./service/ocr -count=1`, race selected idempotency tests, and commit.

## Task 7: LLMRouter platform operation admission and metered routes

**Files:** internal/platformbilling client/adapter/strict parsers, router/platform, controller/helper admission, task/OCR platform adapters; tests.
**Consumes:** UniHub Task 2 API, all verified measurement producers.
**Produces:** Platform routes for chat/responses/embedding/speech/transcription/image generation/edit/OCR/video with account/task scoped query/results.

- [ ] RED: all operations reserve/dispatch/settle once; duplicate logical call never resends; wrong operation/asset/account rejected; incomplete retains hold; local quota unchanged.
- [ ] Extend Rate/Usage with shared metered fields exactly matching UniHub. Admit validated operation/body and server-derived conservative reservation; freeze rate and identity.
- [ ] Preserve JSON strictness; multipart/body storage bounds and stable digest include validated operation and content. Async jobs retain verified ownership and durable service recovery without persisting credentials.
- [ ] Observe actual evidence, persist terminal intent, deliver idempotently. New operation support requires matching metered tariff, not model-name guessing.
- [ ] Run `go test ./internal/platformbilling ./router ./controller ./relay -count=1` and selected race tests.
- [ ] Commit.

## Task 8: UI, migration preview, acceptance and final review

**Files:** web system-settings/models pricing editor, pricing types/API/logs/reconciliation UI, seven locale files; native migration preview; docs coverage matrix; cross-repo test harness.
**Consumes:** ModelNativePricing and receipts, UniHub metered SDK.
**Produces:** Explicit unit editor, exact amounts, native logs/reconciliation states, dry-run migration report and provider coverage matrix.

- [ ] RED: UI contract serialization, unit/tier validation, confirmed zero/pending display and string financial quantities.
- [ ] Expose operation/unit/per-count/step/minimum/version with price preview; platform price belongs to UniHub. Do not silently mutate existing ModelPrice.
- [ ] Migration preview compares actual fixed examples with old/new prices and emits disabled proposals; no live option write.
- [ ] Run `bun run typecheck`, focused tests/lint and build; run backend tests and both repositories' unit/schema tests.
- [ ] Execute isolated actual UniHub HTTP/PostgreSQL/ledger integration with synthetic providers for six units, both assets, replay, loss/restart, and native balance isolation. Record real supplier calls separately and do not fabricate acceptance.
- [ ] Broad final review against approved spec, fix findings, save verification evidence and commit only task-owned files.
