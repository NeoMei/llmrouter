package zhipu_4v

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIResponsesRequestAlwaysConvertsToChat(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hello"`),
	}
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "glm-5.2"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok, "expected chat completions request")
	require.Equal(t, "glm-5.2", chatReq.Model)
	require.True(t, info.ResponsesViaChat, "flag must be set for URL/response handling")
}

func TestGetRequestURLResponsesViaChatTargetsChatCompletions(t *testing.T) {
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl:    "https://open.bigmodel.cn",
			UpstreamModelName: "glm-5.2",
		},
	}
	info.ResponsesViaChat = true

	url, err := (&Adaptor{}).GetRequestURL(info)

	require.NoError(t, err)
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4/chat/completions", url)
}
