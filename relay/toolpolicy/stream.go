package toolpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

// StreamValidator validates one upstream Chat Completions or Responses stream.
// Text-only frames are returned immediately. Frames that contribute to a tool
// call remain request-local until that call reaches a complete, valid state.
type StreamValidator struct {
	policy                 Policy
	hasPolicy              bool
	aliases                map[string]*pendingStreamCall
	calls                  map[*pendingStreamCall]struct{}
	frames                 []*pendingStreamFrame
	sequence               uint64
	failed                 error
	chatGate               *pendingStreamCall
	responseGate           *pendingStreamCall
	responseEnded          bool
	rejectCustomConversion bool
	chatSeen               map[int]struct{}
	chatFinished           map[int]struct{}
}

type pendingStreamCall struct {
	protocol        string
	chatChoice      int
	kind            ToolKind
	id              string
	name            string
	typeName        string
	nativeType      string
	arguments       []streamFragment
	finalArgs       json.RawMessage
	argumentsDoneAt uint64
	inputDoneAt     uint64
	completed       bool
	createdAt       uint64
}

type streamFragment struct {
	sequence uint64
	value    string
}

type pendingStreamFrame struct {
	sequence uint64
	raw      json.RawMessage
	owners   map[*pendingStreamCall]struct{}
}

type streamEnvelope struct {
	Type        string                       `json:"type"`
	Response    *dto.OpenAIResponsesResponse `json:"response,omitempty"`
	Item        *dto.ResponsesOutput         `json:"item,omitempty"`
	OutputIndex *int                         `json:"output_index,omitempty"`
	ItemID      string                       `json:"item_id,omitempty"`
	CallID      string                       `json:"call_id,omitempty"`
	Delta       json.RawMessage              `json:"delta,omitempty"`
	Arguments   json.RawMessage              `json:"arguments,omitempty"`
	Input       json.RawMessage              `json:"input,omitempty"`
	Usage       json.RawMessage              `json:"usage,omitempty"`
	Error       json.RawMessage              `json:"error,omitempty"`
	Choices     []streamChatChoice           `json:"choices,omitempty"`
}

type streamChatChoice struct {
	Index        int             `json:"index"`
	Delta        streamChatDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type streamChatDelta struct {
	ToolCalls []streamChatToolCall `json:"tool_calls,omitempty"`
}

type streamChatToolCall struct {
	Index    *int               `json:"index,omitempty"`
	ID       string             `json:"id,omitempty"`
	Type     json.RawMessage    `json:"type,omitempty"`
	Function streamChatFunction `json:"function"`
}

type streamChatFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

func NewStreamValidator(policy Policy) *StreamValidator {
	return newStreamValidator(policy, true)
}

// NewStreamValidatorFromContext distinguishes a legitimately empty tool
// catalog from missing request policy. Only a tool-bearing frame needs policy;
// ordinary text streams remain compatible when no policy was captured.
func NewStreamValidatorFromContext(c *gin.Context) *StreamValidator {
	policy, ok := FromContext(c)
	return newStreamValidator(policy, ok)
}

// NewStreamValidatorForChatContext applies the same additional restriction as
// the non-stream Responses-to-Chat boundary: custom_tool_call has no faithful
// Chat Completions representation and must not be coerced into a function.
func NewStreamValidatorForChatContext(c *gin.Context) *StreamValidator {
	validator := NewStreamValidatorFromContext(c)
	validator.rejectCustomConversion = true
	return validator
}

func newStreamValidator(policy Policy, hasPolicy bool) *StreamValidator {
	return &StreamValidator{
		policy:       policy,
		hasPolicy:    hasPolicy,
		aliases:      make(map[string]*pendingStreamCall),
		calls:        make(map[*pendingStreamCall]struct{}),
		chatSeen:     make(map[int]struct{}),
		chatFinished: make(map[int]struct{}),
	}
}

// Accept consumes one complete JSON SSE data value. A whole JSON null is
// padding and is ignored; the bytes spelling null inside an argument fragment
// remain part of that argument and are validated normally.
func (v *StreamValidator) Accept(event json.RawMessage) ([]json.RawMessage, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: stream validator is nil", ErrMalformedToolCall)
	}
	if v.failed != nil {
		return nil, v.failed
	}
	raw := bytes.TrimSpace(event)
	if bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, v.fail(fmt.Errorf("%w: empty stream event", ErrMalformedToolCall))
	}
	if err := ValidateRawResponseJSON(raw); err != nil {
		return nil, v.fail(err)
	}
	hasError, err := validateStreamFieldTypes(raw)
	if err != nil {
		return nil, v.fail(err)
	}
	if hasError {
		return nil, v.fail(fmt.Errorf("%w: upstream stream returned an error terminal", ErrMalformedToolCall))
	}

	var envelope streamEnvelope
	if err := common.UnmarshalUnique(raw, &envelope); err != nil {
		return nil, v.fail(fmt.Errorf("%w: invalid stream event: %v", ErrMalformedToolCall, err))
	}
	v.sequence++
	sequence := v.sequence
	owners := make(map[*pendingStreamCall]struct{})
	toolRelated := false
	if v.responseEnded {
		return nil, v.fail(fmt.Errorf("%w: frame arrived after responses terminal", ErrMalformedToolCall))
	}
	if v.chatGate != nil && len(envelope.Choices) == 0 && envelope.Type == "" {
		if len(bytes.TrimSpace(envelope.Usage)) == 0 || len(bytes.TrimSpace(envelope.Error)) > 0 {
			return nil, v.fail(fmt.Errorf("%w: non-usage chat frame arrived after success terminal", ErrMalformedToolCall))
		}
		if !v.allSeenChatChoicesFinished() {
			return nil, v.fail(fmt.Errorf("%w: chat usage arrived before every choice completed", ErrIncompleteToolCall))
		}
	}

	if len(envelope.Choices) > 0 {
		var err error
		toolRelated, err = v.acceptChatFrame(sequence, &envelope, owners)
		if err != nil {
			return nil, v.fail(err)
		}
	} else if envelope.Type != "" {
		var err error
		toolRelated, err = v.acceptResponsesFrame(sequence, &envelope, owners)
		if err != nil {
			return nil, v.fail(err)
		}
	}
	if v.chatGate != nil && len(envelope.Choices) > 0 {
		toolRelated = true
		owners[v.chatGate] = struct{}{}
	}
	if v.chatGate != nil && len(envelope.Choices) == 0 && envelope.Type == "" {
		toolRelated = true
		owners[v.chatGate] = struct{}{}
	}

	cloned := append(json.RawMessage(nil), raw...)
	if toolRelated {
		v.frames = append(v.frames, &pendingStreamFrame{sequence: sequence, raw: cloned, owners: owners})
		return v.flushReady(), nil
	}
	return []json.RawMessage{cloned}, nil
}

// Finish must be called for every termination path: [DONE], terminal EOF,
// scanner error, timeout, or client cancellation. It never promotes an
// incomplete call into a success.
func (v *StreamValidator) Finish() ([]json.RawMessage, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: stream validator is nil", ErrMalformedToolCall)
	}
	if v.failed != nil {
		return nil, v.failed
	}
	for call := range v.calls {
		if call == v.chatGate || call == v.responseGate {
			continue
		}
		if !call.completed {
			return nil, v.fail(fmt.Errorf("%w: stream ended with a pending %s tool call", ErrIncompleteToolCall, call.protocol))
		}
	}
	if v.chatGate != nil && !v.allSeenChatChoicesFinished() {
		return nil, v.fail(fmt.Errorf("%w: stream ended before every chat choice completed", ErrIncompleteToolCall))
	}
	if v.chatGate != nil {
		v.chatGate.completed = true
	}
	if v.responseGate != nil {
		v.responseGate.completed = true
	}
	return v.flushReady(), nil
}

func (v *StreamValidator) acceptChatFrame(sequence uint64, envelope *streamEnvelope, owners map[*pendingStreamCall]struct{}) (bool, error) {
	toolRelated := false
	for choiceIndex := range envelope.Choices {
		choice := &envelope.Choices[choiceIndex]
		if _, finished := v.chatFinished[choice.Index]; finished {
			return false, fmt.Errorf("%w: chat choice %d emitted a frame after completion", ErrMalformedToolCall, choice.Index)
		}
		v.chatSeen[choice.Index] = struct{}{}
		for callPosition := range choice.Delta.ToolCalls {
			toolRelated = true
			delta := &choice.Delta.ToolCalls[callPosition]
			toolIndex := callPosition
			if delta.Index != nil {
				toolIndex = *delta.Index
			}
			alias := "chat:" + strconv.Itoa(choice.Index) + ":" + strconv.Itoa(toolIndex)
			call, err := v.resolveCall([]string{alias}, "chat", choice.Index, sequence, false)
			if err != nil {
				return false, err
			}
			owners[call] = struct{}{}
			call.id += delta.ID
			call.name += delta.Function.Name
			if typeName, err := rawJSONString(delta.Type); err != nil {
				return false, fmt.Errorf("%w: invalid chat tool type: %v", ErrMalformedToolCall, err)
			} else if typeName != "" {
				call.typeName += typeName
			}
			if delta.Function.Arguments != "" {
				call.arguments = append(call.arguments, streamFragment{sequence: sequence, value: delta.Function.Arguments})
			}
		}

		if choice.FinishReason == nil || strings.TrimSpace(*choice.FinishReason) == "" {
			continue
		}
		calls := v.incompleteChatCalls(choice.Index)
		if len(calls) == 0 {
			if strings.TrimSpace(*choice.FinishReason) == "tool_calls" {
				return false, fmt.Errorf("%w: chat choice %d declared tool completion without a call", ErrIncompleteToolCall, choice.Index)
			}
			toolRelated = true
			gate := v.ensureTerminalGate("chat-terminal", sequence)
			v.chatGate = gate
			v.holdBufferedFramesUntil(gate)
			owners[gate] = struct{}{}
			v.chatFinished[choice.Index] = struct{}{}
			continue
		}
		toolRelated = true
		if strings.TrimSpace(*choice.FinishReason) != "tool_calls" {
			return false, fmt.Errorf("%w: chat choice %d ended with %q while a tool call was pending", ErrIncompleteToolCall, choice.Index, *choice.FinishReason)
		}
		for _, call := range calls {
			owners[call] = struct{}{}
			if err := v.completeCall(call); err != nil {
				return false, err
			}
		}
		gate := v.ensureTerminalGate("chat-terminal", sequence)
		v.chatGate = gate
		v.holdBufferedFramesUntil(gate)
		owners[gate] = struct{}{}
		v.chatFinished[choice.Index] = struct{}{}
	}
	return toolRelated, nil
}

func (v *StreamValidator) acceptResponsesFrame(sequence uint64, envelope *streamEnvelope, owners map[*pendingStreamCall]struct{}) (bool, error) {
	eventType := strings.TrimSpace(envelope.Type)
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		if envelope.Item == nil || !isStreamToolItem(envelope.Item.Type) {
			return false, nil
		}
		call, err := v.resolveResponsesCall(sequence, envelope.OutputIndex, envelope.ItemID, envelope.CallID, envelope.Item, false)
		if err != nil {
			return false, err
		}
		owners[call] = struct{}{}
		gate := v.ensureTerminalGate("responses-terminal", sequence)
		v.responseGate = gate
		owners[gate] = struct{}{}
		if err := v.applyResponsesItem(call, envelope.Item); err != nil {
			return false, err
		}
		if eventType == "response.output_item.done" {
			if envelope.Item.Status != "" && envelope.Item.Status != "completed" {
				return false, fmt.Errorf("%w: responses tool item ended with status %q", ErrIncompleteToolCall, envelope.Item.Status)
			}
			if err := v.completeCall(call); err != nil {
				return false, err
			}
		}
		return true, nil
	case "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		kind := ToolKindFunction
		if strings.Contains(eventType, "custom_tool_call_input") {
			kind = ToolKindCustom
		}
		call, err := v.resolveResponsesCall(sequence, envelope.OutputIndex, envelope.ItemID, envelope.CallID, nil, false)
		if err != nil {
			return false, err
		}
		if call.kind != "" && call.kind != kind {
			return false, fmt.Errorf("%w: stream changed tool kind", ErrMalformedToolCall)
		}
		call.kind = kind
		if kind == ToolKindFunction && call.argumentsDoneAt != 0 {
			return false, fmt.Errorf("%w: function arguments frame arrived after arguments.done", ErrMalformedToolCall)
		}
		if kind == ToolKindCustom && call.inputDoneAt != 0 {
			return false, fmt.Errorf("%w: custom input frame arrived after input.done", ErrMalformedToolCall)
		}
		owners[call] = struct{}{}
		gate := v.ensureTerminalGate("responses-terminal", sequence)
		v.responseGate = gate
		owners[gate] = struct{}{}
		if delta, err := rawJSONString(envelope.Delta); err != nil {
			return false, fmt.Errorf("%w: invalid tool argument delta: %v", ErrMalformedToolCall, err)
		} else if delta != "" {
			call.arguments = append(call.arguments, streamFragment{sequence: sequence, value: delta})
		}
		if len(bytes.TrimSpace(envelope.Arguments)) > 0 {
			call.finalArgs = append(json.RawMessage(nil), envelope.Arguments...)
		}
		if len(bytes.TrimSpace(envelope.Input)) > 0 {
			call.finalArgs = append(json.RawMessage(nil), envelope.Input...)
		}
		if strings.HasSuffix(eventType, ".done") {
			if kind == ToolKindFunction {
				call.argumentsDoneAt = sequence
			} else {
				call.inputDoneAt = sequence
			}
		}
		return true, nil
	case "response.completed", "response.done":
		if envelope.Response == nil {
			return false, fmt.Errorf("%w: responses success terminal has no response", ErrIncompleteToolCall)
		}
		var status string
		if err := common.UnmarshalUnique(envelope.Response.Status, &status); err != nil || status != "completed" {
			return false, fmt.Errorf("%w: responses success terminal has status %q", ErrIncompleteToolCall, status)
		}
		if err := v.validateResponsesTerminal(sequence, envelope.Response, owners); err != nil {
			return false, err
		}
		for call := range v.calls {
			if call == v.chatGate || call == v.responseGate {
				continue
			}
			if !call.completed {
				return false, fmt.Errorf("%w: responses success terminal left a tool call pending", ErrIncompleteToolCall)
			}
		}
		gate := v.ensureTerminalGate("responses-terminal", sequence)
		v.responseGate = gate
		v.responseEnded = true
		owners[gate] = struct{}{}
		return true, nil
	case "response.failed", "response.error", "response.incomplete", "error":
		for call := range v.calls {
			if !call.completed {
				return false, fmt.Errorf("%w: responses stream terminated with a pending tool call", ErrIncompleteToolCall)
			}
		}
		v.responseEnded = true
		return false, fmt.Errorf("%w: upstream responses stream ended with %s", ErrMalformedToolCall, eventType)
	default:
		return false, nil
	}
}

func (v *StreamValidator) validateResponsesTerminal(sequence uint64, response *dto.OpenAIResponsesResponse, owners map[*pendingStreamCall]struct{}) error {
	if v.rejectCustomConversion {
		for outputIndex := range response.Output {
			if response.Output[outputIndex].Type == "custom_tool_call" {
				return fmt.Errorf("responses output %d: %w: Chat Completions cannot represent custom tool calls", outputIndex, ErrUnsupportedToolCallConversion)
			}
		}
	}
	if err := v.validatePolicy(func(policy Policy) error { return ValidateResponsesResponse(policy, response) }); err != nil {
		return err
	}
	seen := make(map[*pendingStreamCall]struct{})
	for outputIndex := range response.Output {
		item := &response.Output[outputIndex]
		if !isStreamToolItem(item.Type) {
			continue
		}
		index := outputIndex
		call, err := v.resolveCompletedResponsesCall(&index, item)
		if err != nil {
			return err
		}
		if _, duplicate := seen[call]; duplicate {
			return fmt.Errorf("%w: responses terminal repeated a streamed tool call", ErrMalformedToolCall)
		}
		seen[call] = struct{}{}
		owners[call] = struct{}{}
		if err := v.applyResponsesItem(call, item); err != nil {
			return err
		}
	}
	for call := range v.calls {
		if call == v.chatGate || call == v.responseGate || call.protocol != "responses" {
			continue
		}
		if !call.completed {
			return fmt.Errorf("%w: responses terminal referenced a pending tool call", ErrIncompleteToolCall)
		}
		if _, ok := seen[call]; !ok {
			return fmt.Errorf("%w: responses terminal omitted a streamed tool call", ErrMalformedToolCall)
		}
	}
	return nil
}

func (v *StreamValidator) resolveCompletedResponsesCall(outputIndex *int, item *dto.ResponsesOutput) (*pendingStreamCall, error) {
	aliases := responsesCallAliases(outputIndex, "", "", item)
	var call *pendingStreamCall
	for _, alias := range aliases {
		candidate := v.aliases[alias]
		if candidate == nil {
			continue
		}
		if call == nil {
			call = candidate
		} else if call != candidate {
			return nil, fmt.Errorf("%w: responses terminal aliases conflict", ErrMalformedToolCall)
		}
	}
	if call == nil || !call.completed || call.protocol != "responses" {
		return nil, fmt.Errorf("%w: responses terminal introduced an uncompleted tool call", ErrMalformedToolCall)
	}
	// Identity aliases must already have been observed on the streamed call.
	// Output indexes may be learned at the aggregate terminal, but item/call IDs
	// may not silently change there.
	for _, identity := range []string{strings.TrimSpace(item.ID), strings.TrimSpace(item.CallId)} {
		if identity == "" {
			continue
		}
		if v.aliases["responses:raw:"+identity] != call {
			return nil, fmt.Errorf("%w: responses terminal changed tool identity", ErrMalformedToolCall)
		}
	}
	return call, nil
}

func (v *StreamValidator) completeCall(call *pendingStreamCall) error {
	if call == nil {
		return fmt.Errorf("%w: missing pending call", ErrMalformedToolCall)
	}
	kind := call.kind
	if kind == "" {
		kind = ToolKind(strings.TrimSpace(call.typeName))
	}
	if kind == "" {
		kind = ToolKindFunction
	}
	if call.protocol == "chat" {
		typeName := strings.TrimSpace(call.typeName)
		if typeName != string(ToolKindFunction) && typeName != string(ToolKindCustom) {
			return fmt.Errorf("%w: chat tool call has invalid type %q", ErrMalformedToolCall, typeName)
		}
		kind = ToolKind(typeName)
	}
	if v.rejectCustomConversion && call.nativeType == "custom_tool_call" {
		return fmt.Errorf("%w: Chat Completions cannot represent custom tool calls", ErrUnsupportedToolCallConversion)
	}
	arguments := call.finalArgs
	fragmentText := call.fragmentText()
	if len(bytes.TrimSpace(arguments)) == 0 {
		if kind == ToolKindCustom && len(call.arguments) > 0 {
			encoded, err := common.Marshal(fragmentText)
			if err != nil {
				return fmt.Errorf("%w: custom input: %v", ErrMalformedArguments, err)
			}
			arguments = encoded
		} else {
			arguments = json.RawMessage(fragmentText)
		}
	} else if kind == ToolKindFunction {
		arguments = unwrapFunctionArguments(arguments)
	}
	if len(call.arguments) > 0 {
		terminalText, err := streamArgumentText(kind, arguments)
		if err != nil {
			return err
		}
		if terminalText != fragmentText {
			return fmt.Errorf("%w: terminal tool arguments differ from streamed deltas", ErrMalformedToolCall)
		}
	}
	err := v.validatePolicy(func(policy Policy) error {
		return policy.ValidateCall(Call{ID: call.id, Name: call.name, Kind: kind, Arguments: arguments})
	})
	if err != nil {
		return err
	}
	call.completed = true
	return nil
}

func (v *StreamValidator) validatePolicy(validate func(Policy) error) error {
	err := validate(v.policy)
	if err != nil && !v.hasPolicy {
		return fmt.Errorf("%w: upstream emitted a streamed tool call without a captured request policy: %v", ErrMissingToolPolicy, err)
	}
	return err
}

func (v *StreamValidator) resolveResponsesCall(sequence uint64, outputIndex *int, itemID, callID string, item *dto.ResponsesOutput, allowCompleted bool) (*pendingStreamCall, error) {
	aliases := responsesCallAliases(outputIndex, itemID, callID, item)
	if len(aliases) == 0 {
		aliases = append(aliases, "responses:anonymous:"+strconv.FormatUint(sequence, 10))
	}
	return v.resolveCall(aliases, "responses", 0, sequence, allowCompleted)
}

func responsesCallAliases(outputIndex *int, itemID, callID string, item *dto.ResponsesOutput) []string {
	aliases := make([]string, 0, 8)
	if outputIndex != nil {
		aliases = append(aliases, "responses:output:"+strconv.Itoa(*outputIndex))
	}
	if itemID = strings.TrimSpace(itemID); itemID != "" {
		aliases = append(aliases, "responses:item:"+itemID, "responses:raw:"+itemID)
	}
	if callID = strings.TrimSpace(callID); callID != "" {
		aliases = append(aliases, "responses:call:"+callID, "responses:raw:"+callID)
	}
	if item != nil {
		if id := strings.TrimSpace(item.ID); id != "" {
			aliases = append(aliases, "responses:item:"+id, "responses:raw:"+id)
		}
		if callID := strings.TrimSpace(item.CallId); callID != "" {
			aliases = append(aliases, "responses:call:"+callID, "responses:raw:"+callID)
		}
	}
	return aliases
}

func (v *StreamValidator) resolveCall(aliases []string, protocol string, chatChoice int, sequence uint64, allowCompleted bool) (*pendingStreamCall, error) {
	var call *pendingStreamCall
	for _, alias := range aliases {
		candidate := v.aliases[alias]
		if candidate == nil {
			continue
		}
		if call == nil {
			call = candidate
		} else if call != candidate {
			var err error
			call, err = v.mergeCalls(call, candidate)
			if err != nil {
				return nil, err
			}
		}
	}
	if call != nil && call.completed && !allowCompleted {
		return nil, fmt.Errorf("%w: frame referenced completed tool call", ErrMalformedToolCall)
	}
	if call == nil {
		call = &pendingStreamCall{protocol: protocol, chatChoice: chatChoice, createdAt: sequence}
		v.calls[call] = struct{}{}
	}
	for _, alias := range aliases {
		v.aliases[alias] = call
	}
	return call, nil
}

func (v *StreamValidator) ensureTerminalGate(protocol string, sequence uint64) *pendingStreamCall {
	if protocol == "chat-terminal" && v.chatGate != nil {
		return v.chatGate
	}
	if protocol == "responses-terminal" && v.responseGate != nil {
		return v.responseGate
	}
	gate := &pendingStreamCall{protocol: protocol, createdAt: sequence}
	v.calls[gate] = struct{}{}
	return gate
}

func (v *StreamValidator) holdBufferedFramesUntil(gate *pendingStreamCall) {
	if gate == nil {
		return
	}
	for _, frame := range v.frames {
		frame.owners[gate] = struct{}{}
	}
}

func (v *StreamValidator) mergeCalls(left, right *pendingStreamCall) (*pendingStreamCall, error) {
	if left == right || right == nil {
		return left, nil
	}
	if left.completed || right.completed {
		return nil, fmt.Errorf("%w: alias attempted to reopen a completed tool call", ErrMalformedToolCall)
	}
	if left == nil || right.createdAt < left.createdAt {
		left, right = right, left
	}
	if left.protocol != right.protocol || (left.protocol == "chat" && left.chatChoice != right.chatChoice) {
		return nil, fmt.Errorf("%w: conflicting tool call aliases", ErrMalformedToolCall)
	}
	if err := mergeStableString(&left.id, right.id, "id"); err != nil {
		return nil, err
	}
	if err := mergeStableString(&left.name, right.name, "name"); err != nil {
		return nil, err
	}
	if err := mergeStableString(&left.typeName, right.typeName, "type"); err != nil {
		return nil, err
	}
	if err := mergeStableString(&left.nativeType, right.nativeType, "native type"); err != nil {
		return nil, err
	}
	if left.kind == "" {
		left.kind = right.kind
	} else if right.kind != "" && left.kind != right.kind {
		return nil, fmt.Errorf("%w: conflicting tool call kinds", ErrMalformedToolCall)
	}
	left.arguments = append(left.arguments, right.arguments...)
	if left.argumentsDoneAt != 0 && right.argumentsDoneAt != 0 {
		return nil, fmt.Errorf("%w: duplicate function arguments.done aliases", ErrMalformedToolCall)
	}
	if left.inputDoneAt != 0 && right.inputDoneAt != 0 {
		return nil, fmt.Errorf("%w: duplicate custom input.done aliases", ErrMalformedToolCall)
	}
	if left.argumentsDoneAt == 0 {
		left.argumentsDoneAt = right.argumentsDoneAt
	}
	if left.inputDoneAt == 0 {
		left.inputDoneAt = right.inputDoneAt
	}
	for _, fragment := range left.arguments {
		if left.kind == ToolKindFunction && left.argumentsDoneAt != 0 && fragment.sequence > left.argumentsDoneAt {
			return nil, fmt.Errorf("%w: function argument delta followed arguments.done", ErrMalformedToolCall)
		}
		if left.kind == ToolKindCustom && left.inputDoneAt != 0 && fragment.sequence > left.inputDoneAt {
			return nil, fmt.Errorf("%w: custom input delta followed input.done", ErrMalformedToolCall)
		}
	}
	if len(left.finalArgs) == 0 {
		left.finalArgs = right.finalArgs
	} else if len(right.finalArgs) > 0 && !bytes.Equal(bytes.TrimSpace(left.finalArgs), bytes.TrimSpace(right.finalArgs)) {
		return nil, fmt.Errorf("%w: conflicting terminal tool arguments", ErrMalformedToolCall)
	}
	for alias, call := range v.aliases {
		if call == right {
			v.aliases[alias] = left
		}
	}
	for _, frame := range v.frames {
		if _, ok := frame.owners[right]; ok {
			delete(frame.owners, right)
			frame.owners[left] = struct{}{}
		}
	}
	delete(v.calls, right)
	return left, nil
}

func mergeStableString(target *string, value, field string) error {
	if value == "" {
		return nil
	}
	if *target != "" && *target != value {
		return fmt.Errorf("%w: conflicting tool call %s", ErrMalformedToolCall, field)
	}
	*target = value
	return nil
}

func (v *StreamValidator) applyResponsesItem(call *pendingStreamCall, item *dto.ResponsesOutput) error {
	if call == nil || item == nil {
		return nil
	}
	call.protocol = "responses"
	if call.nativeType != "" && call.nativeType != item.Type {
		return fmt.Errorf("%w: responses stream changed native tool type", ErrMalformedToolCall)
	}
	call.nativeType = item.Type
	kind := streamToolKind(item.Type)
	if call.kind != "" && call.kind != kind {
		return fmt.Errorf("%w: responses stream changed tool kind", ErrMalformedToolCall)
	}
	call.kind = kind
	id := strings.TrimSpace(item.CallId)
	if id == "" {
		id = strings.TrimSpace(item.ID)
	}
	if call.id != "" && id != "" && call.id != id {
		return fmt.Errorf("%w: responses stream changed tool call id", ErrMalformedToolCall)
	}
	if id := strings.TrimSpace(item.CallId); id != "" {
		call.id = id
	} else if call.id == "" {
		call.id = strings.TrimSpace(item.ID)
	}
	name := strings.TrimSpace(item.Name)
	if name == "" && item.Type != "function_call" && item.Type != "custom_tool_call" {
		name = responsesBuiltInDeclarationName(v.policy, item.Type)
	}
	if call.name != "" && name != "" && call.name != name {
		return fmt.Errorf("%w: responses stream changed tool name", ErrMalformedToolCall)
	}
	if name != "" {
		call.name = name
	}
	var finalArgs json.RawMessage
	if call.kind == ToolKindCustom {
		if len(bytes.TrimSpace(item.Input)) > 0 {
			finalArgs = item.Input
		} else if item.Type != "custom_tool_call" {
			finalArgs = json.RawMessage(`{}`)
		}
	} else if len(bytes.TrimSpace(item.Arguments)) > 0 {
		arguments := bytes.TrimSpace(item.Arguments)
		if !bytes.Equal(arguments, []byte(`""`)) {
			finalArgs = item.Arguments
		}
	}
	if len(bytes.TrimSpace(finalArgs)) > 0 {
		if len(bytes.TrimSpace(call.finalArgs)) > 0 && !bytes.Equal(bytes.TrimSpace(call.finalArgs), bytes.TrimSpace(finalArgs)) {
			return fmt.Errorf("%w: responses stream changed terminal tool arguments", ErrMalformedToolCall)
		}
		call.finalArgs = append(json.RawMessage(nil), finalArgs...)
	}
	return nil
}

func (c *pendingStreamCall) fragmentText() string {
	fragments := append([]streamFragment(nil), c.arguments...)
	sort.SliceStable(fragments, func(i, j int) bool { return fragments[i].sequence < fragments[j].sequence })
	var builder strings.Builder
	for _, fragment := range fragments {
		builder.WriteString(fragment.value)
	}
	return builder.String()
}

func streamArgumentText(kind ToolKind, arguments json.RawMessage) (string, error) {
	if kind == ToolKindCustom {
		if common.GetJsonType(arguments) != "string" {
			return "", fmt.Errorf("%w: streamed custom input deltas require a string terminal input", ErrMalformedArguments)
		}
		var value string
		if err := common.UnmarshalUnique(arguments, &value); err != nil {
			return "", fmt.Errorf("%w: %v", ErrMalformedArguments, err)
		}
		return value, nil
	}
	return string(bytes.TrimSpace(arguments)), nil
}

func (v *StreamValidator) incompleteChatCalls(choice int) []*pendingStreamCall {
	calls := make([]*pendingStreamCall, 0)
	for call := range v.calls {
		if call.protocol == "chat" && call.chatChoice == choice && !call.completed {
			calls = append(calls, call)
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].createdAt < calls[j].createdAt })
	return calls
}

func (v *StreamValidator) allSeenChatChoicesFinished() bool {
	for choice := range v.chatSeen {
		if _, finished := v.chatFinished[choice]; !finished {
			return false
		}
	}
	return true
}

func (v *StreamValidator) flushReady() []json.RawMessage {
	var emitted []json.RawMessage
	consumed := 0
	for _, frame := range v.frames {
		ready := true
		for owner := range frame.owners {
			if !owner.completed {
				ready = false
				break
			}
		}
		if !ready {
			break
		}
		emitted = append(emitted, append(json.RawMessage(nil), frame.raw...))
		consumed++
	}
	if consumed > 0 {
		v.frames = append([]*pendingStreamFrame(nil), v.frames[consumed:]...)
	}
	return emitted
}

func (v *StreamValidator) fail(err error) error {
	if err == nil {
		return nil
	}
	v.failed = err
	return err
}

func rawJSONString(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var value string
	if err := common.UnmarshalUnique(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

// validateStreamFieldTypes runs before DTO decoding so JSON null cannot be
// collapsed into a Go zero value and mistaken for an absent fragment.
func validateStreamFieldTypes(raw json.RawMessage) (bool, error) {
	object, err := rawObject(raw, "stream event")
	if err != nil {
		return false, err
	}
	for _, field := range []string{"type", "item_id", "call_id"} {
		if value, ok := object[field]; ok {
			if err := requireRawString(value, field); err != nil {
				return false, err
			}
		}
	}
	if value, ok := object["error"]; ok {
		if isRawNull(value) {
			return false, fmt.Errorf("%w: error must not be null", ErrMalformedToolCall)
		}
		return true, nil
	}
	if value, ok := object["choices"]; ok {
		var choices []json.RawMessage
		if isRawNull(value) || common.UnmarshalUnique(value, &choices) != nil {
			return false, fmt.Errorf("%w: choices must be an array", ErrMalformedToolCall)
		}
		for index, choiceRaw := range choices {
			choice, err := rawObject(choiceRaw, fmt.Sprintf("choices[%d]", index))
			if err != nil {
				return false, err
			}
			if value, ok := choice["delta"]; ok {
				delta, err := rawObject(value, fmt.Sprintf("choices[%d].delta", index))
				if err != nil {
					return false, err
				}
				if toolCallsRaw, ok := delta["tool_calls"]; ok {
					var toolCalls []json.RawMessage
					if isRawNull(toolCallsRaw) || common.UnmarshalUnique(toolCallsRaw, &toolCalls) != nil {
						return false, fmt.Errorf("%w: choices[%d].delta.tool_calls must be an array", ErrMalformedToolCall, index)
					}
					for callIndex, callRaw := range toolCalls {
						call, err := rawObject(callRaw, fmt.Sprintf("choices[%d].delta.tool_calls[%d]", index, callIndex))
						if err != nil {
							return false, err
						}
						for _, field := range []string{"id", "type"} {
							if value, ok := call[field]; ok {
								if err := requireRawString(value, field); err != nil {
									return false, err
								}
							}
						}
						if functionRaw, ok := call["function"]; ok {
							function, err := rawObject(functionRaw, "tool call function")
							if err != nil {
								return false, err
							}
							for _, field := range []string{"name", "arguments"} {
								if value, ok := function[field]; ok {
									if err := requireRawString(value, field); err != nil {
										return false, err
									}
								}
							}
						}
					}
				}
			}
		}
		return false, nil
	}
	if value, ok := object["delta"]; ok {
		if err := requireRawString(value, "delta"); err != nil {
			return false, err
		}
	}
	for _, field := range []string{"arguments", "input"} {
		if value, ok := object[field]; ok {
			if err := requireRawString(value, field); err != nil {
				return false, err
			}
		}
	}
	if itemRaw, ok := object["item"]; ok {
		if err := validateRawResponsesItem(itemRaw, "item"); err != nil {
			return false, err
		}
	}
	if responseRaw, ok := object["response"]; ok {
		response, err := rawObject(responseRaw, "response")
		if err != nil {
			return false, err
		}
		if value, ok := response["status"]; ok {
			if err := requireRawString(value, "response.status"); err != nil {
				return false, err
			}
		}
		if outputRaw, ok := response["output"]; ok {
			var output []json.RawMessage
			if isRawNull(outputRaw) || common.UnmarshalUnique(outputRaw, &output) != nil {
				return false, fmt.Errorf("%w: response.output must be an array", ErrMalformedToolCall)
			}
			for index, itemRaw := range output {
				if err := validateRawResponsesItem(itemRaw, fmt.Sprintf("response.output[%d]", index)); err != nil {
					return false, err
				}
			}
		}
	}
	return false, nil
}

func validateRawResponsesItem(raw json.RawMessage, path string) error {
	item, err := rawObject(raw, path)
	if err != nil {
		return err
	}
	for _, field := range []string{"type", "id", "call_id", "name", "status"} {
		if value, ok := item[field]; ok {
			if err := requireRawString(value, path+"."+field); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"arguments", "input"} {
		if value, ok := item[field]; ok {
			if err := requireRawString(value, path+"."+field); err != nil {
				return err
			}
		}
	}
	return nil
}

func rawObject(raw json.RawMessage, field string) (map[string]json.RawMessage, error) {
	if isRawNull(raw) {
		return nil, fmt.Errorf("%w: %s must be an object", ErrMalformedToolCall, field)
	}
	var object map[string]json.RawMessage
	if err := common.UnmarshalUnique(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: %s must be an object", ErrMalformedToolCall, field)
	}
	return object, nil
}

func requireRawString(raw json.RawMessage, field string) error {
	if isRawNull(raw) {
		return fmt.Errorf("%w: %s must be a string", ErrMalformedToolCall, field)
	}
	var value string
	if err := common.UnmarshalUnique(raw, &value); err != nil {
		return fmt.Errorf("%w: %s must be a string", ErrMalformedToolCall, field)
	}
	return nil
}

func isRawNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func isStreamToolItem(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call" || strings.HasSuffix(itemType, "_call")
}

func streamToolKind(itemType string) ToolKind {
	if itemType == "function_call" {
		return ToolKindFunction
	}
	return ToolKindCustom
}
