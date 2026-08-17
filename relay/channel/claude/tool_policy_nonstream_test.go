package claude

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeHandlerRejectsInvalidToolCallBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-test","stop_reason":"tool_use",
		"content":[{"type":"text","text":"safe"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":[]}],
		"usage":{"input_tokens":1,"output_tokens":1}
	}`))}

	usage, apiErr := ClaudeHandler(c, resp, info)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
	assert.Zero(t, info.SendResponseCount)
}

func TestClaudeHandlerRejectsDuplicateToolInputBeforeMapCollapse(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"}}
	body := `{"id":"msg_1","type":"message","content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":1,"q":2}}]}`

	usage, apiErr := ClaudeHandler(c, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, info)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
}
