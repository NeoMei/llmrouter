package relay

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEmbeddingCapabilityDoesNotAdvertiseUnsupportedConverters(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeZhipu, constant.ChannelTypePerplexity, constant.ChannelTypeMistral, constant.ChannelTypeDeepSeek, constant.ChannelTypeBaiduV2, constant.ChannelTypeXai, constant.ChannelTypeSubmodel, constant.ChannelTypeCodex, constant.ChannelTypeVertexAi, constant.ChannelTypeCohere} {
		t.Run(constant.ChannelTypeNames[channelType], func(t *testing.T) {
			apiType, _ := common.ChannelType2APIType(channelType)
			adaptor := GetAdaptor(apiType)
			require.NotNil(t, adaptor)
			_, err := adaptor.ConvertEmbeddingRequest(nil, nil, dto.EmbeddingRequest{Input: "test"})
			require.ErrorIs(t, err, channel.ErrUnsupportedOperation)
			assert.False(t, capability.DefaultRegistry().Supports(channelType, capability.ProtocolOpenAI, capability.OperationEmbeddingCreate))
		})
	}
}
