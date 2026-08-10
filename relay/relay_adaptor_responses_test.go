package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatOnlyProviderAdaptorsConvertResponsesRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for name, apiType := range map[string]int{
		"moonshot": constant.APITypeMoonshot,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat: types.RelayFormatOpenAIResponses,
				RelayMode:   relayconstant.RelayModeResponses,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: name + "-model",
				},
			}

			converted, err := GetAdaptor(apiType).ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
				Model: name + "-model",
				Input: json.RawMessage(`"hello"`),
			})

			require.NoError(t, err)
			chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
			require.True(t, ok, "expected chat request, got %T", converted)
			require.Len(t, chatRequest.Messages, 1)
			assert.Equal(t, "user", chatRequest.Messages[0].Role)
			assert.Equal(t, "hello", chatRequest.Messages[0].StringContent())
			assert.True(t, c.GetBool("gateway_responses_via_chat"))
		})
	}
}

func TestDeepSeekAdaptorKeepsResponsesRequestNative(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		RelayMode:   relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
	}

	converted, err := GetAdaptor(constant.APITypeDeepSeek).ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
		Model: "deepseek-v4-flash",
		Input: json.RawMessage(`"hello"`),
	})

	require.NoError(t, err)
	responsesRequest, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok, "expected native Responses request, got %T", converted)
	assert.Equal(t, "deepseek-v4-flash", responsesRequest.Model)
	assert.False(t, c.GetBool("gateway_responses_via_chat"))
}
