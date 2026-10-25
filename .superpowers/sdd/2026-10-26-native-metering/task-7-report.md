# Task 7 final report

Implemented metered_v2 platform admission/routes for chat, responses, embedding, speech, transcription, image generation/edit, OCR, video. Legacy exclusive_v1 Chat remains compatible unless explicitly opted into an operation pin. UniHub rate/usage wire shapes are shared metering shapes; frozen full rate and funding snapshots, operation/body/media digest, actor/account/origin/task identity, reservation caps, and terminal intents are persisted. No credentials or raw media enter platform call rows.

Server configuration: UNIHUB_OPERATION_RATE_IDS and UNIHUB_OPERATION_LIMITS in internal/platformbilling/config.go. OperationLimits in operation.go contains max_body_bytes, max_quantity, policy, provider_unit_cap, provider_unit. Provider-reported speech requires an explicit cap; video provider units use a separate pinned namespace cap. Evidence exceeding frozen caps remains incomplete. Image tiers are validated before reserve.

Every synchronous helper fences platform sends. Controller constructs funding-neutral NativeBillingContext from UniHub price. Async OCR and video use the same platform callback and exclude local billing. Scoped status/result routes require fresh credential with exact actor/account/origin/task/call scope. Unsupported cancellation returns 501 and retains hold.

Concurrency/recovery fixes:
- ObserveMetering locks the call via write-first transaction before comparing evidence; contradictory concurrent final receipts persist a conflict marker and cannot be silently overwritten.
- OCR workers recover binding from durable OCRJob provider ID before observation.
- OCR 401/429 typed refusal persists a failure decision, disables platform retry, moves to platform_pending and releases once without requiring a nonexistent provider job ID.
- Video task creation and call binding commit together; insert failure rolls back binding. No uncertain provider request is replayed.
- Release only follows authoritative no-output evidence. Missing/invalid/incomplete evidence holds.

Validation (all local fixtures; no live providers/payments/deployments):

`go test ./router ./controller ./relay ./internal/platformbilling ./service/ocr -count=1` PASS.

`go test -race ./internal/platformbilling -run 'TestConcurrentMetered|TestTaskBinding|TestRecoverDurable|TestMetered' -count=1` PASS.

`TestPlatformMeteredSyncActualUsageAndReplay`: actual Gin router/distributor/controller -> fake supplier HTTP -> UniHub HTTP -> SQLite call lifecycle for all nine operations; seven sync settle, OCR/video create/poll settle, OCR/video explicit no-output release, OCR 401 and 429 refusal release, duplicate logical calls do not resend, wrong-account status/result/cancel rejected, supported owner cancellation returns 501. Local user quota/used quota/request count remain unchanged.

`TestConcurrentMeteredReceiptsPersistConflict`: independent concurrent sessions retain contradictory final evidence.
`TestTaskBindingAndBusinessInsertAreAtomic`: injected task insert failure rolls back provider binding.
`TestRecoverDurableProviderBindingBeforeObservation`: durable worker binding is idempotent and rejects a different provider ID.
`TestMultipartDigestIgnoresBoundaryAndRejectsDuplicate`: stable multipart digest and duplicate rejection.

Latest fixes/tests: 7d918dbd3, cd164a743, c977ce68a, f8732e627. Full commit history includes the earlier admission, routing and provider-fence commits. Independent final review requested; no production acceptance claimed.
