# Core Upstream Correctness Port Design

**Date:** 2026-08-18

**Status:** Approved in design review

## Objective

Selectively port the upstream changes that directly protect LLMRouter's core responsibilities as a production AI gateway: replay-safe upstream transport, correct billing across routing retries, secret-safe database diagnostics, deterministic route execution, bounded and scoped Responses continuity, and fail-closed tool-call mediation.

This work does not attempt to merge all of new-api or OpenCodex. LLMRouter remains based on its current new-api-derived control plane and its existing Go relay architecture. Upstream changes are translated into the current architecture only when they materially improve gateway correctness or safety.

## Product Boundary

The work is divided into three independently testable subprojects:

1. Transport, billing, and database-log correctness.
2. Route execution and Responses continuity isolation.
3. Tool-output policy enforcement and cross-protocol hardening.

The following are explicitly out of scope:

- OpenCodex GUI, local service, account-pool, quota UI, update, shim, workspace, image, video, and search-sidecar implementations.
- A wholesale RelayKit directory or type-system migration.
- Unrelated new-api UI changes and provider-specific features.
- New channel type numbers or changes that conflict with LLMRouter's existing Gateway channel.
- Compatibility reads of legacy unscoped Responses state.
- Automatic deployment before the complete verification gate passes.

## Global Constraints

- Existing public Chat Completions, Responses, Responses Compact, Anthropic Messages, Gemini, and MCP contracts must remain stable unless this design explicitly tightens unsafe behavior.
- Existing Gateway channel type and model-routing data must remain compatible.
- Every behavior change must be developed with a failing regression test first.
- Upstream commits are reference material, not cherry-pick requirements; implementations must follow the current repository's package boundaries.
- Unsafe or ambiguous state must fail closed with a stable client-visible error and must not mutate billing, continuity state, or channel status.
- The existing unrelated untracked file `scripts/prepare-sidecar-env.py` must not be modified or committed.

## Subproject 1: Transport, Billing, and Database Logs

### Replay-safe request bodies

All relay paths that can be retried by Go's HTTP transport must construct requests from replayable body storage. The first send and each transport retry receive independent readers over the same immutable bytes or spool file. `http.Request.GetBody` must never return the original reader or another reader sharing its consumed offset.

The implementation extends the existing body-storage abstraction rather than buffering a second copy in each handler. Content length remains exact. Multipart and large spooled bodies must remain replayable without forcing all bodies into memory. Client cancellation continues to cancel the active upstream request.

Reference upstream behavior: new-api commits `ea4f0210` and `d6b5ce99`.

### Final-group tiered billing

After every successful channel selection, including retries and auto-group transitions, LLMRouter refreshes group-dependent tiered-billing state before sending upstream. If the selected group requires a higher reservation, the billing session reserves the delta. A free-to-paid transition creates a billing session before the upstream call. A paid-to-free transition settles at the free ratio without falsely marking a previously charged request as initially free.

If reservation or billing-state refresh fails, the attempt stops before the upstream request is sent. The attempted channel is still recorded for retry diagnostics. Settlement and consume logs use the final selected group's ratio.

Reference upstream behavior: new-api commits `df43f801` and `cfaba1dd`.

### Parameterized and redacted database diagnostics

GORM uses parameterized SQL logging. Slow-query thresholds remain configurable, but emitted logs must not contain bound values. Driver errors pass through a conservative sanitizer before logging so connection strings, credentials, tokens, and query parameters are not exposed.

The application continues to receive the original error for control flow; only the log representation is sanitized. Tests must prove that representative secrets do not appear in logs while the error class and actionable operation context remain visible.

Reference upstream behavior: new-api commit `c3db4140`.

## Subproject 2: Route Execution and Responses Continuity

### Route target binding

When model-route resolution produces a route ID, channel ID, or target ID, the selected route becomes authoritative for the relay attempt. The distributor may choose among candidates only when no explicit route target exists or when the route policy explicitly defines a fallback sequence. It must not silently replace a resolved target with an unrelated channel.

The relay trace records the resolved route, attempted target, rejection reason, and final target. Billing-model resolution follows the same target identity used by the upstream request.

### Context-fit admission and fallback

Before an upstream attempt, LLMRouter compares the estimated input-token requirement with the candidate model's declared context window. A candidate that cannot fit the request is skipped without sending upstream or consuming quota. Fallback proceeds to the next eligible target.

Unknown context capacity is handled according to catalog provenance: explicitly verified unknown capacity is ineligible for a route that requires a known fit; legacy routes without a fit requirement preserve current behavior. Rejection returns a stable context-capacity error when no candidate can fit.

Reference OpenCodex behavior includes commits `4a5037c654`, `babf254b1d`, `d18781cca9`, `a0e4e8dee`, and `bc6019bae`.

### Versioned, scoped continuity state

New Responses continuity records use a versioned scope containing:

- tenant/user identity;
- token identity;
- route identity;
- channel or target identity;
- client-task identity;
- model identity.

The client-task identity is accepted only from a documented, bounded request attribute or a server-issued continuity identifier. It must not be derived from arbitrary untrusted text.

Legacy records without the full scope are rejected. There is no compatibility fallback. A client using an old `previous_response_id` receives a stable state-scope error and must start a new conversation. Records with mismatched route, target, model, tenant, token, or client-task identity are also rejected.

State expansion must be linear in the number of retained turns. Expanded history is not recursively re-saved as a new materialized parent chain. Retention limits remain bounded.

### Safe persistence and default storage policy

An omitted Responses `store` value is normalized to `false` before upstream conversion. Explicit `store:true` remains available only where the selected capability permits it.

Only valid completed turns enter the continuity store. Refusals, incomplete streams, cancelled requests, failed conversions, upstream protocol errors, undeclared-tool violations, and malformed terminal events are not persisted. Persistence remains best-effort only after all eligibility checks pass.

Reference OpenCodex behavior includes commits `e1769b5e29`, `36550e6af`, `4a299fa09`, `1ecb5a760`, `d30ab74af`, `c994d47e6`, `dea7d2c27`, and `88fc9d346`.

## Subproject 3: Tool-Output Safety and Protocol Hardening

### Request policy snapshot

For every request that declares tools, LLMRouter records a normalized request-scoped policy containing allowed tool names, tool type, and effective `tool_choice`. This snapshot follows the selected route and is not shared across requests or retries with a different normalized request.

### Fail-closed upstream validation

Before emitting an upstream tool call to the client or saving it to continuity state, LLMRouter verifies that:

- the tool was declared by the request;
- the call has a valid non-empty name;
- the call is permitted by the effective `tool_choice`;
- the call ID is valid after protocol normalization;
- arguments are syntactically valid for the relevant tool type;
- a streamed call reaches a valid terminal state.

Violations become stable upstream-protocol errors. They do not trigger dangling-result synthesis, billing as a successful completion, continuity persistence, or automatic tool execution.

Requests without a tool catalog retain normal text behavior. They do not establish a permissive wildcard tool policy.

Reference OpenCodex behavior includes commits `b2c33e96b`, `b66ec3db6`, `d1fcc6524`, `09086ed3a`, and `cfbb3cdec`.

### Cross-protocol tool-call normalization

Tool-call IDs are normalized per request when moving between OpenAI Chat, Responses, Anthropic Messages, and Gemini. The mapping is stable within the request and history replay, collision-free within the request, and never stored in process-global mutable state.

Malformed JSON arguments fail conversion instead of silently becoming an empty object. Null padding in otherwise valid streamed deltas is tolerated, while missing names, invalid terminal frames, and incomplete calls fail closed. The existing dangling tool-result repair remains available only for valid declared calls whose later history lacks a result.

Reference OpenCodex behavior includes commits `388c4c251`, `da098a43d`, `bc1b0416f`, `906533ba7`, `4e0ffb2cb`, `569eb3e34`, `027a993d4`, `f61ed63b8`, `d20aae412`, `b9b723c46`, `da36a30aa`, and `ea0608611`.

## Error Contracts

New failures use stable machine-readable error codes grouped under existing LLMRouter error envelopes:

- request body is not replayable;
- billing preparation failed before upstream send;
- no route target can fit the request;
- continuity state is legacy, mismatched, or invalid;
- upstream returned an undeclared or disallowed tool;
- upstream returned a malformed tool call or terminal stream.

Client-caused validation failures are non-retryable. Candidate-capacity rejection may continue through an explicitly configured fallback chain. Transport-level replay occurs only when the request body is proven replayable. Upstream protocol violations may retry another eligible channel only when the existing retry policy allows that error class, but the invalid output is never forwarded or persisted.

## Testing Strategy

Each behavior follows a red-green-refactor cycle. Focused suites cover:

- independent `GetBody` readers for memory and spooled bodies, content length, HTTP/2 retry, multipart payloads, and cancellation;
- free-to-paid, paid-to-free, cheaper-to-more-expensive, and failed-reservation billing transitions;
- database logs containing operation context but no bound values or representative secrets;
- explicit route target pinning, ordered fallback, context-fit skipping, and no-fit errors;
- legacy continuity rejection, scope mismatch, client-task isolation, linear expansion, bounded retention, refusal exclusion, stream failure exclusion, and omitted `store` normalization;
- undeclared tools, forced tool choice, forbidden tools, missing names, malformed arguments, null stream padding, incomplete terminal calls, per-request ID collision handling, and valid dangling-result repair.

The completion gate runs:

1. All focused backend and frontend tests touched by the work.
2. `go test ./...`.
3. `go test -race` for the affected relay, billing, routing, and continuity packages.
4. `go vet ./...`.
5. `gofmt` and `git diff --check`.
6. Frontend format, typecheck, and tests if any frontend file changes.
7. The existing offline capability, route, and protocol contract suites.
8. The deployed health check and online protocol matrix only after code review, commit, push, and deployment authorization are satisfied.

## Delivery Sequence

The subprojects land in dependency order:

1. Replay-safe transport, final-group billing, and secret-safe database logging.
2. Authoritative route targets, context-fit fallback, and versioned Responses continuity.
3. Request-scoped tool policy and cross-protocol output validation.
4. A whole-branch review followed by the full completion gate.

Each subproject is separately reviewable and revertible. Deployment occurs only after all selected subprojects and the full verification gate pass.
