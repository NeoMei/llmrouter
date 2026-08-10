package service

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestPreConsumeUsesResolvedBillingModel(t *testing.T) {
	info := relaycommon.RelayInfo{OriginModelName: "gpt-5.6-sol", ResolvedModelName: "deepseek-v4-flash", BillingModelName: "deepseek-v4-flash"}
	got := billingModelFor(&info)
	require.Equal(t, "deepseek-v4-flash", got)
}

func TestBillingModelForFallsBackToResolved(t *testing.T) {
	info := relaycommon.RelayInfo{OriginModelName: "alias", ResolvedModelName: "upstream-model"}
	require.Equal(t, "upstream-model", billingModelFor(&info))
}

func TestBillingModelForFallsBackToOrigin(t *testing.T) {
	info := relaycommon.RelayInfo{OriginModelName: "origin-model"}
	require.Equal(t, "origin-model", billingModelFor(&info))
}

func TestBillingModelForNil(t *testing.T) {
	require.Equal(t, "", billingModelFor(nil))
}
