package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryParamChannelPredicateFiltersUnsupportedTypes(t *testing.T) {
	param := &RetryParam{
		Protocol:  capability.ProtocolOpenAI,
		Operation: capability.OperationAudioSpeech,
	}
	predicate := param.channelPredicate()
	require.NotNil(t, predicate)
	assert.True(t, predicate(&model.Channel{Type: constant.ChannelTypeOpenAI}))
	assert.False(t, predicate(&model.Channel{Type: constant.ChannelTypeJina}))
}

func TestRetryParamWithoutOperationPreservesLegacySelection(t *testing.T) {
	assert.Nil(t, (&RetryParam{}).channelPredicate())
}
