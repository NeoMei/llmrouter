package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesBillCompletedHostedCallsNotDeclarations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	policy, err := toolpolicy.BuildFromResponses(json.RawMessage(`[{"type":"web_search"}]`), nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-flash"},
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
			dto.BuildInToolWebSearch: {ToolName: dto.BuildInToolWebSearch},
		}},
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"id":"resp_1","status":"completed","tools":[{"type":"web_search"}],
		"output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"x"}},{"type":"message","content":[]}],
		"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearch].CallCount)
}

func TestResponsesVendorToolUsageReplacesProtocolCounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	policy, err := toolpolicy.BuildFromResponses(json.RawMessage(`[{"type":"web_search"}]`), nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-flash"},
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{}},
	}
	info.VendorToolUsage = func(raw []byte) map[string]int {
		var payload struct {
			Usage struct {
				ServerToolUse struct {
					ToolSearch int `json:"tool_search"`
					ToolFetch  int `json:"tool_fetch"`
				} `json:"server_tool_use"`
			} `json:"usage"`
		}
		require.NoError(t, json.Unmarshal(raw, &payload))
		return map[string]int{"deepseek_search": payload.Usage.ServerToolUse.ToolSearch, "deepseek_fetch": payload.Usage.ServerToolUse.ToolFetch}
	}
	body := `{
		"id":"resp_1","status":"completed","tools":[{"type":"web_search"}],
		"output":[
			{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search"}},
			{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page"}}
		],
		"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"server_tool_use":{"tool_search":2,"tool_fetch":0}}}`
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools["deepseek_search"].CallCount)
	require.Zero(t, info.ResponsesUsageInfo.BuiltInTools["deepseek_fetch"].CallCount)
	assert.Equal(t, 1, usage.PromptTokens)
	assert.Equal(t, 1, usage.CompletionTokens)
}

func TestResponsesFailedBodyStillReturnsBillableUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-flash"}}
	body := `{"status":"failed","error":{"type":"server_error","message":"generation failed"},"usage":{"input_tokens":17,"output_tokens":3,"total_tokens":20}}`
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := OaiResponsesHandler(c, info, resp)
	require.NotNil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 17, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
}

func TestResponsesStreamFailedTerminalKeepsUsage(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-flash"}}
	body := `data: {"type":"response.failed","response":{"status":"failed","usage":{"input_tokens":11,"output_tokens":2,"total_tokens":13}}}}` + "\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

	var captured *dto.Usage
	captureResponsesFailedTerminalUsage(`{"type":"response.failed","response":{"status":"failed","usage":{"input_tokens":11,"output_tokens":2,"total_tokens":13}}}`, info, &captured)
	require.NotNil(t, captured)
	assert.Equal(t, 11, captured.PromptTokens)

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.NotNil(t, apiErr)
	// The provider failure stays a failed client outcome. Usage capture is a
	// separate concern and is verified directly above so it remains available
	// for the upstream handler without turning failure into success.
}
