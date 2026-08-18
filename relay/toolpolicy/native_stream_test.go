package toolpolicy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamValidatorsRejectInvalidToolsWithoutEmitting(t *testing.T) {
	tests := []struct {
		name   string
		kind   NativeStreamKind
		frames []json.RawMessage
	}{
		{name: "claude", kind: NativeStreamClaude, frames: []json.RawMessage{
			json.RawMessage(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"delete_all","input":{}}}`),
			json.RawMessage(`{"type":"content_block_stop","index":0}`),
		}},
		{name: "gemini", kind: NativeStreamGemini, frames: []json.RawMessage{
			json.RawMessage(`{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"delete_all","args":{}}}]}}]}`),
		}},
		{name: "ollama", kind: NativeStreamOllama, frames: []json.RawMessage{
			json.RawMessage(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"delete_all","arguments":{}}}]},"done":false}`),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := NewNativeStreamValidator(test.kind, streamTestPolicy(t), true)
			var got error
			for _, frame := range test.frames {
				emitted, err := validator.Accept(frame)
				assert.Empty(t, emitted)
				if err != nil {
					got = err
					break
				}
			}
			require.ErrorIs(t, got, ErrUndeclaredTool)
		})
	}
}

func TestNativeStreamValidatorsRequireProviderSuccessTerminal(t *testing.T) {
	tests := []struct {
		name  string
		kind  NativeStreamKind
		frame json.RawMessage
	}{
		{name: "claude", kind: NativeStreamClaude, frame: json.RawMessage(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`)},
		{name: "gemini", kind: NativeStreamGemini, frame: json.RawMessage(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`)},
		{name: "ollama", kind: NativeStreamOllama, frame: json.RawMessage(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"lookup","arguments":{}}}]},"done":false}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := NewNativeStreamValidator(test.kind, streamTestPolicy(t), true)
			emitted, err := validator.Accept(test.frame)
			require.NoError(t, err)
			assert.Empty(t, emitted)
			emitted, err = validator.Finish()
			require.ErrorIs(t, err, ErrIncompleteToolCall)
			assert.Empty(t, emitted)
		})
	}
}

func TestNativeStreamValidatorsFlushValidToolsOnlyAfterSuccessTerminal(t *testing.T) {
	tests := []struct {
		name   string
		kind   NativeStreamKind
		frames []json.RawMessage
	}{
		{name: "claude", kind: NativeStreamClaude, frames: []json.RawMessage{
			json.RawMessage(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`),
			json.RawMessage(`{"type":"content_block_stop","index":0}`),
			json.RawMessage(`{"type":"message_stop"}`),
		}},
		{name: "gemini", kind: NativeStreamGemini, frames: []json.RawMessage{
			json.RawMessage(`{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`),
		}},
		{name: "ollama", kind: NativeStreamOllama, frames: []json.RawMessage{
			json.RawMessage(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"lookup","arguments":{}}}]},"done":false}`),
			json.RawMessage(`{"done":true,"done_reason":"stop"}`),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := NewNativeStreamValidator(test.kind, streamTestPolicy(t), true)
			for _, frame := range test.frames {
				emitted, err := validator.Accept(frame)
				require.NoError(t, err)
				assert.Empty(t, emitted)
			}
			emitted, err := validator.Finish()
			require.NoError(t, err)
			require.Len(t, emitted, len(test.frames))
		})
	}
}

func TestNativeStreamValidatorsWithholdSuccessTerminalBeforeLateInvalidFrame(t *testing.T) {
	tests := []struct {
		name     string
		kind     NativeStreamKind
		terminal json.RawMessage
		late     json.RawMessage
	}{
		{name: "claude", kind: NativeStreamClaude,
			terminal: json.RawMessage(`{"type":"message_stop"}`),
			late:     json.RawMessage(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`)},
		{name: "gemini", kind: NativeStreamGemini,
			terminal: json.RawMessage(`{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"text":"done"}]}}]}`),
			late:     json.RawMessage(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`)},
		{name: "ollama", kind: NativeStreamOllama,
			terminal: json.RawMessage(`{"message":{"role":"assistant","content":"done"},"done":true}`),
			late:     json.RawMessage(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"lookup","arguments":{}}}]},"done":false}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := NewNativeStreamValidator(test.kind, streamTestPolicy(t), true)
			emitted, err := validator.Accept(test.terminal)
			require.NoError(t, err)
			assert.Empty(t, emitted)
			emitted, err = validator.Accept(test.late)
			require.ErrorIs(t, err, ErrMalformedToolCall)
			assert.Empty(t, emitted)
		})
	}
}
