package toolpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func streamTestPolicy(t *testing.T) Policy {
	t.Helper()
	policy, err := BuildFromResponses(
		json.RawMessage(`[{"type":"function","name":"lookup"},{"type":"custom","name":"shell"}]`),
		nil,
	)
	require.NoError(t, err)
	return policy
}

func acceptResponsesSuccess(t *testing.T, validator *StreamValidator, output string) json.RawMessage {
	t.Helper()
	terminal := json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":` + output + `}}`)
	emitted, err := validator.Accept(terminal)
	require.NoError(t, err)
	assert.Empty(t, emitted)
	return terminal
}

func TestStreamValidatorBuffersInterleavedResponsesCallsInOriginalToolOrder(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	frames := []json.RawMessage{
		json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_a","call_id":"call_a","name":"lookup","status":"in_progress"}}`),
		json.RawMessage(`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"item_b","call_id":"call_b","name":"lookup","status":"in_progress"}}`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"item_b","delta":"{\"b\":null}"}`),
		json.RawMessage(`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"item_b","call_id":"call_b","name":"lookup","arguments":"{\"b\":null}","status":"completed"}}`),
	}
	for _, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		assert.Empty(t, emitted)
	}

	text := json.RawMessage(`{"type":"response.output_text.delta","output_index":2,"delta":"still working"}`)
	emitted, err := validator.Accept(text)
	require.NoError(t, err)
	require.Equal(t, []json.RawMessage{text}, emitted)

	ending := []json.RawMessage{
		json.RawMessage(`{"type":"response.function_call_arguments.delta","item_id":"item_a","delta":"{\"a\":"}`),
		json.RawMessage(`null`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"true}"}`),
		json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_a","call_id":"call_a","name":"lookup","arguments":"{\"a\":true}","status":"completed"}}`),
	}
	var flushed []json.RawMessage
	for _, frame := range ending {
		emitted, err = validator.Accept(frame)
		require.NoError(t, err)
		flushed = append(flushed, emitted...)
	}
	assert.Empty(t, flushed)
	responseTerminal := acceptResponsesSuccess(t, validator, `[{"type":"function_call","id":"item_a","call_id":"call_a","name":"lookup","arguments":"{\"a\":true}","status":"completed"},{"type":"function_call","id":"item_b","call_id":"call_b","name":"lookup","arguments":"{\"b\":null}","status":"completed"}]`)
	flushed, err = validator.Finish()
	require.NoError(t, err)
	require.Len(t, flushed, 8)
	assert.JSONEq(t, string(frames[0]), string(flushed[0]))
	assert.JSONEq(t, string(frames[1]), string(flushed[1]))
	assert.JSONEq(t, string(frames[2]), string(flushed[2]))
	assert.JSONEq(t, string(frames[3]), string(flushed[3]))
	assert.JSONEq(t, string(ending[0]), string(flushed[4]))
	assert.JSONEq(t, string(ending[2]), string(flushed[5]))
	assert.JSONEq(t, string(ending[3]), string(flushed[6]))
	assert.JSONEq(t, string(responseTerminal), string(flushed[7]))

	finished, err := validator.Finish()
	require.NoError(t, err)
	assert.Empty(t, finished)
}

func TestStreamValidatorAccumulatesSplitChatNameIDAndArguments(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	frames := []json.RawMessage{
		json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"call_","type":"function","function":{"name":"look","arguments":"{\"q\":"}}]},"finish_reason":null}]}`),
		json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"9","function":{"name":"up","arguments":"\"x\"}"}}]},"finish_reason":null}]}`),
	}
	for _, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		assert.Empty(t, emitted)
	}
	terminal := json.RawMessage(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	emitted, err := validator.Accept(terminal)
	require.NoError(t, err)
	assert.Empty(t, emitted)
	emitted, err = validator.Finish()
	require.NoError(t, err)
	require.Len(t, emitted, 3)
	assert.JSONEq(t, string(frames[0]), string(emitted[0]))
	assert.JSONEq(t, string(frames[1]), string(emitted[1]))
	assert.JSONEq(t, string(terminal), string(emitted[2]))
}

func TestStreamValidatorAllowsInterleavedChatChoicesToFinishSeparately(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	frames := []json.RawMessage{
		json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}`),
		json.RawMessage(`{"choices":[{"index":1,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}`),
		json.RawMessage(`{"choices":[{"index":1,"delta":{},"finish_reason":"tool_calls"}]}`),
		json.RawMessage(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
	}
	for _, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		assert.Empty(t, emitted)
	}
	emitted, err := validator.Finish()
	require.NoError(t, err)
	require.Len(t, emitted, len(frames))
}

func TestStreamValidatorWithholdsResponsesSuccessTerminalUntilFinish(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	terminal := json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
	emitted, err := validator.Accept(terminal)
	require.NoError(t, err)
	assert.Empty(t, emitted)

	emitted, err = validator.Finish()
	require.NoError(t, err)
	require.Equal(t, []json.RawMessage{terminal}, emitted)
}

func TestStreamValidatorDropsWithheldSuccessTerminalWhenTrailingFrameArrives(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	_, err := validator.Accept(json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`))
	require.NoError(t, err)
	emitted, err := validator.Accept(json.RawMessage(`{"type":"response.output_text.delta","delta":"late"}`))
	require.ErrorIs(t, err, ErrMalformedToolCall)
	assert.Empty(t, emitted)
	_, err = validator.Finish()
	require.ErrorIs(t, err, ErrMalformedToolCall)
}

func TestStreamValidatorSupportsCustomResponsesInput(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	frames := []json.RawMessage{
		json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","status":"in_progress"}}`),
		json.RawMessage(`{"type":"response.custom_tool_call_input.delta","item_id":"item_1","delta":"printf "}`),
		json.RawMessage(`{"type":"response.custom_tool_call_input.delta","output_index":0,"delta":"ok"}`),
		json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","input":"printf ok","status":"completed"}}`),
	}
	for _, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		assert.Empty(t, emitted)
	}
	acceptResponsesSuccess(t, validator, `[{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","input":"printf ok","status":"completed"}]`)
	emitted, err := validator.Finish()
	require.NoError(t, err)
	require.Len(t, emitted, len(frames)+1)
}

func TestStreamValidatorLinksResponsesOutputItemAndCallIDAliases(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	frames := []json.RawMessage{
		json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"q\":"}`),
		json.RawMessage(`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"null}"}`),
		json.RawMessage(`{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":null}","status":"completed"}}`),
	}
	var emitted []json.RawMessage
	for _, frame := range frames {
		var err error
		emitted, err = validator.Accept(frame)
		require.NoError(t, err)
	}
	assert.Empty(t, emitted)
	acceptResponsesSuccess(t, validator, `[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":null}","status":"completed"}]`)
	emitted, err := validator.Finish()
	require.NoError(t, err)
	require.Len(t, emitted, len(frames)+1)
}

func TestStreamValidatorLinksDirectEqualItemAndCallIDAliases(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	frames := []json.RawMessage{
		json.RawMessage(`{"type":"response.function_call_arguments.delta","item_id":"call_1","delta":"{\"q\":"}`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"null}"}`),
		json.RawMessage(`{"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":null}","status":"completed"}}`),
	}
	var emitted []json.RawMessage
	for _, frame := range frames {
		var err error
		emitted, err = validator.Accept(frame)
		require.NoError(t, err)
	}
	assert.Empty(t, emitted)
	acceptResponsesSuccess(t, validator, `[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":null}","status":"completed"}]`)
	emitted, err := validator.Finish()
	require.NoError(t, err)
	require.Len(t, emitted, len(frames)+1)
}

func TestStreamValidatorRejectsLateOrDuplicateFramesForCompletedCall(t *testing.T) {
	tests := []struct {
		name string
		late json.RawMessage
	}{
		{name: "late delta", late: json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"late"}`)},
		{name: "duplicate done", late: json.RawMessage(`{"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := NewStreamValidator(streamTestPolicy(t))
			doneEmitted, err := validator.Accept(json.RawMessage(`{"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`))
			require.NoError(t, err)
			assert.Empty(t, doneEmitted)
			emitted, err := validator.Accept(test.late)
			require.ErrorIs(t, err, ErrMalformedToolCall)
			assert.Empty(t, emitted)
		})
	}
}

func TestStreamValidatorAllowsCompletedCallToBeReconciledByResponseTerminal(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	done := json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`)
	completed := json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]}}`)
	for _, frame := range []json.RawMessage{done, completed} {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		assert.Empty(t, emitted)
	}
	emitted, err := validator.Finish()
	require.NoError(t, err)
	require.Len(t, emitted, 2)
}

func TestStreamValidatorRejectsResponsesTerminalToolSetMismatch(t *testing.T) {
	done := json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`)
	tests := []struct {
		name     string
		terminal json.RawMessage
		skipDone bool
	}{
		{name: "omitted streamed call", terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)},
		{name: "new terminal call", skipDone: true, terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]}}`)},
		{name: "item identity changed", terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_2","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]}}`)},
		{name: "call identity changed", terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_2","name":"lookup","arguments":"{}","status":"completed"}]}}`)},
		{name: "kind changed", terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","input":"{}","status":"completed"}]}}`)},
		{name: "name changed", terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell","arguments":"{}","status":"completed"}]}}`)},
		{name: "arguments changed", terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"x\":1}","status":"completed"}]}}`)},
		{name: "duplicate terminal call", terminal: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"},{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]}}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := NewStreamValidator(streamTestPolicy(t))
			if !test.skipDone {
				emitted, err := validator.Accept(done)
				require.NoError(t, err)
				assert.Empty(t, emitted)
			}
			emitted, err := validator.Accept(test.terminal)
			require.Error(t, err)
			assert.Empty(t, emitted)
			_, finishErr := validator.Finish()
			require.Error(t, finishErr)
		})
	}
}

func TestStreamValidatorRejectsDeltaOrDuplicateDoneAfterArgumentDone(t *testing.T) {
	tests := []struct {
		name      string
		added     json.RawMessage
		done      json.RawMessage
		lateDelta json.RawMessage
	}{
		{
			name:      "function",
			added:     json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`),
			done:      json.RawMessage(`{"type":"response.function_call_arguments.done","call_id":"call_1","arguments":"{}"}`),
			lateDelta: json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"late"}`),
		},
		{
			name:      "custom",
			added:     json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","status":"in_progress"}}`),
			done:      json.RawMessage(`{"type":"response.custom_tool_call_input.done","call_id":"call_1","input":"echo ok"}`),
			lateDelta: json.RawMessage(`{"type":"response.custom_tool_call_input.delta","call_id":"call_1","delta":"late"}`),
		},
	}
	for _, test := range tests {
		for _, late := range []struct {
			name  string
			frame json.RawMessage
		}{{name: "delta", frame: test.lateDelta}, {name: "duplicate done", frame: test.done}} {
			t.Run(test.name+"/"+late.name, func(t *testing.T) {
				validator := NewStreamValidator(streamTestPolicy(t))
				for _, frame := range []json.RawMessage{test.added, test.done} {
					emitted, err := validator.Accept(frame)
					require.NoError(t, err)
					assert.Empty(t, emitted)
				}
				emitted, err := validator.Accept(late.frame)
				require.ErrorIs(t, err, ErrMalformedToolCall)
				assert.Empty(t, emitted)
			})
		}
	}
}

func TestStreamValidatorRejectsConflictingCrossNamespaceAliases(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	_, err := validator.Accept(json.RawMessage(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"item_a","delta":"{}"}`))
	require.NoError(t, err)
	_, err = validator.Accept(json.RawMessage(`{"type":"response.custom_tool_call_input.delta","output_index":1,"call_id":"call_b","delta":"echo"}`))
	require.NoError(t, err)
	emitted, err := validator.Accept(json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_a","call_id":"call_b","name":"lookup","status":"in_progress"}}`))
	require.ErrorIs(t, err, ErrMalformedToolCall)
	assert.Empty(t, emitted)
}

func TestStreamValidatorRejectsResponsesErrorTerminals(t *testing.T) {
	for _, eventType := range []string{"response.failed", "response.error", "response.incomplete", "error"} {
		t.Run(eventType, func(t *testing.T) {
			validator := NewStreamValidator(streamTestPolicy(t))
			emitted, err := validator.Accept(json.RawMessage(`{"type":"` + eventType + `","error":{"message":"upstream failed"}}`))
			require.Error(t, err)
			assert.Empty(t, emitted)
			_, finishErr := validator.Finish()
			require.Error(t, finishErr)
		})
	}
}

func TestStreamValidatorRejectsNativeChatErrorObject(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	emitted, err := validator.Accept(json.RawMessage(`{"error":{"message":"upstream failed","type":"server_error"}}`))
	require.Error(t, err)
	assert.Empty(t, emitted)
}

func TestStreamValidatorRejectsPresentNullErrorField(t *testing.T) {
	for _, frame := range []json.RawMessage{
		json.RawMessage(`{"choices":[],"error":null}`),
		json.RawMessage(`{"type":"response.output_text.delta","delta":"safe","error":null}`),
	} {
		validator := NewStreamValidator(streamTestPolicy(t))
		emitted, err := validator.Accept(frame)
		require.ErrorIs(t, err, ErrMalformedToolCall)
		assert.Empty(t, emitted)
	}
}

func TestStreamValidatorFinishRejectsResponsesToolWithoutValidatedTerminal(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	_, err := validator.Accept(json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`))
	require.NoError(t, err)
	emitted, err := validator.Finish()
	require.ErrorIs(t, err, ErrIncompleteToolCall)
	assert.Empty(t, emitted)
}

func TestStreamValidatorFinishRejectsTextOnlyResponsesWithoutValidatedTerminal(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	emitted, err := validator.Accept(json.RawMessage(`{"type":"response.output_text.delta","delta":"partial"}`))
	require.NoError(t, err)
	require.Len(t, emitted, 1)
	emitted, err = validator.Finish()
	require.ErrorIs(t, err, ErrIncompleteToolCall)
	assert.Empty(t, emitted)
}

func TestStreamValidatorRequiredTextOnlyFailsWithoutEmitting(t *testing.T) {
	policy, err := BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, "required")
	require.NoError(t, err)
	validator := NewStreamValidator(policy)
	emitted, err := validator.Accept(json.RawMessage(`{"choices":[{"index":0,"delta":{"content":"must stay buffered"},"finish_reason":"stop"}]}`))
	require.NoError(t, err)
	assert.Empty(t, emitted)
	emitted, err = validator.Finish()
	require.ErrorIs(t, err, ErrDisallowedTool)
	assert.Empty(t, emitted)
}

func TestStreamValidatorNamedChoiceTextOnlyFailsForChatAndResponses(t *testing.T) {
	policy, err := BuildFromChat(
		[]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
	)
	require.NoError(t, err)
	for name, frame := range map[string]json.RawMessage{
		"chat":      json.RawMessage(`{"choices":[{"index":0,"delta":{"content":"text"},"finish_reason":"stop"}]}`),
		"responses": json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","status":"completed"}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			validator := NewStreamValidator(policy)
			emitted, err := validator.Accept(frame)
			if err == nil {
				require.Empty(t, emitted)
				emitted, err = validator.Finish()
			}
			require.ErrorIs(t, err, ErrDisallowedTool)
			assert.Empty(t, emitted)
		})
	}
}

func TestStreamValidatorResourceBoundsFailClosed(t *testing.T) {
	t.Run("single frame bytes", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		frame := json.RawMessage(`{"type":"response.output_text.delta","delta":"` + strings.Repeat("x", streamMaxFrameBytes) + `"}`)
		emitted, err := validator.Accept(frame)
		require.ErrorIs(t, err, ErrMalformedToolCall)
		assert.Empty(t, emitted)
	})
	t.Run("active calls", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		for i := 0; i <= streamMaxActiveCalls; i++ {
			frame := json.RawMessage(fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":{"type":"function_call","id":"item_%d","call_id":"call_%d","name":"lookup","status":"in_progress"}}`, i, i, i))
			emitted, err := validator.Accept(frame)
			if i < streamMaxActiveCalls {
				require.NoError(t, err)
				assert.Empty(t, emitted)
			} else {
				require.ErrorIs(t, err, ErrMalformedToolCall)
				assert.Empty(t, emitted)
			}
		}
	})
	t.Run("aliases", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		for i := 0; i <= streamMaxAliases+10; i++ {
			frame := json.RawMessage(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":%d,"call_id":"shared","delta":""}`, i))
			_, err := validator.Accept(frame)
			if err != nil {
				require.ErrorIs(t, err, ErrMalformedToolCall)
				return
			}
		}
		t.Fatal("alias limit did not fail")
	})
	t.Run("buffered frames", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		added := json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`)
		for i := 0; i <= streamMaxBufferedFrames; i++ {
			_, err := validator.Accept(added)
			if i < streamMaxBufferedFrames {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrMalformedToolCall)
			}
		}
	})
	t.Run("per call arguments", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		frame := json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"` + strings.Repeat("x", streamMaxCallArgumentBytes+1) + `"}`)
		_, err := validator.Accept(frame)
		require.ErrorIs(t, err, ErrMalformedToolCall)
	})
	t.Run("total buffered bytes", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		padding := strings.Repeat("x", 4096)
		for i := 0; i < streamMaxBufferedBytes/4096+10; i++ {
			frame := json.RawMessage(fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"padding":%q,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`, padding))
			_, err := validator.Accept(frame)
			if err != nil {
				require.ErrorIs(t, err, ErrMalformedToolCall)
				return
			}
		}
		t.Fatal("buffered byte limit did not fail")
	})
}

func TestStreamValidatorResourceBoundsAllowAtLimit(t *testing.T) {
	t.Run("single frame bytes", func(t *testing.T) {
		prefix := `{"type":"response.output_text.delta","delta":"`
		suffix := `"}`
		frame := json.RawMessage(prefix + strings.Repeat("x", streamMaxFrameBytes-len(prefix)-len(suffix)) + suffix)
		require.Len(t, frame, streamMaxFrameBytes)
		emitted, err := NewStreamValidator(streamTestPolicy(t)).Accept(frame)
		require.NoError(t, err)
		require.Equal(t, []json.RawMessage{frame}, emitted)
	})
	t.Run("active calls", func(t *testing.T) {
		var calls strings.Builder
		for i := 0; i < streamMaxActiveCalls; i++ {
			if i > 0 {
				calls.WriteByte(',')
			}
			fmt.Fprintf(&calls, `{"index":%d,"id":"call_%d","type":"function","function":{"name":"lookup","arguments":"{}"}}`, i, i)
		}
		validator := NewStreamValidator(streamTestPolicy(t))
		_, err := validator.Accept(json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[` + calls.String() + `]},"finish_reason":null}]}`))
		require.NoError(t, err)
		_, err = validator.Accept(json.RawMessage(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
		require.NoError(t, err)
		emitted, err := validator.Finish()
		require.NoError(t, err)
		require.Len(t, emitted, 2)
	})
	t.Run("aliases", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		const outputAliases = streamMaxAliases - 4
		for i := 0; i < outputAliases; i++ {
			_, err := validator.Accept(json.RawMessage(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":%d,"call_id":"call_1","delta":""}`, i)))
			require.NoError(t, err)
		}
		done := json.RawMessage(fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`, outputAliases-1))
		_, err := validator.Accept(done)
		require.NoError(t, err)
		require.Len(t, validator.aliases, streamMaxAliases)
		acceptResponsesSuccess(t, validator, fmt.Sprintf(`[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]`))
		_, err = validator.Finish()
		require.NoError(t, err)
	})
	t.Run("buffered frames", func(t *testing.T) {
		validator := NewStreamValidator(streamTestPolicy(t))
		added := json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`)
		for i := 0; i < streamMaxBufferedFrames-2; i++ {
			_, err := validator.Accept(added)
			require.NoError(t, err)
		}
		done := json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`)
		emitted, err := validator.Accept(done)
		require.NoError(t, err)
		assert.Empty(t, emitted)
		acceptResponsesSuccess(t, validator, `[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]`)
		emitted, err = validator.Finish()
		require.NoError(t, err)
		require.Len(t, emitted, streamMaxBufferedFrames)
	})
	t.Run("per call arguments", func(t *testing.T) {
		arguments := `{"value":"` + strings.Repeat("x", streamMaxCallArgumentBytes-len(`{"value":""}`)) + `"}`
		require.Len(t, arguments, streamMaxCallArgumentBytes)
		validator := NewStreamValidator(streamTestPolicy(t))
		done := json.RawMessage(fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":%q,"status":"completed"}}`, arguments))
		_, err := validator.Accept(done)
		require.NoError(t, err)
		acceptResponsesSuccess(t, validator, fmt.Sprintf(`[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":%q,"status":"completed"}]`, arguments))
		_, err = validator.Finish()
		require.NoError(t, err)
	})
	t.Run("total buffered bytes", func(t *testing.T) {
		done := `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`
		terminalOutput := `[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]`
		terminal := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":` + terminalOutput + `}}`
		prefix := `{"type":"response.output_item.added","output_index":0,"padding":"`
		suffix := `","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`
		paddingBytes := streamMaxBufferedBytes - len(done) - len(terminal) - 2*(len(prefix)+len(suffix))
		addedOne := prefix + strings.Repeat("x", paddingBytes/2) + suffix
		addedTwo := prefix + strings.Repeat("x", paddingBytes-paddingBytes/2) + suffix
		require.LessOrEqual(t, len(addedOne), streamMaxFrameBytes)
		require.LessOrEqual(t, len(addedTwo), streamMaxFrameBytes)
		require.Equal(t, streamMaxBufferedBytes, len(addedOne)+len(addedTwo)+len(done)+len(terminal))
		validator := NewStreamValidator(streamTestPolicy(t))
		_, err := validator.Accept(json.RawMessage(addedOne))
		require.NoError(t, err)
		_, err = validator.Accept(json.RawMessage(addedTwo))
		require.NoError(t, err)
		emitted, err := validator.Accept(json.RawMessage(done))
		require.NoError(t, err)
		assert.Empty(t, emitted)
		acceptResponsesSuccess(t, validator, terminalOutput)
		emitted, err = validator.Finish()
		require.NoError(t, err)
		require.Len(t, emitted, 4)
	})
}

func TestStreamValidatorRejectsNullOrWrongTypedProtocolFields(t *testing.T) {
	frames := []json.RawMessage{
		json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":null}`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":7,"delta":"{}"}`),
		json.RawMessage(`{"type":"response.function_call_arguments.done","call_id":"call_1","arguments":null}`),
		json.RawMessage(`{"type":"response.function_call_arguments.done","call_id":"call_1","arguments":{}}`),
		json.RawMessage(`{"type":"response.output_item.added","item":{"type":"function_call","id":null,"call_id":"call_1","name":"lookup"}}`),
		json.RawMessage(`{"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":null}}`),
		json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":{}}]}}`),
		json.RawMessage(`{"choices":[{"index":0,"delta":null,"finish_reason":null}]}`),
		json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":null,"type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}`),
		json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":null,"arguments":"{}"}}]},"finish_reason":null}]}`),
		json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":null}}]},"finish_reason":null}]}`),
	}
	for _, frame := range frames {
		validator := NewStreamValidator(streamTestPolicy(t))
		emitted, err := validator.Accept(frame)
		require.ErrorIs(t, err, ErrMalformedToolCall, "frame: %s", frame)
		assert.Empty(t, emitted)
	}
}

func TestStreamValidatorFailClosedCases(t *testing.T) {
	tests := []struct {
		name   string
		frames []json.RawMessage
		finish bool
		want   error
	}{
		{
			name: "duplicate raw keys",
			frames: []json.RawMessage{
				json.RawMessage(`{"choices":[],"choices":[]}`),
			},
			want: ErrMalformedToolCall,
		},
		{
			name: "missing name at chat terminal",
			frames: []json.RawMessage{
				json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"arguments":"{}"}}]},"finish_reason":null}]}`),
				json.RawMessage(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
			},
			want: ErrMalformedToolCall,
		},
		{
			name: "responses success terminal cannot complete pending item",
			frames: []json.RawMessage{
				json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`),
				json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`),
			},
			want: ErrIncompleteToolCall,
		},
		{
			name: "EOF with pending call",
			frames: []json.RawMessage{
				json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`),
			},
			finish: true,
			want:   ErrIncompleteToolCall,
		},
		{
			name: "malformed assembled arguments",
			frames: []json.RawMessage{
				json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`),
				json.RawMessage(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{bad"}`),
				json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"completed"}}`),
			},
			want: ErrMalformedArguments,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := NewStreamValidator(streamTestPolicy(t))
			var got error
			for _, frame := range test.frames {
				_, got = validator.Accept(frame)
				if got != nil {
					break
				}
			}
			if got == nil && test.finish {
				_, got = validator.Finish()
			}
			require.Error(t, got)
			assert.True(t, errors.Is(got, test.want), "got %v, want %v", got, test.want)
		})
	}
}

func TestStreamValidatorMissingPolicyAllowsTextButRejectsTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	validator := NewStreamValidatorFromContext(c)

	text := json.RawMessage(`{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)
	emitted, err := validator.Accept(text)
	require.NoError(t, err)
	require.Equal(t, []json.RawMessage{text}, emitted)

	_, err = validator.Accept(json.RawMessage(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}`))
	require.NoError(t, err)
	_, err = validator.Accept(json.RawMessage(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
	require.ErrorIs(t, err, ErrMissingToolPolicy)
}

func TestStreamValidatorWholeNullFrameIsPaddingNotArgumentNull(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	emitted, err := validator.Accept(json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Empty(t, emitted)

	for _, frame := range []json.RawMessage{
		json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"value\":nu"}`),
		json.RawMessage(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"ll}"}`),
	} {
		_, err = validator.Accept(frame)
		require.NoError(t, err)
	}
	terminal := json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"value\":null}","status":"completed"}}`)
	emitted, err = validator.Accept(terminal)
	require.NoError(t, err)
	assert.Empty(t, emitted)
	acceptResponsesSuccess(t, validator, `[{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"value\":null}","status":"completed"}]`)
	emitted, err = validator.Finish()
	require.NoError(t, err)
	assert.Len(t, emitted, 5)
}

func TestStreamValidatorRejectsMutationAfterFailure(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	_, err := validator.Accept(json.RawMessage(`{"choices":[],"choices":[]}`))
	require.Error(t, err)

	_, nextErr := validator.Accept(json.RawMessage(`{"choices":[{"index":0,"delta":{"content":"must not leak"},"finish_reason":null}]}`))
	require.Error(t, nextErr)
	assert.Equal(t, err.Error(), nextErr.Error())
	_, finishErr := validator.Finish()
	require.Error(t, finishErr)
	assert.Equal(t, err.Error(), finishErr.Error())
}

func TestStreamValidatorRejectsResponsesMetadataMutationAtDone(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	added := json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"delete_all","status":"in_progress"}}`)
	emitted, err := validator.Accept(added)
	require.NoError(t, err)
	assert.Empty(t, emitted)

	done := json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`)
	emitted, err = validator.Accept(done)
	require.ErrorIs(t, err, ErrMalformedToolCall)
	assert.Empty(t, emitted)
}

func TestStreamValidatorRejectsSuccessTerminalWithoutResponse(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	emitted, err := validator.Accept(json.RawMessage(`{"type":"response.completed"}`))
	require.ErrorIs(t, err, ErrIncompleteToolCall)
	assert.Empty(t, emitted)
}

func TestStreamValidatorRejectsCompletedEventWithNonCompletedResponse(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	emitted, err := validator.Accept(json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"incomplete","output":[]}}`))
	require.ErrorIs(t, err, ErrIncompleteToolCall)
	assert.Empty(t, emitted)
}

func TestStreamValidatorRejectsChatToolTerminalWithoutCall(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	emitted, err := validator.Accept(json.RawMessage(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
	require.ErrorIs(t, err, ErrIncompleteToolCall)
	assert.Empty(t, emitted)
}

func TestStreamValidatorRejectsCustomCallWithoutInput(t *testing.T) {
	validator := NewStreamValidator(streamTestPolicy(t))
	_, err := validator.Accept(json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","status":"in_progress"}}`))
	require.NoError(t, err)
	_, err = validator.Accept(json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","status":"completed"}}`))
	require.ErrorIs(t, err, ErrMalformedArguments)
}

func TestStreamValidatorAcceptsDeclaredBuiltInResponsesCall(t *testing.T) {
	policy, err := BuildFromResponses(json.RawMessage(`[{"type":"web_search_preview"}]`), nil)
	require.NoError(t, err)
	validator := NewStreamValidator(policy)
	frames := []json.RawMessage{
		json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"in_progress"}}`),
		json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed"}}`),
	}
	for _, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		assert.Empty(t, emitted)
	}
	acceptResponsesSuccess(t, validator, `[{"type":"web_search_call","id":"ws_1","status":"completed"}]`)
	emitted, err := validator.Finish()
	require.NoError(t, err)
	assert.Len(t, emitted, 3)
}

func TestChatTargetStreamValidatorRejectsCustomToolConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	Set(c, streamTestPolicy(t))
	validator := NewStreamValidatorForChatContext(c)
	_, err := validator.Accept(json.RawMessage(`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","status":"in_progress"}}`))
	require.NoError(t, err)
	emitted, err := validator.Accept(json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"shell","input":"echo ok","status":"completed"}}`))
	require.ErrorIs(t, err, ErrUnsupportedToolCallConversion)
	assert.Empty(t, emitted)
}

func TestStreamValidatorUsesUniqueJSONDecoder(t *testing.T) {
	var value any
	require.Error(t, common.UnmarshalUnique([]byte(`{"delta":{"x":1,"x":2}}`), &value))
}
