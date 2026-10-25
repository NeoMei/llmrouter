# Task 6 implementation report

## RED
Added deterministic fixtures for confirmed zero, fractional Kling decimal, missing/invalid/queued provider evidence, Doubao token zero, Ali decimal duration, and Veo unknown output probe.

## GREEN
Commit `b87c8d9f4` wires frozen native task contracts into `TaskBillingContext`, `Task` funding ownership, terminal evidence outbox, polling recovery, local native settlement, and platform observer dispatch. Video policies parse exact successful terminal receipts only: Kling provider units, Doubao completion tokens, Sora completed seconds, Ali model-specific duration; request duration is bounded reservation input. Missing/invalid/conflicting evidence stays pending. OCR platform admission creates no local wallet/token operations, local quota updates, request counters, or consume logs; platform successful request emits one `request=1` usage to the platform observer. Platform task/OCR jobs retain immutable call/task IDs and skip local billing.

Validation: `go test ./relay/channel/task/... ./service ./model ./service/ocr ./relay ./controller -count=1` passed, including `TestPlatformOCRNeverCreatesLocalMoneyOperations` and video native evidence fixtures.

## Concerns
Platform controller must supply the `PlatformTaskObserver`; platform adapter must install `PlatformTaskRelease` for trusted no-output release for failed OCR/video terminals. Provider-unit caps require `LLMROUTER_NATIVE_VIDEO_PROVIDER_CAPS` per model/policy; absent cap rejects admission. Veo is admission-disabled/pending until bounded media probing is implemented; no fallback estimate is allowed. No paid/provider calls were made.
