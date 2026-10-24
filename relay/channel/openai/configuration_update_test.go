package openai

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeResponsesConfigurationUpdateDestinationCapability(t *testing.T) {
	input := json.RawMessage(`[{"type":"message","role":"user","content":"go","unknown":{"keep":true}},{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"function_call","call_id":"call_a","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":"ok"}]`)
	for _, tc := range []struct {
		name, base string
		channel    int
		preserve   bool
	}{
		{"official", "https://api.openai.com", constant.ChannelTypeOpenAI, true},
		{"official prefix", "https://api.openai.com/v1", constant.ChannelTypeOpenAI, true},
		{"default official", "", constant.ChannelTypeOpenAI, true},
		{"gateway", "https://upstream.example.com", constant.ChannelTypeOpenAI, false},
		{"lookalike", "https://api.openai.com.evil.test", constant.ChannelTypeOpenAI, false},
		{"openrouter", "https://openrouter.ai/api", constant.ChannelTypeOpenRouter, true},
		{"azure", "https://example.openai.azure.com", constant.ChannelTypeAzure, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := responsesCompatInfo(false)
			info.ChannelBaseUrl = tc.base
			info.ChannelType = tc.channel
			got, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, dto.OpenAIResponsesRequest{Model: "test", Input: input})
			require.NoError(t, err)
			out, ok := got.(dto.OpenAIResponsesRequest)
			require.True(t, ok)
			if tc.preserve {
				assert.JSONEq(t, string(input), string(out.Input))
			} else {
				var items []map[string]any
				require.NoError(t, common.Unmarshal(out.Input, &items))
				require.Len(t, items, 3)
				assert.Equal(t, "message", items[0]["type"])
				assert.Equal(t, map[string]any{"keep": true}, items[0]["unknown"])
				assert.Equal(t, "function_call", items[1]["type"])
				assert.Equal(t, "function_call_output", items[2]["type"])
			}
			assert.Contains(t, string(input), `configuration_update`)
		})
	}
}
