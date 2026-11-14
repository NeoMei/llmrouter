package deepseek

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shared Responses helper only calls providers that opt in; this keeps the
// method signature wired to the interface the helper type-asserts.
var _ channel.ResponsesRequestFinalizer = (*Adaptor)(nil)

// Synthetic shapes reproduced against DeepSeek's native Responses endpoint on
// 2026-10-28; see also https://github.com/yetone/magpie/issues/1822.
// An empty string fails while a space before the assistant step succeeds.
func TestResponsesReasoningReplay(t *testing.T) {
	// Exercise the final wire-body hook without dropping unknown JSON fields.
	finalize := func(info *relaycommon.RelayInfo, req dto.OpenAIResponsesRequest) (any, error) {
		data, err := common.Marshal(req)
		if err != nil {
			return nil, err
		}
		data, err = (&Adaptor{}).FinalizeResponsesRequest(info, data)
		if err != nil {
			return nil, err
		}
		var out dto.OpenAIResponsesRequest
		err = common.Unmarshal(data, &out)
		return out, err
	}
	const r = `{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":" "}]}`
	const note = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking."}]}`
	const call = `{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}"}`
	const output = `{"type":"function_call_output","call_id":"c1","output":"7"}`
	const call2 = `{"type":"function_call","call_id":"c2","name":"probe","arguments":"{}"}`
	const output2 = `{"type":"function_call_output","call_id":"c2","output":"8"}`
	const customCall = `{"type":"custom_tool_call","call_id":"custom1","name":"apply_patch","input":"synthetic"}`
	const customOutput = `{"type":"custom_tool_call_output","call_id":"custom1","output":"ok"}`
	const actual = `{"type":"reasoning","id":"r1","summary":[],"encrypted_content":"opaque","content":[{"type":"reasoning_text","text":"Existing reasoning"}],"extra":9007199254740993}`
	tests := []struct{ name, input, want string }{
		{"missing", "[" + note + "," + call + "," + output + "]", "[" + r + "," + note + "," + call + "," + output + "]"},
		{"empty", `[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":""}]},` + call + "]", "[" + r + "," + call + "]"},
		{"null content", `[{"type":"reasoning","summary":[],"content":null},` + call + "]", "[" + r + "," + call + "]"},
		{"missing content", `[{"type":"reasoning","summary":[]},` + call + "]", "[" + r + "," + call + "]"},
		{"empty content", `[{"type":"reasoning","summary":[],"content":[]},` + call + "]", "[" + r + "," + call + "]"},
		{"move existing", "[" + note + "," + actual + "," + call + "," + output + "]", "[" + actual + "," + note + "," + call + "," + output + "]"},
		{"preserve", "[" + actual + "," + note + "," + call + "," + output + "]", "[" + actual + "," + note + "," + call + "," + output + "]"},
		{"interleaved", "[" + call + "," + output + "," + call2 + "," + output2 + "]", "[" + r + "," + call + "," + output + "," + r + "," + call2 + "," + output2 + "]"},
		{"after tool", "[" + call + "," + output + "," + note + "]", "[" + r + "," + call + "," + output + "," + r + "," + note + "]"},
		{"new user boundary", "[" + call + "," + output + `,{"role":"user","content":"Next"},` + note + "]", "[" + r + "," + call + "," + output + `,{"role":"user","content":"Next"},` + note + "]"},
		{"parallel", "[" + note + "," + call + "," + call2 + "," + output + "," + output2 + "]", "[" + r + "," + note + "," + call + "," + call2 + "," + output + "," + output2 + "]"},
		{"missing text", `[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text"}]},` + call + "]", "[" + r + "," + call + "]"},
		{"null text", `[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":null}]},` + call + "]", "[" + r + "," + call + "]"},
		{"preserve whitespace", "[" + r + "," + call + "]", "[" + r + "," + call + "]"},
		{"unknown boundary", "[" + call + "," + output + `,{"type":"future_item"},` + note + "]", "[" + r + "," + call + "," + output + `,{"type":"future_item"},` + note + "]"},
		{"custom tool", "[" + note + "," + customCall + "," + customOutput + "," + note + "]", "[" + r + "," + note + "," + customCall + "," + customOutput + "," + r + "," + note + "]"},
		{"message only", "[" + note + "]", "[" + note + "]"},
		{"string input", `"hello"`, `"hello"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","input":`+tt.input+`}`), &req))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://api.deepseek.com"}}
			converted, err := finalize(info, req)
			require.NoError(t, err)
			out := converted.(dto.OpenAIResponsesRequest)
			assert.JSONEq(t, tt.want, string(out.Input))
			if strings.Contains(tt.input, `"extra":9007199254740993`) {
				assert.Contains(t, string(out.Input), `"extra":9007199254740993`)
			}
			assert.Equal(t, tt.input, string(req.Input), "input belongs to the caller")
			again, err := finalize(info, out)
			require.NoError(t, err)
			assert.Equal(t, string(out.Input), string(again.(dto.OpenAIResponsesRequest).Input))
		})
	}
	for _, endpoint := range []string{
		"", "https://api.deepseek.com/v1", "https://api.deepseek.com:443",
		"https://API.DEEPSEEK.COM", "https://Api.DeepSeek.Com:443/v1",
	} {
		t.Run("official "+endpoint, func(t *testing.T) {
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","input":[`+call+`]}`), &req))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: endpoint}}
			converted, err := finalize(info, req)
			require.NoError(t, err)
			assert.JSONEq(t, "["+r+","+call+"]", string(converted.(dto.OpenAIResponsesRequest).Input))
		})
	}
	for _, endpoint := range []string{
		"https://other.example", "https://api.deepseek.com.example", "http://api.deepseek.com",
		"https://API.DEEPSEEK.COM:8443", "https://api.deepseek.com:invalid",
	} {
		t.Run(endpoint, func(t *testing.T) {
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","input":[`+call+`]}`), &req))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: endpoint}}
			converted, err := finalize(info, req)
			require.NoError(t, err)
			assert.Equal(t, req.Input, converted.(dto.OpenAIResponsesRequest).Input)
		})
	}
	t.Run("thinking disabled", func(t *testing.T) {
		var req dto.OpenAIResponsesRequest
		require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","reasoning":{"effort":"none"},"input":[`+call+`]}`), &req))
		converted, err := finalize(nil, req)
		require.NoError(t, err)
		assert.Equal(t, req.Input, converted.(dto.OpenAIResponsesRequest).Input)
	})
}
