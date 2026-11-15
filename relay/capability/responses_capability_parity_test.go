package capability_test

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/baidu_v2"
	"github.com/QuantumNous/new-api/relay/channel/minimax"
	"github.com/QuantumNous/new-api/relay/channel/mistral"
	"github.com/QuantumNous/new-api/relay/channel/ollama"
	"github.com/QuantumNous/new-api/relay/channel/siliconflow"
	"github.com/QuantumNous/new-api/relay/channel/submodel"
	"github.com/QuantumNous/new-api/relay/channel/zhipu"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/responsesprobe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openAICompatibleChannels mirrors the 32-channel slice in
// relay/capability/defaults.go. It is kept here so the test fails loudly if a
// channel is added to the declaration without revisiting its responses parity.
var openAICompatibleChannels = []int{
	constant.ChannelTypeOpenAI,
	constant.ChannelTypeAzure,
	constant.ChannelTypeOllama,
	constant.ChannelTypeOpenAIMax,
	constant.ChannelTypeOhMyGPT,
	constant.ChannelTypeCustom,
	constant.ChannelTypeAILS,
	constant.ChannelTypeAIProxy,
	constant.ChannelTypeAPI2GPT,
	constant.ChannelTypeAIGC2D,
	constant.ChannelTypeZhipu,
	constant.ChannelTypeAli,
	constant.ChannelType360,
	constant.ChannelTypeOpenRouter,
	constant.ChannelTypeAIProxyLibrary,
	constant.ChannelTypeFastGPT,
	constant.ChannelTypeMoonshot,
	constant.ChannelTypeZhipu_v4,
	constant.ChannelTypePerplexity,
	constant.ChannelTypeLingYiWanWu,
	constant.ChannelTypeMiniMax,
	constant.ChannelTypeSiliconFlow,
	constant.ChannelTypeMistral,
	constant.ChannelTypeDeepSeek,
	constant.ChannelTypeVolcEngine,
	constant.ChannelTypeBaiduV2,
	constant.ChannelTypeXinference,
	constant.ChannelTypeXai,
	constant.ChannelTypeSubmodel,
	constant.ChannelTypeCodex,
	constant.ChannelTypeAdvancedCustom,
	constant.ChannelTypeGateway,
}

// responsesCreateRejectingAdaptors binds the seven channels whose adaptors
// return ErrUnsupportedOperation from ConvertOpenAIResponsesRequest to their
// concrete implementations. AIProxyLibrary maps to APITypeAIProxyLibrary, for
// which relay.GetAdaptor has no case; it is covered separately below.
var responsesCreateRejectingAdaptors = []struct {
	channelType int
	adaptor     channel.Adaptor
}{
	{constant.ChannelTypeOllama, &ollama.Adaptor{}},
	{constant.ChannelTypeZhipu, &zhipu.Adaptor{}},
	{constant.ChannelTypeMiniMax, &minimax.Adaptor{}},
	{constant.ChannelTypeSiliconFlow, &siliconflow.Adaptor{}},
	{constant.ChannelTypeMistral, &mistral.Adaptor{}},
	{constant.ChannelTypeBaiduV2, &baidu_v2.Adaptor{}},
	{constant.ChannelTypeSubmodel, &submodel.Adaptor{}},
}

var responsesCompactSupported = []int{
	constant.ChannelTypeOpenAI,
	constant.ChannelTypeAzure,
	constant.ChannelTypeOpenAIMax,
	constant.ChannelTypeOhMyGPT,
	constant.ChannelTypeCustom,
	constant.ChannelTypeAILS,
	constant.ChannelTypeAIProxy,
	constant.ChannelTypeAPI2GPT,
	constant.ChannelTypeAIGC2D,
	constant.ChannelType360,
	constant.ChannelTypeFastGPT,
	constant.ChannelTypeLingYiWanWu,
	constant.ChannelTypeCodex,
	constant.ChannelTypeDeepSeek,
	constant.ChannelTypeMoonshot,
	constant.ChannelTypeZhipu_v4,
}

func TestResponsesCreateRegistryMatchesAdaptorConversion(t *testing.T) {
	registry := capability.DefaultRegistry()
	for _, tc := range responsesCreateRejectingAdaptors {
		t.Run(constant.ChannelTypeNames[tc.channelType]+" converter rejects", func(t *testing.T) {
			_, err := tc.adaptor.ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{
				Model: "parity-model",
				Input: []byte(`"hello"`),
			})
			require.ErrorIs(t, err, channel.ErrUnsupportedOperation)
			assert.False(t, registry.Supports(tc.channelType, capability.ProtocolOpenAI, capability.OperationResponseCreate),
				"registry must not advertise responses.create for a rejecting adaptor")
		})
	}

	// AIProxyLibrary has no GetAdaptor case at all: ChannelType2APIType maps it
	// to APITypeAIProxyLibrary, and relay/relay_adaptor.go leaves that case
	// commented out.
	t.Run("AIProxyLibrary has no dispatch case", func(t *testing.T) {
		apiType, ok := common.ChannelType2APIType(constant.ChannelTypeAIProxyLibrary)
		require.True(t, ok)
		require.Equal(t, constant.APITypeAIProxyLibrary, apiType)
		assert.False(t, registry.Supports(constant.ChannelTypeAIProxyLibrary, capability.ProtocolOpenAI, capability.OperationResponseCreate))
	})

	// Every other OpenAI-compatible channel keeps responses.create. This also
	// pins the required Codex, DeepSeek, Moonshot, OpenAI, Azure and Ali cases.
	excluded := map[int]struct{}{constant.ChannelTypeAIProxyLibrary: {}}
	for _, tc := range responsesCreateRejectingAdaptors {
		excluded[tc.channelType] = struct{}{}
	}
	for _, channelType := range openAICompatibleChannels {
		if _, skip := excluded[channelType]; skip {
			continue
		}
		t.Run(constant.ChannelTypeNames[channelType]+" keeps responses.create", func(t *testing.T) {
			assert.True(t, registry.Supports(channelType, capability.ProtocolOpenAI, capability.OperationResponseCreate))
		})
	}

	require.Len(t, excluded, 8, "exactly eight channels must lose responses.create")
	require.Len(t, openAICompatibleChannels, 32)
	createCount := 0
	for _, channelType := range openAICompatibleChannels {
		if registry.Supports(channelType, capability.ProtocolOpenAI, capability.OperationResponseCreate) {
			createCount++
		}
	}
	assert.Equal(t, 24, createCount, "32 base channels minus 8 rejecting adaptors")
}

func channelMeta(channelType int) *relaycommon.ChannelMeta {
	apiType, _ := common.ChannelType2APIType(channelType)
	return &relaycommon.ChannelMeta{ChannelType: channelType, ApiType: apiType}
}

// compactEndpointReachable models the two gates in relay/responses_handler.go
// and relay/responsesprobe/probe.go: ApiType must be OpenAI/Codex, or
// DecideResponsesViaChat must set ResponsesViaChat for this channel and mode.
func compactEndpointReachable(channelType int) bool {
	meta := channelMeta(channelType)
	if meta.ApiType == constant.APITypeOpenAI || meta.ApiType == constant.APITypeCodex {
		return true
	}
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponsesCompact}
	info.ChannelMeta = meta
	responsesprobe.DecideResponsesViaChat(context.Background(), info)
	return info.ResponsesViaChat
}

func TestResponsesCompactRegistryMatchesDispatch(t *testing.T) {
	registry := capability.DefaultRegistry()
	allowed := map[int]struct{}{}
	for _, channelType := range responsesCompactSupported {
		allowed[channelType] = struct{}{}
	}
	require.Len(t, responsesCompactSupported, 16)

	for _, channelType := range openAICompatibleChannels {
		_, declared := allowed[channelType]
		reachable := compactEndpointReachable(channelType)
		t.Run(constant.ChannelTypeNames[channelType], func(t *testing.T) {
			supported := registry.Supports(channelType, capability.ProtocolOpenAI, capability.OperationResponseCompact)
			assert.Equal(t, declared, supported, "registry declaration must match the compact dispatch set")
			assert.Equal(t, declared, reachable, "declared set must match the handler/probe reachability")
		})
	}

	compactCount := 0
	for _, channelType := range openAICompatibleChannels {
		if registry.Supports(channelType, capability.ProtocolOpenAI, capability.OperationResponseCompact) {
			compactCount++
		}
	}
	assert.Equal(t, 16, compactCount)
}
