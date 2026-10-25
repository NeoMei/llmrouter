# Task 6 implementation report

## RED
Added deterministic fixtures for confirmed zero, fractional Kling decimal, missing/invalid/queued provider evidence, Doubao token zero, Ali decimal duration, and Veo unknown output probe.

## GREEN
Commit `b87c8d9f4` wires frozen native task contracts into `TaskBillingContext`, `Task` funding ownership, terminal evidence outbox, polling recovery, local native settlement, and platform observer dispatch. Video policies parse exact successful terminal receipts only: Kling provider units, Doubao completion tokens, Sora completed seconds, Ali model-specific duration; request duration is bounded reservation input. Missing/invalid/conflicting evidence stays pending. OCR platform admission creates no local wallet/token operations, local quota updates, request counters, or consume logs; platform successful request emits one `request=1` usage to the platform observer. Platform task/OCR jobs retain immutable call/task IDs and skip local billing.

Validation: `go test ./relay/channel/task/... ./service ./model ./service/ocr ./relay ./controller -count=1` passed, including `TestPlatformOCRNeverCreatesLocalMoneyOperations` and video native evidence fixtures.

## Concerns
Platform controller must supply the `PlatformTaskObserver`; platform adapter must install `PlatformTaskRelease` for trusted no-output release for failed OCR/video terminals. Provider-unit caps require `LLMROUTER_NATIVE_VIDEO_PROVIDER_CAPS` per model/policy; absent cap rejects admission. Veo is admission-disabled/pending until bounded media probing is implemented; no fallback estimate is allowed. No paid/provider calls were made.

## Review follow-up

Post-review fixes (`273461dc0`, `d00b1d2a6`) persist colliding task evidence, paginate native recovery, reject duplicate receipt keys, invoke trusted no-output release for failed platform video/OCR terminals, and reject Veo output-probe admission until a bounded probe exists. Focused regression tests pass. Multiple-output provider-specific aggregation remains pending because no stable per-output duration/unit schema is defined in the current adapters; plural unsupported receipts stay pending rather than being estimated.
