package claude

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeStreamFinalUsageEstimatesOnlyUncountedUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, start, delta    string
		text, done            bool
		wantInput, wantOutput int
		wantEstimated         bool
	}{
		{name: "cache-only refusal preserves zero ordinary counts", start: `{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":20000,"cache_creation":{"ephemeral_1h_input_tokens":20000}}`, delta: `{"output_tokens":0}`, done: true},
		{name: "broken stream preserves cache-only input", start: `{"input_tokens":0,"output_tokens":1,"cache_read_input_tokens":50000}`, text: true, wantOutput: 2, wantEstimated: true},
		{name: "zeros with text are estimated", start: `{"input_tokens":0,"output_tokens":0}`, delta: `{"output_tokens":0}`, text: true, done: true, wantInput: 1200, wantOutput: 2, wantEstimated: true},
		{name: "missing input with reported output is estimated", start: `{"output_tokens":50}`, delta: `{"output_tokens":50}`, done: true, wantInput: 1200, wantOutput: 50, wantEstimated: true},
		{name: "zero delta does not erase counted input", start: `{"input_tokens":1000,"output_tokens":1}`, delta: `{"input_tokens":0,"output_tokens":50}`, text: true, done: true, wantInput: 1000, wantOutput: 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"}}
			info.SetEstimatePromptTokens(1200)
			state := &ClaudeResponseInfo{Usage: &dto.Usage{}}
			require.Nil(t, HandleStreamResponseData(c, info, state, fmt.Sprintf(`{"type":"message_start","message":{"id":"m","model":"claude-test","usage":%s}}`, tc.start)))
			if tc.text {
				require.Nil(t, HandleStreamResponseData(c, info, state, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`))
			}
			if tc.done {
				require.Nil(t, HandleStreamResponseData(c, info, state, fmt.Sprintf(`{"type":"message_delta","usage":%s}`, tc.delta)))
			}
			HandleStreamFinalResponse(c, info, state)
			assert.Equal(t, tc.wantInput, state.Usage.PromptTokens)
			assert.Equal(t, tc.wantOutput, state.Usage.CompletionTokens)
			assert.Equal(t, tc.wantEstimated, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
			require.NotNil(t, state.Usage.BillingUsage)
			require.NotNil(t, state.Usage.BillingUsage.ClaudeUsage)
			assert.Equal(t, tc.wantInput, state.Usage.BillingUsage.ClaudeUsage.InputTokens, "settlement input must match the finalized usage")
			assert.Equal(t, tc.wantOutput, state.Usage.BillingUsage.ClaudeUsage.OutputTokens)
			assert.Equal(t, tc.wantEstimated, state.Usage.BillingUsage.Estimated)
		})
	}
}
