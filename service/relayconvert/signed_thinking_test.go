package relayconvert

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"net/http/httptest"
	"testing"
)

const signedThinkingSignature = "YW50aHJvcGljLXNpZ25hdHVyZS1leGFtcGxl"

func TestClaudeSignedThinkingChatRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			thought := "thought α"
			response := &dto.ClaudeResponse{Id: "msg_test", Model: "claude-test", Content: []dto.ClaudeMediaMessage{
				{Type: "thinking", Thinking: &thought, Signature: signedThinkingSignature},
				{Type: "tool_use", Id: "call_a", Name: "lookup", Input: map[string]any{"q": "go"}},
			}, StopReason: "tool_use"}
			var message dto.Message
			if !stream {
				message = ResponseClaude2OpenAI(response).Choices[0].Message
			} else {
				events := []*dto.ClaudeResponse{
					{Type: "message_start", Message: &dto.ClaudeMediaMessage{Id: "msg_test", Model: "claude-test"}},
					{Type: "content_block_start", Index: common.GetPointer(0), ContentBlock: &dto.ClaudeMediaMessage{Type: "thinking", Thinking: common.GetPointer("")}},
					{Type: "content_block_delta", Index: common.GetPointer(0), Delta: &dto.ClaudeMediaMessage{Type: "thinking_delta", Thinking: common.GetPointer("thought ")}},
					{Type: "content_block_delta", Index: common.GetPointer(0), Delta: &dto.ClaudeMediaMessage{Type: "thinking_delta", Thinking: common.GetPointer("α")}},
					{Type: "content_block_delta", Index: common.GetPointer(0), Delta: &dto.ClaudeMediaMessage{Type: "signature_delta", Signature: signedThinkingSignature}},
					{Type: "content_block_stop", Index: common.GetPointer(0)},
				}
				var reasoning string
				var details gjson.Result
				for _, event := range events {
					got, err := ConvertStreamResponse(c, nil, types.RelayFormatOpenAI, event)
					require.NoError(t, err)
					chunk, ok := got.Value.(*dto.ChatCompletionsStreamResponse)
					if !ok || chunk == nil || len(chunk.Choices) == 0 {
						continue
					}
					reasoning += chunk.Choices[0].Delta.GetReasoningContent()
					raw, err := common.Marshal(chunk)
					require.NoError(t, err)
					if d := gjson.GetBytes(raw, "choices.0.delta.reasoning_details"); d.Exists() {
						require.False(t, details.Exists(), "one complete carrier per block")
						details = d
					}
				}
				assert.Equal(t, thought, reasoning)
				require.True(t, details.Exists(), "stream must retain provider signature")
				raw := `{"role":"assistant","content":"","reasoning_content":"thought α","reasoning_details":` + details.Raw + `,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"go\"}"}}]}`
				require.NoError(t, common.Unmarshal([]byte(raw), &message))
			}
			raw, err := common.Marshal(message)
			require.NoError(t, err)
			assert.JSONEq(t, `[{"type":"reasoning.text","text":"thought α","signature":"`+signedThinkingSignature+`","format":"anthropic-claude-v1","index":0}]`, gjson.GetBytes(raw, "reasoning_details").Raw)
			req := &dto.GeneralOpenAIRequest{Model: "claude-test", MaxTokens: common.GetPointer(uint(32)), Messages: []dto.Message{{Role: "user", Content: "go"}, message, {Role: "tool", ToolCallId: "call_a", Content: "ok"}}}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAnthropic, ChannelBaseUrl: "https://api.anthropic.com"}}
			result, err := ConvertRequest(c, info, types.RelayFormatClaude, req)
			require.NoError(t, err)
			got, ok := result.Value.(*dto.ClaudeRequest)
			require.True(t, ok)
			require.Len(t, got.Messages, 3)
			parts, err := got.Messages[1].ParseContent()
			require.NoError(t, err)
			require.Len(t, parts, 2)
			assert.Equal(t, "thinking", parts[0].Type)
			assert.Equal(t, &thought, parts[0].Thinking)
			assert.Equal(t, signedThinkingSignature, parts[0].Signature)
			assert.Equal(t, "tool_use", parts[1].Type)
			assert.Equal(t, "call_a", parts[1].Id)
			resultParts, err := got.Messages[2].ParseContent()
			require.NoError(t, err)
			require.Len(t, resultParts, 1)
			assert.Equal(t, "call_a", resultParts[0].ToolUseId)
		})
	}
}

func TestSignedThinkingReplayRequiresAnthropicDestinationAndValidCarrier(t *testing.T) {
	for _, tc := range []struct{ name, base, details string }{
		{"gateway", "https://proxy.test", `[{"type":"reasoning.text","text":"thought","signature":"` + signedThinkingSignature + `","format":"anthropic-claude-v1"}]`},
		{"lookalike", "https://api.anthropic.com.evil.test", `[{"type":"reasoning.text","text":"thought","signature":"` + signedThinkingSignature + `"}]`},
		{"unknown metadata", "https://api.anthropic.com", `[{"type":"vendor.future","opaque":{"x":1}}]`},
		{"missing signature", "https://api.anthropic.com", `[{"type":"reasoning.text","text":"thought"}]`},
		{"unsigned", "https://api.anthropic.com", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var message dto.Message
			require.NoError(t, common.Unmarshal([]byte(`{"role":"assistant","content":"answer","reasoning_content":"thought","reasoning_details":`+tc.details+`}`), &message))
			result, err := ConvertRequest(nil, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAnthropic, ChannelBaseUrl: tc.base}}, types.RelayFormatClaude, &dto.GeneralOpenAIRequest{Model: "claude-test", MaxTokens: common.GetPointer(uint(32)), Messages: []dto.Message{{Role: "user", Content: "go"}, message}})
			require.NoError(t, err)
			got := result.Value.(*dto.ClaudeRequest)
			assert.Equal(t, "answer", got.Messages[1].Content)
			encoded, err := common.Marshal(message)
			require.NoError(t, err)
			assert.JSONEq(t, tc.details, gjson.GetBytes(encoded, "reasoning_details").Raw)
		})
	}
}

func TestClaudeThinkingBlocksPreserveRedactionAndAnswer(t *testing.T) {
	thought := "same prefix"
	response := &dto.ClaudeResponse{Content: []dto.ClaudeMediaMessage{
		{Type: "redacted_thinking", Content: nil},
		{Type: "thinking", Thinking: &thought, Signature: signedThinkingSignature},
		{Type: "text", Text: common.GetPointer("same prefix answer")},
	}}
	// Decode the provider field, so the fixture also protects the Claude wire DTO.
	raw := `{"type":"redacted_thinking","data":"opaque-redacted-state"}`
	require.NoError(t, common.Unmarshal([]byte(raw), &response.Content[0]))
	message := ResponseClaude2OpenAI(response).Choices[0].Message
	encoded, err := common.Marshal(message)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"reasoning.encrypted","data":"opaque-redacted-state","format":"anthropic-claude-v1","index":0},{"type":"reasoning.text","text":"same prefix","signature":"`+signedThinkingSignature+`","format":"anthropic-claude-v1","index":1}]`, gjson.GetBytes(encoded, "reasoning_details").Raw)
	result, err := ConvertRequest(nil, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAnthropic, ChannelBaseUrl: "https://api.anthropic.com"}}, types.RelayFormatClaude, &dto.GeneralOpenAIRequest{Model: "claude-test", MaxTokens: common.GetPointer(uint(32)), Messages: []dto.Message{{Role: "user", Content: "go"}, message}})
	require.NoError(t, err)
	parts, err := result.Value.(*dto.ClaudeRequest).Messages[1].ParseContent()
	require.NoError(t, err)
	require.Len(t, parts, 3)
	encoded, err = common.Marshal(parts[0])
	require.NoError(t, err)
	assert.JSONEq(t, raw, string(encoded))
	assert.Equal(t, "same prefix answer", parts[2].GetText())
}

func TestClaudeUnsignedThinkingAndRetryKeepSignatureAssociation(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	events := []*dto.ClaudeResponse{
		{Type: "message_start"},
		{Type: "content_block_start", Index: common.GetPointer(0), ContentBlock: &dto.ClaudeMediaMessage{Type: "thinking", Thinking: common.GetPointer("abandoned"), Signature: signedThinkingSignature}},
		{Type: "message_start"},
		{Type: "content_block_start", Index: common.GetPointer(0), ContentBlock: &dto.ClaudeMediaMessage{Type: "thinking", Thinking: common.GetPointer("plain")}},
		{Type: "content_block_stop", Index: common.GetPointer(0)},
	}
	for _, event := range events {
		result, err := ConvertStreamResponse(c, nil, types.RelayFormatOpenAI, event)
		require.NoError(t, err)
		raw, err := common.Marshal(result.Value)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "reasoning_details")
	}
	unsigned := ResponseClaude2OpenAI(&dto.ClaudeResponse{Content: []dto.ClaudeMediaMessage{{Type: "thinking", Thinking: common.GetPointer("plain")}, {Type: "text", Text: common.GetPointer("answer")}}})
	assert.Equal(t, "plain", unsigned.Choices[0].Message.GetReasoningContent())
	raw, err := common.Marshal(unsigned)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "reasoning_details")
}

func TestClaudeThinkingStatefulRegistryWithoutContext(t *testing.T) {
	state, err := NewResponseStreamState(types.RelayFormatClaude, types.RelayFormatOpenAI, ResponseStreamOptions{})
	require.NoError(t, err)
	events := []*dto.ClaudeResponse{
		{Type: "message_start"},
		{Type: "content_block_start", Index: common.GetPointer(0), ContentBlock: &dto.ClaudeMediaMessage{Type: "thinking", Thinking: common.GetPointer("thought")}},
		{Type: "content_block_delta", Index: common.GetPointer(0), Delta: &dto.ClaudeMediaMessage{Type: "signature_delta", Signature: signedThinkingSignature}},
		{Type: "content_block_stop", Index: common.GetPointer(0)},
	}
	var details string
	for _, event := range events {
		chunks, err := ConvertStreamResponseChunk(nil, nil, state, event)
		require.NoError(t, err)
		for _, chunk := range chunks {
			raw, err := common.Marshal(chunk.Value)
			require.NoError(t, err)
			if d := gjson.GetBytes(raw, "choices.0.delta.reasoning_details"); d.Exists() {
				details = d.Raw
			}
		}
	}
	assert.JSONEq(t, `[{"type":"reasoning.text","text":"thought","signature":"`+signedThinkingSignature+`","format":"anthropic-claude-v1","index":0}]`, details)
}
