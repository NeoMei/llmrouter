package toolpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
)

var ErrIncompleteToolCall = errors.New("incomplete tool call")

// ValidateChatResponse validates every complete tool call in every choice of a
// native Chat Completions response. ToolCalls remains raw on dto.Message, so
// this adapter deliberately decodes it without accepting duplicate JSON keys.
func ValidateChatResponse(policy Policy, response *dto.OpenAITextResponse) error {
	if response == nil {
		return fmt.Errorf("%w: chat response is nil", ErrMalformedToolCall)
	}
	for choiceIndex := range response.Choices {
		raw := bytes.TrimSpace(response.Choices[choiceIndex].Message.ToolCalls)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		var calls []struct {
			ID       string               `json:"id"`
			Type     string               `json:"type"`
			Function dto.FunctionResponse `json:"function"`
		}
		if err := common.UnmarshalUnique(raw, &calls); err != nil {
			return fmt.Errorf("%w: chat choice %d tool_calls: %v", ErrMalformedToolCall, choiceIndex, err)
		}
		for callIndex, call := range calls {
			kind := ToolKind(strings.TrimSpace(call.Type))
			if kind != ToolKindFunction && kind != ToolKindCustom {
				return fmt.Errorf("%w: chat choice %d call %d has unsupported type %q", ErrMalformedToolCall, choiceIndex, callIndex, call.Type)
			}
			if err := policy.ValidateCall(Call{
				ID: call.ID, Name: call.Function.Name, Kind: kind,
				Arguments: json.RawMessage(call.Function.Arguments),
			}); err != nil {
				return fmt.Errorf("chat choice %d call %d: %w", choiceIndex, callIndex, err)
			}
		}
	}
	return nil
}

// ValidateResponsesResponse validates function/custom calls plus explicit
// built-in call items. Built-in results and other non-call output items pass
// through unchanged.
func ValidateResponsesResponse(policy Policy, response *dto.OpenAIResponsesResponse) error {
	if response == nil {
		return fmt.Errorf("%w: responses response is nil", ErrMalformedToolCall)
	}
	for outputIndex := range response.Output {
		output := &response.Output[outputIndex]
		kind := ToolKindFunction
		id := output.CallId
		name := output.Name
		arguments := output.Arguments
		isCall := false

		switch output.Type {
		case "function_call":
			isCall = true
			arguments = unwrapFunctionArguments(arguments)
		case "custom_tool_call":
			isCall = true
			kind = ToolKindCustom
		default:
			if strings.HasSuffix(output.Type, "_call") {
				isCall = true
				kind = ToolKindCustom
				if strings.TrimSpace(id) == "" {
					id = output.ID
				}
				if strings.TrimSpace(name) == "" {
					name = responsesBuiltInDeclarationName(policy, output.Type)
				}
				if len(bytes.TrimSpace(arguments)) == 0 {
					arguments = json.RawMessage(`{}`)
				}
			}
		}
		if !isCall {
			continue
		}
		if output.Status != "" && output.Status != "completed" {
			return fmt.Errorf("responses output %d: %w: status %q", outputIndex, ErrIncompleteToolCall, output.Status)
		}
		if err := policy.ValidateCall(Call{ID: id, Name: name, Kind: kind, Arguments: arguments}); err != nil {
			return fmt.Errorf("responses output %d: %w", outputIndex, err)
		}
	}
	return nil
}

func responsesBuiltInDeclarationName(policy Policy, outputType string) string {
	candidates := []string{strings.TrimSuffix(outputType, "_call")}
	switch outputType {
	case "web_search_call":
		candidates = []string{"web_search_preview", "web_search"}
	case "computer_call":
		candidates = []string{"computer_use_preview", "computer"}
	}
	for _, candidate := range candidates {
		if declaration, ok := policy.allowed[candidate]; ok && declaration.Kind == ToolKindCustom {
			return candidate
		}
	}
	return candidates[0]
}

func unwrapFunctionArguments(arguments json.RawMessage) json.RawMessage {
	if common.GetJsonType(arguments) != "string" {
		return arguments
	}
	var value string
	if err := common.UnmarshalUnique(arguments, &value); err != nil {
		return arguments
	}
	return json.RawMessage(value)
}

// ValidateClaudeResponse validates native Anthropic tool_use blocks. Other
// content blocks (including tool results) are not upstream calls.
func ValidateClaudeResponse(policy Policy, response *dto.ClaudeResponse) error {
	if response == nil {
		return fmt.Errorf("%w: claude response is nil", ErrMalformedToolCall)
	}
	for contentIndex := range response.Content {
		content := &response.Content[contentIndex]
		kind := ToolKindFunction
		isCall := content.Type == "tool_use"
		if !isCall && strings.HasSuffix(content.Type, "tool_use") {
			isCall = true
			kind = ToolKindCustom
		}
		if !isCall {
			continue
		}
		arguments, err := common.Marshal(content.Input)
		if err != nil {
			return fmt.Errorf("%w: claude content %d input: %v", ErrMalformedArguments, contentIndex, err)
		}
		if err := policy.ValidateCall(Call{ID: content.Id, Name: content.Name, Kind: kind, Arguments: arguments}); err != nil {
			return fmt.Errorf("claude content %d: %w", contentIndex, err)
		}
	}
	return nil
}

// ValidateGeminiResponse validates every functionCall in every candidate. The
// Gemini wire format has no call-id field, so a request-local structural ID is
// used only to satisfy the common completeness invariant; downstream adapters
// remain responsible for their public protocol IDs.
func ValidateGeminiResponse(policy Policy, response *dto.GeminiChatResponse) error {
	if response == nil {
		return fmt.Errorf("%w: gemini response is nil", ErrMalformedToolCall)
	}
	for candidateIndex := range response.Candidates {
		candidate := &response.Candidates[candidateIndex]
		for partIndex := range candidate.Content.Parts {
			functionCall := candidate.Content.Parts[partIndex].FunctionCall
			if functionCall == nil {
				continue
			}
			arguments, err := common.Marshal(functionCall.Arguments)
			if err != nil {
				return fmt.Errorf("%w: gemini candidate %d part %d arguments: %v", ErrMalformedArguments, candidateIndex, partIndex, err)
			}
			id := fmt.Sprintf("gemini:%d:%d", candidateIndex, partIndex)
			if err := policy.ValidateCall(Call{ID: id, Name: functionCall.FunctionName, Kind: ToolKindFunction, Arguments: arguments}); err != nil {
				return fmt.Errorf("gemini candidate %d part %d: %w", candidateIndex, partIndex, err)
			}
		}
	}
	return nil
}

func IsPolicyViolation(err error) bool {
	return errors.Is(err, ErrUndeclaredTool) ||
		errors.Is(err, ErrDisallowedTool) ||
		errors.Is(err, ErrToolKindMismatch)
}

// NewResponseValidationError preserves the repository's normal retry behavior
// for malformed upstream responses while exposing a stable failure class.
func NewResponseValidationError(err error) *types.NewAPIError {
	code := types.ErrorCodeUpstreamToolProtocolInvalid
	if IsPolicyViolation(err) {
		code = types.ErrorCodeUpstreamToolPolicyViolation
	}
	return types.NewErrorWithStatusCode(err, code, http.StatusBadGateway)
}
