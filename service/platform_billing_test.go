package service

import (
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
	"testing"
)

// Platform traffic must reject every legacy quota entry even without a session.
func TestPlatformRejectsLegacyFundingFallback(t *testing.T) {
	info := &relaycommon.RelayInfo{PlatformMode: true, UserId: 123, TokenId: 456}
	require.Error(t, PreConsumeTokenQuota(info, 10))
	require.Error(t, PostConsumeQuota(info, 10, 0, false))
	require.Error(t, SettleBilling(nil, info, 10))
	require.NotNil(t, PreConsumeBilling(nil, 10, info))
	_, err := NewBillingSession(nil, info, 10)
	require.NotNil(t, err)
	// Must return before local usage counters/logs too; no configured global DB.
	PostTextConsumeQuota(nil, info, &dto.Usage{PromptTokens: 10, CompletionTokens: 5}, nil)
	PostAudioConsumeQuota(nil, info, &dto.Usage{PromptTokens: 10, CompletionTokens: 5}, "")
	LogTaskConsumption(nil, info)
}
