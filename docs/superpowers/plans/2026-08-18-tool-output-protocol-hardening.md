# Tool-Output Policy and Protocol Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject undeclared, disallowed, malformed, or incomplete upstream tool calls before they reach clients or continuity state, while preserving valid tool use across OpenAI Chat, Responses, Anthropic Messages, and Gemini conversions.

**Architecture:** Build an immutable request-scoped tool policy in Gin context after request normalization. Validate complete calls at response boundaries and buffer streamed tool events until a valid terminal call is available. Use a per-request ID normalizer in protocol converters, reject malformed JSON instead of replacing it, and permit dangling-result repair only for calls that pass the same policy.

**Tech Stack:** Go 1.22+, Gin, SSE/stream relay, existing DTO and relayconvert packages, testify, SHA-256 for deterministic request-local identifiers.

## Global Constraints

- Existing public Chat Completions, Responses, Responses Compact, Anthropic Messages, Gemini, and MCP contracts must remain stable unless the approved design explicitly tightens unsafe behavior.
- Existing Gateway channel type and model-routing data must remain compatible.
- Every behavior change must be developed with a failing regression test first.
- Upstream commits are reference material, not cherry-pick requirements; implementations must follow the current repository's package boundaries.
- Unsafe or ambiguous state must fail closed with a stable client-visible error and must not mutate billing, continuity state, or channel status.
- Requests without a tool catalog retain ordinary text behavior but do not create a wildcard tool policy.
- Tool-policy and tool-ID state must be request-local; no process-global mutable map or counter is allowed.
- Invalid tool output must not be emitted, persisted, billed as a successful completion, automatically executed, or repaired into a synthetic success.
- Null padding in valid stream deltas is tolerated; missing names, malformed arguments, and incomplete terminal frames fail closed.
- The unrelated untracked file `scripts/prepare-sidecar-env.py` must not be modified or committed.
- All JSON encoding and decoding must use wrappers from `common/json.go`.

---

### Task 1: Build an immutable request-scoped tool policy

**Files:**
- Create: `relay/toolpolicy/policy.go`
- Create: `relay/toolpolicy/policy_test.go`
- Modify: `constant/context_key.go`
- Modify: `relay/compatible_handler.go`
- Modify: `relay/responses_handler.go`
- Modify: `relay/claude_handler.go`
- Modify: `relay/gemini_handler.go`
- Modify: `types/error.go`

**Interfaces:**
- Consumes: normalized Chat `[]dto.ToolCallRequest`, Responses `Tools`/`ToolChoice` raw JSON, and Gin request context.
- Produces: `Policy`, `BuildFromChat`, `BuildFromResponses`, `BuildFromClaude`, `BuildFromGemini`, `ValidateCall`, `Set`, `FromContext`, and stable error code `upstream_tool_policy_violation`.

- [x] **Step 1: Write failing policy tests**

Cover declared function, undeclared function, empty name, `tool_choice:"none"`, forced named choice, no-catalog text-only behavior, duplicate declaration normalization, and invalid request tool definitions.

```go
func TestPolicyRejectsUndeclaredAndForcedChoiceMismatch(t *testing.T) {
	policy, err := BuildFromResponses(
		json.RawMessage(`[{"type":"function","name":"lookup"},{"type":"function","name":"save"}]`),
		json.RawMessage(`{"type":"function","name":"lookup"}`),
	)
	require.NoError(t, err)
	require.NoError(t, policy.ValidateCall(Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}))
	err = policy.ValidateCall(Call{ID: "call_2", Name: "save", Arguments: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, ErrDisallowedTool)
	err = policy.ValidateCall(Call{ID: "call_3", Name: "missing", Arguments: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, ErrUndeclaredTool)
}

func TestPolicyWithoutCatalogDoesNotWildcardToolCalls(t *testing.T) {
	policy, err := BuildFromResponses(nil, nil)
	require.NoError(t, err)
	assert.False(t, policy.HasCatalog())
	err = policy.ValidateCall(Call{ID: "call_1", Name: "unexpected", Arguments: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, ErrUndeclaredTool)
}

func mustPolicy(t *testing.T, allowedName string) Policy {
	t.Helper()
	policy, err := BuildFromResponses(
		json.RawMessage(fmt.Sprintf(`[{"type":"function","name":%q}]`, allowedName)),
		nil,
	)
	require.NoError(t, err)
	return policy
}
```

- [x] **Step 2: Verify RED**

Run: `go test ./relay/toolpolicy -count=1`

Expected: build failure because the package and interfaces do not exist.

- [x] **Step 3: Implement the policy model**

```go
type ToolKind string

const (
	ToolKindFunction ToolKind = "function"
	ToolKindCustom   ToolKind = "custom"
)

type ChoiceMode string

const (
	ChoiceAuto     ChoiceMode = "auto"
	ChoiceNone     ChoiceMode = "none"
	ChoiceRequired ChoiceMode = "required"
	ChoiceNamed    ChoiceMode = "named"
)

type Declaration struct {
	Name string
	Kind ToolKind
}

type Call struct {
	ID        string
	Name      string
	Kind      ToolKind
	Arguments json.RawMessage
}

type Policy struct {
	allowed    map[string]Declaration
	choice     ChoiceMode
	forcedName string
}
```

`BuildFromChat`, `BuildFromResponses`, `BuildFromClaude`, and `BuildFromGemini` normalize each protocol's declared tools and effective choice into the same immutable structure. `ValidateCall` requires a non-empty normalized ID/name, a declaration with the same kind, syntactically valid arguments, and compatibility with the effective choice. Function arguments must be a JSON object; custom-tool input may be a valid JSON string or object. `ChoiceNone` rejects every call, and `ChoiceNamed` permits only `forcedName`.

Return copies from `FromContext`; never expose the internal map for mutation. Add `ContextKeyToolPolicy`. Build and store the matching policy after the request copy is normalized in `TextHelper`, `ResponsesHelper`, `ClaudeHelper`, or `GeminiHelper`, before adaptor conversion or upstream send.

- [x] **Step 4: Verify GREEN**

Run: `go test -race ./relay/toolpolicy -count=1`

Expected: PASS with no race report.

- [x] **Step 5: Commit Task 1**

```bash
git add relay/toolpolicy/policy.go relay/toolpolicy/policy_test.go constant/context_key.go relay/compatible_handler.go relay/responses_handler.go relay/claude_handler.go relay/gemini_handler.go types/error.go
git commit -m "feat(relay): capture request-scoped tool policy"
```

### Task 2: Validate complete non-stream tool calls before emission

**Files:**
- Create: `relay/toolpolicy/validate_response.go`
- Create: `relay/toolpolicy/validate_response_test.go`
- Modify: `relay/channel/openai/relay-openai.go`
- Modify: `relay/channel/openai/relay_responses.go`
- Modify: `relay/channel/claude/relay-claude.go`
- Modify: `relay/channel/gemini/relay-gemini.go`
- Modify: `relay/channel/openai/responses_state_test.go`
- Modify: `dto/openai_response.go`

**Interfaces:**
- Consumes: `Policy.ValidateCall` from Task 1 and current response DTOs.
- Produces: `ValidateChatResponse`, `ValidateResponsesResponse`, `ValidateClaudeResponse`, and `ValidateGeminiResponse` returning typed policy/protocol errors.

- [x] **Step 1: Write failing response validation tests**

Test one valid call and each invalid condition: undeclared name, missing name, missing ID, malformed arguments, wrong argument type, forced-choice mismatch, and refusal/incomplete Responses output. Add `Refusal string json:"refusal,omitempty"` to `dto.ResponsesOutputContent` so refusal eligibility is representable.

```go
func TestValidateResponsesResponseRejectsUndeclaredTool(t *testing.T) {
	policy := mustPolicy(t, "lookup")
	resp := &dto.OpenAIResponsesResponse{
		ID: "resp_1",
		Output: []dto.ResponsesOutput{{
			Type: "function_call", CallId: "call_1", Name: "delete_all",
			Arguments: json.RawMessage(`{}`), Status: "completed",
		}},
	}
	err := ValidateResponsesResponse(policy, resp)
	require.ErrorIs(t, err, ErrUndeclaredTool)
}
```

- [x] **Step 2: Verify RED**

Run: `go test ./relay/toolpolicy ./relay/channel/openai ./relay/channel/claude ./relay/channel/gemini -run '(Validate.*Tool|UndeclaredTool|MalformedArguments|ForcedChoice)' -count=1`

Expected: build failure for the validation functions.

- [x] **Step 3: Implement format adapters into one validation core**

Each format adapter extracts only complete upstream calls and maps them to `toolpolicy.Call`. It returns an error before any `c.JSON`, `c.Data`, SSE write, continuity persistence, or success settlement. Built-in non-call output items remain unchanged; built-in calls are valid only when the request declared their exact type/name.

```go
func ValidateResponsesResponse(policy Policy, response *dto.OpenAIResponsesResponse) error {
	if response == nil {
		return ErrMalformedToolCall
	}
	for _, output := range response.Output {
		if output.Type != "function_call" && output.Type != "custom_tool_call" {
			continue
		}
		kind := ToolKindFunction
		if output.Type == "custom_tool_call" {
			kind = ToolKindCustom
		}
		if output.Status != "" && output.Status != "completed" {
			return ErrIncompleteToolCall
		}
		if err := policy.ValidateCall(Call{ID: output.CallId, Name: output.Name, Kind: kind, Arguments: output.Arguments}); err != nil {
			return err
		}
	}
	return nil
}
```

- [x] **Step 4: Wire validation before output and persistence**

At every non-stream response boundary, retrieve the request policy from Gin context and validate the provider-native response or its normalized DTO before writing. Convert failures to `upstream_tool_policy_violation` or `upstream_tool_protocol_invalid`, set skip-success-settlement state, and return a retryable channel error only when the existing upstream-error policy permits another channel. Ensure `persistResponsesTurn` is not called after a validation failure.

- [x] **Step 5: Verify GREEN**

Run: `go test -race ./relay/toolpolicy ./relay/channel/openai ./relay/channel/claude ./relay/channel/gemini -run '(Tool|Responses|Claude|Gemini|Policy)' -count=1`

Expected: PASS; recorder bodies contain no invalid tool call bytes.

- [x] **Step 6: Commit Task 2**

```bash
git add relay/toolpolicy/validate_response.go relay/toolpolicy/validate_response_test.go relay/channel/openai/relay-openai.go relay/channel/openai/relay_responses.go relay/channel/claude/relay-claude.go relay/channel/gemini/relay-gemini.go relay/channel/openai/responses_state_test.go dto/openai_response.go
git commit -m "fix(relay): reject invalid upstream tool calls"
```

### Task 3: Buffer streamed calls until a valid terminal state

**Files:**
- Create: `relay/toolpolicy/stream.go`
- Create: `relay/toolpolicy/stream_test.go`
- Modify: `relay/channel/openai/relay-openai.go`
- Modify: `relay/channel/openai/relay_responses.go`
- Modify: `service/relayconvert/internal/oai_chat/to_oai_responses_stream_resp.go`
- Modify: `service/relayconvert/internal/oai_responses/to_oai_chat_stream_resp.go`

**Interfaces:**
- Consumes: immutable `Policy` and complete-call validator from Tasks 1–2.
- Produces: `StreamValidator.Accept(event json.RawMessage) ([]json.RawMessage, error)` and `StreamValidator.Finish() ([]json.RawMessage, error)`.

- [x] **Step 1: Write failing stream state-machine tests**

Cover split name/arguments, interleaved indices, null delta padding, valid terminal flush, missing name, malformed JSON, terminal event without completion, and EOF with a pending call. Assert tool-related frames are withheld until validation succeeds, while unrelated text frames pass through immediately.

```go
func TestStreamValidatorBuffersToolFramesUntilValidDone(t *testing.T) {
	validator := NewStreamValidator(mustPolicy(t, "lookup"))
	frames := []json.RawMessage{
		json.RawMessage(`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"lookup"}}`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","item_id":"call_1","delta":"{\"q\":"}`),
		json.RawMessage(`null`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","item_id":"call_1","delta":"\"x\"}"}`),
	}
	for _, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		assert.Empty(t, emitted)
	}
	emitted, err := validator.Accept(json.RawMessage(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}","status":"completed"}}`))
	require.NoError(t, err)
	assert.Len(t, emitted, 4)
}
```

- [x] **Step 2: Verify RED**

Run: `go test ./relay/toolpolicy -run 'TestStreamValidator' -count=1`

Expected: build failure because `StreamValidator` does not exist.

- [x] **Step 3: Implement request-local stream accumulation**

Key pending calls by output index/item ID/call ID in an instance-owned map. Ignore a frame whose complete JSON value is `null`; do not ignore null bytes inside an argument string. Buffer every frame belonging to a pending tool item. At the terminal frame, combine arguments, validate the completed call, and return the buffered frames in original order. `Finish` returns `ErrIncompleteToolCall` when pending calls remain.

```go
type StreamValidator struct {
	policy  Policy
	pending map[string]*pendingCall
	order   uint64
}

type pendingCall struct {
	id        string
	name      string
	kind      ToolKind
	arguments strings.Builder
	frames    []sequencedFrame
	terminal  bool
}
```

- [x] **Step 4: Put the state machine before every SSE write**

For native OpenAI Chat/Responses streams and both Chat↔Responses converters, pass each normalized event through `Accept`, write only returned frames, and call `Finish` on `[DONE]`, `response.completed`, EOF, and cancellation. On failure, do not emit buffered tool frames or a successful terminal event; return the stable upstream protocol error and skip persistence/success billing.

- [x] **Step 5: Verify GREEN**

Run: `go test -race ./relay/toolpolicy ./relay/channel/openai ./service/relayconvert/internal/oai_chat ./service/relayconvert/internal/oai_responses -run '(Stream|ToolCall|Null|Incomplete|Terminal)' -count=1`

Expected: PASS; invalid buffered frames never appear in recorder output.

- [x] **Step 6: Commit Task 3**

```bash
git add relay/toolpolicy/stream.go relay/toolpolicy/stream_test.go relay/channel/openai/relay-openai.go relay/channel/openai/relay_responses.go service/relayconvert/internal/oai_chat/to_oai_responses_stream_resp.go service/relayconvert/internal/oai_responses/to_oai_chat_stream_resp.go
git commit -m "fix(relay): validate streamed tool calls atomically"
```

### Task 4: Normalize tool-call IDs per request and reject malformed conversion input

**Files:**
- Create: `service/relayconvert/internal/toolcallid/normalizer.go`
- Create: `service/relayconvert/internal/toolcallid/normalizer_test.go`
- Modify: `service/relayconvert/internal/oai_chat/to_claude_messages_req.go`
- Modify: `service/relayconvert/internal/oai_chat/to_claude_messages_req_test.go`
- Modify: `service/relayconvert/internal/oai_chat/to_gemini_chat_req.go`
- Create: `service/relayconvert/internal/oai_chat/to_gemini_chat_req_test.go`
- Modify: `service/relayconvert/internal/oai_responses/to_claude_messages_req.go`
- Create: `service/relayconvert/internal/oai_responses/to_claude_messages_req_test.go`
- Modify: `service/relayconvert/internal/oai_responses/to_gemini_chat_req.go`
- Create: `service/relayconvert/internal/oai_responses/to_gemini_chat_req_test.go`
- Modify: `relay/quirk/tool_buffer.go`
- Modify: `relay/quirk/tool_buffer_test.go`

**Interfaces:**
- Consumes: validated request history and existing converter entry points.
- Produces: `toolcallid.Normalizer`, `Normalize(sourceID string) string`, `Lookup(sourceID string) (string, bool)`, and `ToolCallBuffer.Flush() ([]BufferedToolCall, error)`.

- [x] **Step 1: Write failing normalization and malformed-argument tests**

Test valid IDs preserved, invalid characters normalized, empty IDs assigned deterministically, two source IDs with the same sanitized base remain collision-free, the same source ID maps identically within one normalizer, and separate normalizers do not share state. Converter tests must expect an error for `{bad-json` rather than `{}`.

```go
func TestNormalizerIsStableCollisionFreeAndRequestLocal(t *testing.T) {
	a := New("call")
	first := a.Normalize("bad/id")
	assert.Equal(t, first, a.Normalize("bad/id"))
	second := a.Normalize("bad id")
	assert.NotEqual(t, first, second)
	b := New("call")
	assert.Equal(t, first, b.Normalize("bad/id"))
}
```

- [x] **Step 2: Verify RED**

Run: `go test ./service/relayconvert/internal/toolcallid ./service/relayconvert/internal/oai_chat ./service/relayconvert/internal/oai_responses ./relay/quirk -run '(Normalizer|MalformedArguments|ToolCallBuffer)' -count=1`

Expected: build failure for the new package/API, and existing converter behavior fails the malformed-JSON expectation.

- [x] **Step 3: Implement deterministic request-local IDs**

Allow ASCII letters, digits, `_`, and `-`, with a maximum of 64 bytes. Preserve a valid non-empty source ID. Otherwise concatenate the configured prefix, `_`, and the first 24 lowercase hexadecimal characters of SHA-256 over the source ID or generated ordinal; on an in-request collision, append `_2`, `_3`, shortening the hash portion to remain within 64 bytes. Store forward and reverse maps only on the `Normalizer` instance.

```go
type Normalizer struct {
	prefix  string
	forward map[string]string
	reverse map[string]string
	seq     uint64
}
```

- [x] **Step 4: Use one normalizer for each conversion request/history**

Instantiate one normalizer at each top-level converter entry and pass it through message/input-item conversion. Normalize both tool calls and their later tool results through the same map. Return a descriptive conversion error when function arguments cannot be decoded as valid JSON; do not substitute an empty object.

Change `ToolCallBuffer.Flush` to validate non-empty name and JSON-object arguments and return `([]BufferedToolCall, error)`. Missing IDs use an instance-owned normalizer rather than a process-global counter.

- [x] **Step 5: Verify GREEN**

Run: `go test -race ./service/relayconvert/internal/toolcallid ./service/relayconvert/internal/oai_chat ./service/relayconvert/internal/oai_responses ./relay/quirk -run '(Tool|Normalizer|Claude|Gemini|Malformed|Collision)' -count=1`

Expected: PASS with no race report.

- [x] **Step 6: Commit Task 4**

```bash
git add service/relayconvert/internal/toolcallid/normalizer.go service/relayconvert/internal/toolcallid/normalizer_test.go service/relayconvert/internal/oai_chat/to_claude_messages_req.go service/relayconvert/internal/oai_chat/to_claude_messages_req_test.go service/relayconvert/internal/oai_chat/to_gemini_chat_req.go service/relayconvert/internal/oai_chat/to_gemini_chat_req_test.go service/relayconvert/internal/oai_responses/to_claude_messages_req.go service/relayconvert/internal/oai_responses/to_claude_messages_req_test.go service/relayconvert/internal/oai_responses/to_gemini_chat_req.go service/relayconvert/internal/oai_responses/to_gemini_chat_req_test.go relay/quirk/tool_buffer.go relay/quirk/tool_buffer_test.go
git commit -m "fix(convert): normalize tool IDs and reject malformed calls"
```

### Task 5: Restrict dangling-result repair to valid declared calls

**Files:**
- Modify: `relay/quirk/tool_repair.go`
- Modify: `relay/quirk/tool_repair_test.go`
- Modify: `relay/channel/gateway/adaptor.go`
- Modify: `relay/channel/gateway/adaptor_test.go`

**Interfaces:**
- Consumes: `toolpolicy.Policy` and normalized IDs from Tasks 1 and 4.
- Produces: `RepairDanglingToolCalls(req *dto.GeneralOpenAIRequest, policy toolpolicy.Policy) error`.

- [x] **Step 1: Write failing repair-policy tests**

Retain the valid dangling repair test. Add undeclared name, missing name, malformed arguments, invalid ID, and `tool_choice:none` cases; each must return an error and leave the message slice unchanged.

```go
func TestRepairDanglingToolCallsRejectsUndeclaredCallWithoutMutation(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{}
	req.Messages = []dto.Message{{Role: "assistant"}}
	req.Messages[0].SetToolCalls([]dto.ToolCallRequest{{
		ID: "call_1", Type: "function",
		Function: dto.FunctionRequest{Name: "delete_all", Arguments: `{}`},
	}})
	original := append([]dto.Message(nil), req.Messages...)
	err := RepairDanglingToolCalls(req, mustPolicy(t, "lookup"))
	require.ErrorIs(t, err, toolpolicy.ErrUndeclaredTool)
	assert.Equal(t, original, req.Messages)
}
```

- [x] **Step 2: Verify RED**

Run: `go test ./relay/quirk -run 'TestRepairDanglingToolCalls' -count=1`

Expected: build failure because the repair function does not accept a policy or return an error.

- [x] **Step 3: Validate before mutation and synthesize only approved results**

First scan into a new output slice and validate every assistant call through `policy.ValidateCall`. Only after the full scan succeeds assign `req.Messages = repaired`. Preserve the existing placeholder text for a valid declared call whose result is genuinely absent.

- [x] **Step 4: Update callers and verify GREEN**

Retrieve the request policy from Gin context, pass it to repair, and propagate an error as `invalid_request_error` before upstream send.

Run: `go test -race ./relay/quirk ./relay/... -run '(Dangling|ToolRepair|ToolPolicy)' -count=1`

Expected: PASS; invalid history is never mutated into apparently valid tool output.

- [x] **Step 5: Commit Task 5**

```bash
git add relay/quirk/tool_repair.go relay/quirk/tool_repair_test.go
git add relay/channel/gateway/adaptor.go relay/channel/gateway/adaptor_test.go
git commit -m "fix(relay): gate dangling tool repair by request policy"
```

### Task 6: Subproject and whole-branch verification gate

**Files:**
- No planned file changes; this task is a verification-only gate.

**Interfaces:**
- Consumes: Tasks 1–5 plus the previous two subprojects.
- Produces: final branch evidence suitable for code review, push, authorized deployment, and the online matrix.

- [x] **Step 1: Run focused race suites**

Run: `go test -race ./relay/toolpolicy ./relay/quirk ./relay/channel/openai ./relay/channel/claude ./relay/channel/gemini ./service/relayconvert/... ./service/responsesstate -count=1`

Expected: PASS with no race report.

- [x] **Step 2: Run all offline protocol contracts**

Run: `go test ./relay/... ./service/relayconvert/... ./service/modelcatalog ./service/responsesstate -count=1`

Expected: PASS for Chat, Responses, Compact, Anthropic, Gemini, route, continuity, stream, and tool conversion contracts.

- [x] **Step 3: Run the full backend completion gate**

Run: `go test ./...`

Expected: all packages PASS.

Run: `go vet ./...`

Expected: exit 0 with no diagnostics.

Run: `gofmt -w relay/toolpolicy relay/quirk service/relayconvert/internal/toolcallid service/relayconvert/internal/oai_chat service/relayconvert/internal/oai_responses relay/channel/openai relay/channel/claude relay/channel/gemini dto/openai_response.go constant/context_key.go types/error.go && git diff --check`

Expected: no formatting or whitespace diagnostics.

- [x] **Step 4: Perform whole-branch review and scope audit**

Run: `git diff --stat e424a24e..HEAD && git diff --name-only e424a24e..HEAD && git diff -- scripts/prepare-sidecar-env.py`

Expected: changes are limited to approved specs/plans and core transport, billing, logging, routing, continuity, tool-policy, converter, DTO/error, and test files; the sidecar helper has no diff.

- [x] **Step 5: Push, deploy, and verify only after review authorization**

After the whole-branch review has no open Critical/Important findings, commit any verified repair with `git commit -m "test(core): close protocol hardening verification gaps"`, push the reviewed branch, deploy that exact commit using the repository's existing deployment workflow, then verify:

```bash
curl -fsS https://llmrouter.win/api/status
```

Run the existing online 16-case protocol matrix against the deployed commit. Expected: all 16 gateway cases pass; Kimi may return only the already-known upstream `access_terminated_error` while its billing period is exhausted, and that external condition must not be reported as an llmrouter regression.

Verification note (2026-08-18): the local whole-branch review, focused race suites, full `go test ./...`, `go vet ./...`, formatting, and diff-scope checks passed after closing the remaining replay-metadata, JSON-wrapper, and route-trace isolation gaps. After explicit deployment authorization, commit `7591d93ebe513fb0ac8a9f9a83894ed51c32a678` was pushed to `main` and `upstream-sync`, built as immutable image `llmrouter:7591d93e`, canary-checked, and deployed with the previous `llmrouter:e424a24e` container retained stopped for rollback. Local and public health checks passed, and the supported 16-case Chat, Responses, and Claude protocol matrix passed. Additional DeepSeek Responses-stream probes correctly failed closed when the upstream ended without a `response.completed` terminal.
