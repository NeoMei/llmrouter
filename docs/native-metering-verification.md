# Native metering verification — 2026-10-26

Implemented in the `codex/native-metering-v2` branches of LLMRouter and UniHub. Existing legacy pricing remains unchanged until a model/operation explicitly uses `metered_v2`. No production deployment, live price publication, or supplier billing call was performed.

## Delivery

The common exact-quantity contract supports token, character, audio-second, image, request, video-second and namespaced supplier units. LLMRouter observes actual usage and owns local billing journals; UniHub owns platform rate snapshots, funding allocation, charge calculation and settlement. Both use the same golden metering contract, without converting media use into invented tokens.

The six requested modes are represented: chat/embedding actual supplier tokens; TTS characters; STT seconds; successful images by size tier; successful OCR requests (page billing disabled); and video successful seconds or pinned supplier units. Support is explicit per adapter and measurement policy. See [coverage and configuration](native-metering-coverage.md).

## Executed verification

| Layer | Evidence | Result |
| --- | --- | --- |
| LLMRouter backend | Full `go test ./... -count=1`, plus final native/metered/platform package reruns | Passed |
| Concurrency | Platform evidence conflict, atomic task/binding and recovery tests with `go test -race` | Passed |
| Portable database journals | Dedicated SQLite, MySQL 5.7 and PostgreSQL 9.6 containers; migration twice, positive/zero settlement, restart replay, admin release and receipt projection | All three passed; temporary containers removed |
| Router to supplier protocol | Real Gin/middleware/controller/provider HTTP fixtures for chat, Responses, embedding, speech, transcription, image generation/edit, OCR and video | Passed; one send, replay rejection, account isolation and unchanged local platform-principal balances/counters |
| Async lifecycle | OCR/video submit, poll, settle, no-output release; OCR 401/429 refusal; lost binding recovery | Passed |
| UniHub accounting | Full `go test ./...` using isolated PostgreSQL schemas and Formance ledgers | Passed |
| UniHub native HTTP | `TestNativeMeteredUnitsAndAssetsHTTPAcceptance`: eight operation/unit cases × CNY/6 and CREDIT/6 | 16 cases passed; exact rounding, final receipt replay, changed-command conflict, final balances |
| SDK | TypeScript strict compilation and Node SDK tests | 14 tests passed |
| Frontend | Typecheck, six native contract tests, production build, scoped lint | Passed; lint has no errors |
| Browser | `bunx playwright test --config playwright.native.config.ts` against local real React pages with test API responses | 2 passed: image-tier save, separate cap save, pending vs confirmed-zero receipts |
| Independent final source review | Rechecked corrected accounting, dispatch, recovery, conflict and policy-admission paths | No remaining reproducible P1 finding within declared support scope |

The important regression cases include missing vs explicit zero, malformed/duplicate/conflicting receipts, incomplete streams, quota saturation, frozen prices, local/platform funding separation, over-cap supplier quantities, 60-second pre-dispatch recovery, rejected reservations, pagination past failed intents, and stream-request image JSON fallback.

## Operational boundaries

- All newly introduced prices and caps require operator configuration. The offline migration script produces disabled proposals only.
- STT server measurement currently accepts bounded PCM WAV; another codec needs its own certified measurement path or supported provider receipt admission.
- Remote image dimensions without trusted output metadata remain pending; requested dimensions are never substituted as measured output.
- Video generic/Veo policies and unsupported multiple outputs reject before reservation. Sora, Kling, Doubao and Ali use the explicitly named receipt policies in the coverage matrix.
- A process/network failure after possible supplier acceptance but before a usable response is durably recorded remains `UNKNOWN`/pending. Automatic replay cannot prove no output; the call requires reconciliation rather than a blind resend or refund.
- Provider cancellation is not generally available and returns an explicit unsupported response. A local cancel is not treated as proof of a supplier refund.

Automated protocol fixtures and local browser acceptance are complete for the documented paths. Real supplier/account compatibility and production rollout remain separate acceptance steps.
