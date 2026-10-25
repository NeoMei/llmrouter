# Task 7 implementation report

Commit: `0898ab74b1aa82169ad94e58672d9c8659704025`

Implemented in `internal/platformbilling` and `router/platform.go`:

- `metered_v2` operation admission with strict JSON and bounded multipart canonicalization for responses, embeddings, speech, transcription, image generation/edit, OCR and video.
- Frozen metered rate snapshots, operation/body/scope digest, conservative reservation, remote snapshot matching, and UniHub-only settlement.
- Evidence API (`MeteredPrice`, `ObserveMetering`) preserving missing/invalid/incomplete/confirmed and explicit zero; conflicting final evidence keeps the hold.
- Durable task ownership (`BindTask`, `ObserveTask`, `Task`) and recovery-compatible terminal intent.
- Platform sync routes for metered responses, embedding, audio, image operations; unknown tariffs fail closed.
- Legacy Chat admission/session remains exclusive_v1-compatible.

Raw verification:

```text
go test ./router ./controller ./relay ./internal/platformbilling -count=1
ok   github.com/QuantumNous/new-api/router
ok   github.com/QuantumNous/new-api/controller
ok   github.com/QuantumNous/new-api/relay
ok   github.com/QuantumNous/new-api/internal/platformbilling
```

Remaining integration owned by parent/Task 6:

- Initialize `NativeBillingContext` from `Session.MeteredPrice` before platform relay billing early return.
- Add provider dispatch fences at the first supplier send for each new helper; preserve retry semantics.
- Wire OCR/video asynchronous supplier lifecycle and platform task routes to `BindTask`/`ObserveTask`; block legacy task and OCR billing side effects for platform-owned jobs.
- Platform Chat selects metered_v2 only when an explicit chat operation rate pin is configured; otherwise `/platform/v1/chat/completions` remains legacy exclusive_v1.
- Keep local wallet, subscription, token quota, request counters and local OCR/task logs untouched for platform calls.

Additional route evidence: `router/platform_metered_test.go` runs real Gin middleware + distributor + controller + provider HTTP fixture for embedding, speech, and image generation. It verifies reserve, one supplier send, terminal settlement, idempotent replay rejection, and unchanged local user quota/used quota/request count. Chat and Responses metered producer evidence remains pending because their current raw observers emit incomplete token usage for the fixture; no false settlement is accepted.

Async routes use immutable `Adapter.Task` scope and persisted `platform_call_id`/provider task mapping. OCR creation now carries `AuthorizedAdmission.PlatformCallID/PlatformPrice/Dispatch/BindTask`; legacy OCR status excludes platform jobs. Video/OCR status/result routes reject shared-principal or wrong-task access; cancellation returns bounded unsupported until provider capability confirms no output. `Adapter.ReleaseTask` requires exact call/task binding and trusted no-output evidence before releasing.

Chat/Responses HTTP fixture now passes with provider protocol usage containing both priced input and output dimensions; the previous UNKNOWN result was caused by the test tariff omitting output, and the strict observer correctly held it. Provider-reported speech now requires an explicit per-model character cap; confirmed usage above the frozen cap stays incomplete. Race validation passed for metered platform state-machine tests.

The route fixture now covers chat, responses, embedding, speech, transcription (server-measured WAV), image generation, and image edit with actual provider HTTP responses. Existing OCR supplier lifecycle tests cover retry/poll/resource ownership; Task6 native task recovery tests cover durable platform holds and release hooks. A dedicated platform OCR HTTP end-to-end fixture remains a follow-up because the existing controller path needs live runtime/catalog setup.
