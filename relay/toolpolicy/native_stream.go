package toolpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

type NativeStreamKind string

const (
	NativeStreamClaude NativeStreamKind = "claude"
	NativeStreamGemini NativeStreamKind = "gemini"
	NativeStreamOllama NativeStreamKind = "ollama"
)

type NativeStreamValidator struct {
	kind          NativeStreamKind
	policy        Policy
	hasPolicy     bool
	failed        error
	success       bool
	toolSeen      bool
	callCount     int
	frames        []json.RawMessage
	bufferedBytes int
	claudeCalls   map[int]*nativeClaudeCall
	geminiCalls   map[string]string
	ollamaCalls   int
}

type nativeClaudeCall struct {
	id        string
	name      string
	kind      ToolKind
	initial   any
	fragments strings.Builder
	completed bool
}

func NewNativeStreamValidator(kind NativeStreamKind, policy Policy, hasPolicy bool) *NativeStreamValidator {
	return &NativeStreamValidator{
		kind: kind, policy: policy, hasPolicy: hasPolicy,
		claudeCalls: make(map[int]*nativeClaudeCall), geminiCalls: make(map[string]string),
	}
}

func NewNativeStreamValidatorFromContext(c *gin.Context, kind NativeStreamKind) *NativeStreamValidator {
	policy, ok := FromContext(c)
	return NewNativeStreamValidator(kind, policy, ok)
}

func (v *NativeStreamValidator) Accept(event json.RawMessage) ([]json.RawMessage, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: native stream validator is nil", ErrMalformedToolCall)
	}
	if v.failed != nil {
		return nil, v.failed
	}
	raw := bytes.TrimSpace(event)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, v.fail(fmt.Errorf("%w: native stream frame must be an object", ErrMalformedToolCall))
	}
	if len(raw) > streamMaxFrameBytes {
		return nil, v.fail(fmt.Errorf("%w: native stream frame exceeds %d bytes", ErrMalformedToolCall, streamMaxFrameBytes))
	}
	if err := ValidateRawResponseJSON(raw); err != nil {
		return nil, v.fail(err)
	}
	if v.success {
		return nil, v.fail(fmt.Errorf("%w: native frame arrived after success terminal", ErrMalformedToolCall))
	}

	var toolRelated, terminal bool
	var err error
	switch v.kind {
	case NativeStreamClaude:
		toolRelated, terminal, err = v.acceptClaude(raw)
	case NativeStreamGemini:
		toolRelated, terminal, err = v.acceptGemini(raw)
	case NativeStreamOllama:
		toolRelated, terminal, err = v.acceptOllama(raw)
	default:
		err = fmt.Errorf("%w: unsupported native stream kind %q", ErrMalformedToolCall, v.kind)
	}
	if err != nil {
		return nil, v.fail(err)
	}
	if terminal {
		if err := v.validateTerminal(); err != nil {
			return nil, v.fail(err)
		}
		v.success = true
	}
	// Provider success terminals stay withheld until transport completion so a
	// late malformed frame can never follow a success byte already sent.
	buffered := toolRelated || terminal || v.policy.RequiresToolCall()
	if !buffered {
		return []json.RawMessage{append(json.RawMessage(nil), raw...)}, nil
	}
	if len(v.frames) >= streamMaxBufferedFrames || v.bufferedBytes+len(raw) > streamMaxBufferedBytes {
		return nil, v.fail(fmt.Errorf("%w: native stream buffer limit exceeded", ErrMalformedToolCall))
	}
	cloned := append(json.RawMessage(nil), raw...)
	v.frames = append(v.frames, cloned)
	v.bufferedBytes += len(cloned)
	return nil, nil
}

func (v *NativeStreamValidator) Finish() ([]json.RawMessage, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: native stream validator is nil", ErrMalformedToolCall)
	}
	if v.failed != nil {
		return nil, v.failed
	}
	if (v.toolSeen || v.policy.RequiresToolCall()) && !v.success {
		return nil, v.fail(fmt.Errorf("%w: native stream ended without provider success terminal", ErrIncompleteToolCall))
	}
	if err := v.validateTerminal(); err != nil {
		return nil, v.fail(err)
	}
	emitted := append([]json.RawMessage(nil), v.frames...)
	v.frames = nil
	v.bufferedBytes = 0
	return emitted, nil
}

func (v *NativeStreamValidator) Abort(cause error) error {
	if v == nil {
		return fmt.Errorf("%w: native stream validator is nil", ErrMalformedToolCall)
	}
	v.frames = nil
	v.bufferedBytes = 0
	if cause == nil {
		cause = fmt.Errorf("native stream aborted")
	}
	return v.fail(fmt.Errorf("%w: %v", ErrIncompleteToolCall, cause))
}

func (v *NativeStreamValidator) acceptClaude(raw json.RawMessage) (bool, bool, error) {
	var event dto.ClaudeResponse
	if err := common.UnmarshalUnique(raw, &event); err != nil {
		return false, false, fmt.Errorf("%w: invalid Claude stream frame: %v", ErrMalformedToolCall, err)
	}
	switch event.Type {
	case "content_block_start":
		if event.ContentBlock == nil || !strings.HasSuffix(event.ContentBlock.Type, "tool_use") {
			return false, false, nil
		}
		index := event.GetIndex()
		if len(v.claudeCalls) >= streamMaxActiveCalls || v.claudeCalls[index] != nil {
			return false, false, fmt.Errorf("%w: duplicate or excessive Claude tool block", ErrMalformedToolCall)
		}
		kind := ToolKindCustom
		if event.ContentBlock.Type == "tool_use" {
			kind = ToolKindFunction
		}
		v.claudeCalls[index] = &nativeClaudeCall{id: event.ContentBlock.Id, name: event.ContentBlock.Name, kind: kind, initial: event.ContentBlock.Input}
		v.toolSeen = true
		return true, false, nil
	case "content_block_delta":
		if event.Delta == nil || event.Delta.Type != "input_json_delta" {
			return false, false, nil
		}
		call := v.claudeCalls[event.GetIndex()]
		if call == nil || call.completed || event.Delta.PartialJson == nil {
			return false, false, fmt.Errorf("%w: orphan or malformed Claude tool delta", ErrMalformedToolCall)
		}
		if call.fragments.Len()+len(*event.Delta.PartialJson) > streamMaxCallArgumentBytes {
			return false, false, fmt.Errorf("%w: Claude tool input exceeds limit", ErrMalformedToolCall)
		}
		call.fragments.WriteString(*event.Delta.PartialJson)
		return true, false, nil
	case "content_block_stop":
		call := v.claudeCalls[event.GetIndex()]
		if call == nil {
			return false, false, nil
		}
		if call.completed {
			return false, false, fmt.Errorf("%w: duplicate Claude tool block stop", ErrMalformedToolCall)
		}
		arguments := json.RawMessage(call.fragments.String())
		if call.fragments.Len() == 0 {
			var err error
			arguments, err = common.Marshal(call.initial)
			if err != nil {
				return false, false, fmt.Errorf("%w: Claude tool input: %v", ErrMalformedArguments, err)
			}
		}
		if err := v.validateCall(Call{ID: call.id, Name: call.name, Kind: call.kind, Arguments: arguments}); err != nil {
			return false, false, err
		}
		call.completed = true
		v.callCount++
		return true, false, nil
	case "message_stop":
		for _, call := range v.claudeCalls {
			if !call.completed {
				return false, false, fmt.Errorf("%w: Claude message stopped with pending tool block", ErrIncompleteToolCall)
			}
		}
		return false, true, nil
	default:
		return false, false, nil
	}
}

func (v *NativeStreamValidator) acceptGemini(raw json.RawMessage) (bool, bool, error) {
	var response dto.GeminiChatResponse
	if err := common.UnmarshalUnique(raw, &response); err != nil {
		return false, false, fmt.Errorf("%w: invalid Gemini stream frame: %v", ErrMalformedToolCall, err)
	}
	toolRelated := false
	terminal := len(response.Candidates) > 0
	for candidateIndex := range response.Candidates {
		candidate := &response.Candidates[candidateIndex]
		if candidate.FinishReason == nil || strings.TrimSpace(*candidate.FinishReason) == "" {
			terminal = false
		}
		for partIndex := range candidate.Content.Parts {
			call := candidate.Content.Parts[partIndex].FunctionCall
			if call == nil {
				continue
			}
			toolRelated = true
			v.toolSeen = true
			arguments, err := common.Marshal(call.Arguments)
			if err != nil || len(arguments) > streamMaxCallArgumentBytes {
				return false, false, fmt.Errorf("%w: Gemini function arguments invalid or too large", ErrMalformedArguments)
			}
			id := "gemini_candidate_" + strconv.Itoa(candidateIndex) + "_call_" + strconv.Itoa(partIndex)
			if len(bytes.TrimSpace(call.ID)) > 0 {
				if err := common.UnmarshalUnique(call.ID, &id); err != nil || strings.TrimSpace(id) == "" {
					return false, false, fmt.Errorf("%w: invalid Gemini function call id", ErrMalformedToolCall)
				}
			}
			signature := call.FunctionName + "\x00" + string(arguments)
			if previous, ok := v.geminiCalls[id]; ok {
				if previous != signature {
					return false, false, fmt.Errorf("%w: Gemini function call changed", ErrMalformedToolCall)
				}
				continue
			}
			if len(v.geminiCalls) >= streamMaxActiveCalls {
				return false, false, fmt.Errorf("%w: too many Gemini function calls", ErrMalformedToolCall)
			}
			if err := v.validateCall(Call{ID: id, Name: call.FunctionName, Kind: ToolKindFunction, Arguments: arguments}); err != nil {
				return false, false, err
			}
			v.geminiCalls[id] = signature
			v.callCount++
		}
	}
	if len(response.Candidates) == 0 && response.PromptFeedback != nil && response.PromptFeedback.BlockReason != nil {
		terminal = true
	}
	return toolRelated, terminal, nil
}

func (v *NativeStreamValidator) acceptOllama(raw json.RawMessage) (bool, bool, error) {
	var chunk struct {
		Message *struct {
			ToolCalls []struct {
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		Done bool `json:"done"`
	}
	if err := common.UnmarshalUnique(raw, &chunk); err != nil {
		return false, false, fmt.Errorf("%w: invalid Ollama stream frame: %v", ErrMalformedToolCall, err)
	}
	toolRelated := false
	if chunk.Message != nil {
		for _, tool := range chunk.Message.ToolCalls {
			toolRelated = true
			v.toolSeen = true
			if v.ollamaCalls >= streamMaxActiveCalls || len(tool.Function.Arguments) > streamMaxCallArgumentBytes {
				return false, false, fmt.Errorf("%w: Ollama tool call resource limit", ErrMalformedToolCall)
			}
			arguments := tool.Function.Arguments
			if len(bytes.TrimSpace(arguments)) == 0 || bytes.Equal(bytes.TrimSpace(arguments), []byte("null")) {
				arguments = json.RawMessage(`{}`)
			}
			id := "ollama_call_" + strconv.Itoa(v.ollamaCalls)
			if err := v.validateCall(Call{ID: id, Name: tool.Function.Name, Kind: ToolKindFunction, Arguments: arguments}); err != nil {
				return false, false, err
			}
			v.ollamaCalls++
			v.callCount++
		}
	}
	return toolRelated, chunk.Done, nil
}

func (v *NativeStreamValidator) validateCall(call Call) error {
	err := v.policy.ValidateCall(call)
	if err != nil && !v.hasPolicy {
		return fmt.Errorf("%w: upstream emitted a native streamed tool call without captured policy: %v", ErrMissingToolPolicy, err)
	}
	return err
}

func (v *NativeStreamValidator) validateTerminal() error {
	err := v.policy.ValidateTerminal(v.callCount)
	if err != nil && !v.hasPolicy && v.callCount > 0 {
		return fmt.Errorf("%w: upstream emitted a native streamed tool call without captured policy: %v", ErrMissingToolPolicy, err)
	}
	return err
}

func (v *NativeStreamValidator) fail(err error) error {
	if v.failed == nil {
		v.failed = err
	}
	return v.failed
}
