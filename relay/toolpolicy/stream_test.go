package toolpolicy

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
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
	require.Len(t, flushed, 7)
	assert.JSONEq(t, string(frames[0]), string(flushed[0]))
	assert.JSONEq(t, string(frames[1]), string(flushed[1]))
	assert.JSONEq(t, string(frames[2]), string(flushed[2]))
	assert.JSONEq(t, string(frames[3]), string(flushed[3]))
	assert.JSONEq(t, string(ending[0]), string(flushed[4]))
	assert.JSONEq(t, string(ending[2]), string(flushed[5]))
	assert.JSONEq(t, string(ending[3]), string(flushed[6]))

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
	for index, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		if index < len(frames)-1 {
			assert.Empty(t, emitted)
		} else {
			require.Len(t, emitted, len(frames))
		}
	}
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
	require.Len(t, emitted, len(frames))
	_, err := validator.Finish()
	require.NoError(t, err)
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
	assert.Len(t, emitted, 4)
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
	for index, frame := range frames {
		emitted, err := validator.Accept(frame)
		require.NoError(t, err)
		if index == 0 {
			assert.Empty(t, emitted)
		} else {
			assert.Len(t, emitted, 2)
		}
	}
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
