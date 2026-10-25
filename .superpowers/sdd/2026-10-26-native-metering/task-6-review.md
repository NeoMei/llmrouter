# Task 6 implementation review

Reviewed frozen commits `b87c8d9f4` and `81c0ccc49` (with documentation commits `66f15db5d` and `92ef95f61`) in the native-metering worktree. The requested diff was first saved as `review-task6.diff`. This is a code/spec review; no provider calls or broad test reruns were performed.

## Findings

### P1 — failed platform video terminals cannot use the required no-output release path

`prepareNativeTaskIntent` forcibly changes every non-success terminal to `state=incomplete` and removes measurements (`service/task_native_billing.go:29-33`). `settleNativeTask` invokes `PlatformTaskRelease` only when the evidence is `confirmed` with zero measurements (`:57-60`). Therefore a failed video terminal with no output can never satisfy the release condition; it is sent to `PlatformTaskObserver` as incomplete and then left `native_pending` forever (`:61-80`). The same gap exists for platform OCR: `service/ocr/billing.go:155-165` observes a failure as incomplete and explicitly returns without calling `PlatformTaskRelease`. This violates the brief's trusted no-output release requirement and leaves the platform call in a held state unless an external/manual action exists.

### P1 — colliding terminal observations silently discard the losing evidence

Two pollers can read the same in-progress task and fetch different terminal quantities. Both start with an empty `NativeEvidence`, so the comparison in `prepareNativeTaskIntent` cannot detect the collision. The first `UpdateWithStatus` wins; the loser only disables settlement (`service/task_polling.go:652-655`) and never records the conflicting observation in the owner journal. The winning quantity may therefore settle despite observed contradictory evidence. Persist the losing observation/conflict against the current task/native journal before treating this as a harmless duplicate; identical observations must remain idempotent.

### P1 — no multiple-output evidence aggregation

`VideoNativeUsage` extracts one scalar field (`seconds`, `final_unit_deduction`, `completion_tokens`, or one Ali duration) and creates exactly one measurement (`relay/channel/task/taskcommon/native_video.go:34-85`). It does not inspect output arrays or sum successful outputs. The task brief requires multiple outputs; the provider-contract document also calls out Sora extensions/multiple generated media. A provider response with two outputs is therefore either mis-billed from a total scalar or left missing, depending on the provider shape.

### P2 — actual-video receipt support is narrower than the report claims and Veo is silently unsupported

The parser has no `veo_output_probe_v1` branch (`native_video.go:36-67`), so the policy is always `missing`; this is safe (no estimate), but admission accepts the policy through billing-setting validation and the reservation path still permits a configured cap/seconds. The report says Veo is admission-disabled, but the implementation does not enforce that at this parser boundary; a successful Veo job remains permanently pending unless another component rejects it. There is also no bounded media probe implementation in this repository, so actual Veo duration remains unverifiable.

### P1 — recovery starvation after 100 permanently pending tasks

`recoverNativeTasks` repeatedly selects `native_pending=true ORDER BY id LIMIT 100` with no cursor (`service/task_native_billing.go:90-99`). Missing/invalid/failed evidence returns without clearing `native_pending` (`:79-80`). Once 100 earlier tasks are permanently awaiting measurement, newer successful jobs whose process crashed after terminal persistence never reach replay. Separate measurement holds from retryable delivery, or paginate fairly.

### P1 — duplicate receipt fields are accepted as confirmed usage

`VideoNativeUsage` uses `common.Unmarshal` rather than the existing duplicate-rejecting `common.UnmarshalUnique` (`native_video.go:30`). A receipt such as `{"status":"completed","seconds":"5","seconds":"0"}` therefore confirms the last value and can settle to zero instead of pending conflicting evidence. Reject duplicate JSON keys, including nested usage fields.

## What is implemented / supported

- Video reservation uses bounded request seconds or configured provider caps; final local settlement parses successful Sora, Kling, Doubao, and model-specific Ali receipts. Exact decimal quantities, explicit zero, missing/invalid states, and queued/non-terminal responses are represented in fixtures (`native_video.go:69-89`).
- Native price and reservation inputs are serialized in the task/native billing contract; the settlement path does not look up current prices. OCR/native underlying operations retain subscription generation and immutable reservation data.
- Platform OCR admission sets `Funding=platform`, quota zero, and skips local wallet/token operations; successful OCR emits one `request=1` measurement. The included test covers no local balances/request count/audit operations.
- Task terminal status and native evidence are normally written through the guarded `UpdateWithStatus` path before settlement, and `recoverNativeTasks` scans `native_pending` rows at the beginning of task polling.

## Spec/quality gaps and unverifiable claims

- Provider contract evidence supports the documented receipt fields, but no live provider/native-media probes were run. Sora completed retrieval shape, Ali model/version-specific schemas, Kling final-unit precision/rounding, and Veo output duration remain acceptance items rather than production evidence.
- The claimed RED coverage for duplicate/colliding terminal observations and crash-after-terminal-money is not demonstrated for task video; only the separate native/OCR ledger tests cover some idempotency cases.
- The report's statement that platform adapters "must install" release is true as an integration prerequisite, but the repository's OCR failure path never invokes that release hook.

## Review outcome

The implementation has a sound frozen-contract direction and safe pending behavior for missing/invalid successful evidence, but it is not complete against Task 6 acceptance. The P1 lifecycle defects (no failed platform release, silently discarded colliding observations, recovery starvation, and duplicate receipt fields) plus lack of multiple-output aggregation should be fixed before treating Task 6 as complete. `review-task6.diff` is retained in the artifact directory as requested.

## Integration clarification

The current OCR worker does recover ordinary successful platform jobs after intent persistence: submitted/done jobs with empty result_resource_id and non-settled billing satisfy its existing query. No finding is asserted for that normal crash window. Platform observer replay after a lost acknowledgement is expected and delegates idempotency to the durable platform Call journal; it is not independently treated as a defect. Subscription reset and price immutability rely on the shared NativeBilling/OCR ledger; code retains generations and serialized prices, but task-specific crash/reset integration tests were not found.

## Follow-up fixes (commits 273461dc0, d00b1d2a6)

The review fixes now persist `NativeConflictEvidence` for a terminal observation that loses the status CAS and block settlement while that conflict remains. Native recovery paginates by task ID so permanent measurement holds cannot starve newer rows. JSON receipt parsing rejects duplicate keys. Platform video release is restricted to an explicit provider-confirmed empty-output failure; platform OCR failed terminals invoke the trusted release hook and mark the platform ledger refunded. Veo reservation rejects the currently unimplemented output-probe policy rather than accepting a reservation that can never settle. Focused service/model/OCR/taskcommon tests pass.

A multiple-output aggregation implementation was deliberately not added: the current provider contracts in this worktree do not define a stable per-output duration/unit field for a safe generic sum. Plural/unsupported receipts therefore remain pending; provider-specific schemas and tests are still required before enabling such tariffs. Provider live probes and native-media validation remain outside this review.
