package moonshot

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
		Model: "kimi-k2.7-code",
		Input: json.RawMessage(`"hello"`),
		Tools: json.RawMessage(`[{"type":"function","name":"get_weather"}]`),
	}
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k2.7-code"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok, "expected chat completions request")
	require.Equal(t, "kimi-k2.7-code", chatReq.Model)
	require.NotEmpty(t, chatReq.Messages)
	require.True(t, info.ResponsesViaChat, "flag must be set for URL/response handling")
}

func TestGetRequestURLResponsesViaChatTargetsChatCompletions(t *testing.T) {
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl:    "https://api.kimi.com/coding",
			UpstreamModelName: "kimi-k2.7-code",
		},
	}
	info.ResponsesViaChat = true

	url, err := (&Adaptor{}).GetRequestURL(info)

	require.NoError(t, err)
	require.Equal(t, "https://api.kimi.com/coding/v1/chat/completions", url)
}
