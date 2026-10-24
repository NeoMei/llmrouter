package claude

import (
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClaudeNativeChatStreamSkipsEmptyBlockStop(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI}
	state := &ClaudeResponseInfo{Usage: &dto.Usage{}}
	for _, event := range []string{`{"type":"message_start","message":{"id":"msg_test","model":"claude-test"}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"answer"}}`, `{"type":"content_block_stop","index":0}`} {
		require.Nil(t, HandleStreamResponseData(c, info, state, event))
	}
	assert.NotContains(t, recorder.Body.String(), "data: null")
	assert.Equal(t, 2, strings.Count(recorder.Body.String(), "data: "))
}

func TestClaudeNativeChatStreamCarriesSignedThinking(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI}
	state := &ClaudeResponseInfo{Usage: &dto.Usage{}}
	events := []string{
		`{"type":"message_start","message":{"id":"msg_test","model":"claude-test"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thought"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"YW50aHJvcGljLXNpZ25hdHVyZS1leGFtcGxl"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	for _, event := range events {
		require.Nil(t, HandleStreamResponseData(c, info, state, event))
	}
	var details string
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if value := gjson.Get(strings.TrimPrefix(line, "data: "), "choices.0.delta.reasoning_details"); value.Exists() {
			details = value.Raw
		}
	}
	assert.JSONEq(t, `[{"type":"reasoning.text","text":"thought","signature":"YW50aHJvcGljLXNpZ25hdHVyZS1leGFtcGxl","format":"anthropic-claude-v1","index":0}]`, details)
	assert.NotContains(t, recorder.Body.String(), "data: null")
}
