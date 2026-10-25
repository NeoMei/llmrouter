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
