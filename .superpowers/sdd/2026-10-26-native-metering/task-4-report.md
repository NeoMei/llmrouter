# Task 4 report

Implemented actual provider token evidence capture before legacy fallback. `dto.NativeTokenEvidence` parses OpenAI chat/Responses, Claude Messages, and Gemini usage envelopes with cumulative SSE snapshots, explicit zero preservation, duplicate/negative/overflow/conflicting-total rejection, cache and reasoning dimensions, and canonical exclusive measurements. Missing or incomplete evidence remains pending; no estimate is promoted. Gemini embedding native routes fail closed because the provider response lacks actual token evidence.

Adapters now observe raw response bytes/SSE before conversion and defer persistence through `service.NativeTokenObserver` and `RecordNativeUsageForRelay`; local text quota bypasses native contexts. Tests cover actual-vs-estimate, zero, malformed/duplicate/overflow, cumulative streams, cache/reasoning, and embedding aggregates.

Validation: `go test ./dto ./service ./relay ./relay/channel/openai ./relay/channel/claude ./relay/channel/gemini -count=1` passed.

Concern: admission reservation binding remains Task 3 integration; this change exposes evidence only and does not create local funding.
