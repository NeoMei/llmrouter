package gemini

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

func TestGeminiChatHandlerRejectsInvalidToolCallBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/model-a:generateContent", nil)
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatGemini, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"candidates":[
			{"index":0,"content":{"role":"model","parts":[{"text":"safe"}]}},
			{"index":1,"content":{"role":"model","parts":[{"functionCall":{"name":"delete_all","args":{}}}]}}
		],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
	}`))}

	usage, apiErr := GeminiChatHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
	assert.Zero(t, info.SendResponseCount)
}
