package relayconvert

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOutboundReasoningDetailsOnlyKeepSupportedProviderSignatures(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		channel    int
		preserve   bool
	}{
		{"anthropic", "https://api.anthropic.com", constant.ChannelTypeAnthropic, true},
		{"anthropic proxy", "https://proxy.test", constant.ChannelTypeAnthropic, false},
		{"openrouter", "https://openrouter.ai/api", constant.ChannelTypeOpenRouter, true},
		{"openrouter proxy", "https://proxy.test", constant.ChannelTypeOpenRouter, false},
		{"openai", "https://api.openai.com", constant.ChannelTypeOpenAI, false},
		{"deepseek", "https://api.deepseek.com", constant.ChannelTypeDeepSeek, false},
		{"gateway", "https://proxy.test", constant.ChannelTypeGateway, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `[{"type":"reasoning.text","text":"thought","signature":"` + signedThinkingSignature + `","format":"anthropic-claude-v1"},{"type":"reasoning.encrypted","data":"opaque","format":"anthropic-claude-v1"},{"type":"vendor.future","format":"other-v1","opaque":{"keep":true}}]`
			var message dto.Message
			require.NoError(t, common.Unmarshal([]byte(`{"role":"assistant","content":"answer","reasoning_content":"thought","reasoning_details":`+raw+`}`), &message))
			request := &dto.GeneralOpenAIRequest{Messages: []dto.Message{message}}
			FilterUnsupportedReasoningDetails(request, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: tc.channel, ChannelBaseUrl: tc.base}})
			if tc.preserve {
				assert.JSONEq(t, raw, string(request.Messages[0].ReasoningDetails))
			} else {
				assert.JSONEq(t, `[{"type":"vendor.future","format":"other-v1","opaque":{"keep":true}}]`, string(request.Messages[0].ReasoningDetails))
			}
			assert.Equal(t, "thought", request.Messages[0].GetReasoningContent())
			assert.Equal(t, "answer", request.Messages[0].StringContent())
		})
	}
}
