package openai

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func responsesCompatInfo(viaChat bool) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ChannelBaseUrl:    "https://upstream.example.com",
			UpstreamModelName: "test-model",
			ApiKey:            "k",
			ChannelId:         42,
		},
	}
	info.ResponsesViaChat = viaChat
	return info
}

func TestConvertOpenAIResponsesRequestPassthroughWhenNative(t *testing.T) {
	request := dto.OpenAIResponsesRequest{Model: "test-model", Input: json.RawMessage(`"hi"`)}
	info := responsesCompatInfo(false)

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	same, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok, "native upstream must receive the responses request unchanged")
	require.Equal(t, "test-model", same.Model)
}

func TestConvertOpenAIResponsesRequestConvertsWhenViaChat(t *testing.T) {
	request := dto.OpenAIResponsesRequest{Model: "test-model", Input: json.RawMessage(`"hi"`)}
	info := responsesCompatInfo(true)

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok, "chat-only upstream must receive a chat completions request")
	require.Equal(t, "test-model", chatReq.Model)
}

func TestGetRequestURLResponsesViaChatTargetsChatCompletions(t *testing.T) {
	info := responsesCompatInfo(true)

	url, err := (&Adaptor{}).GetRequestURL(info)

	require.NoError(t, err)
	require.Equal(t, "https://upstream.example.com/v1/chat/completions", url)
}

func TestGetRequestURLResponsesNativePassthroughURL(t *testing.T) {
	info := responsesCompatInfo(false)
	info.RequestURLPath = "/v1/responses"

	url, err := (&Adaptor{}).GetRequestURL(info)

	require.NoError(t, err)
	require.Equal(t, "https://upstream.example.com/v1/responses", url)
}

func TestConvertOpenAIResponsesRequestEffortSuffixStillParsed(t *testing.T) {
	request := dto.OpenAIResponsesRequest{Model: "test-model-high", Input: json.RawMessage(`"hi"`)}
	info := responsesCompatInfo(true)

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, "test-model", chatReq.Model)
	require.Equal(t, "high", chatReq.ReasoningEffort)
	require.Equal(t, "high", info.ReasoningEffort)
}
