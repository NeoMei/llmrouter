package service

import (
	"context"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativePlatformEvidenceSurvivesDisconnectWithoutLocalDatabase(t *testing.T) {
	old := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	info := &relaycommon.RelayInfo{PlatformMode: true, NativeBilling: &relaycommon.NativeBillingContext{Platform: true, Observe: func(ctx context.Context, u metering.Usage) error {
		called = true
		require.NoError(t, ctx.Err())
		assert.Equal(t, "missing", u.State)
		return nil
	}}}
	require.NoError(t, RecordNativeUsageForRelay(ctx, info, metering.Usage{State: "missing"}))
	assert.True(t, called)
	info.NativeBilling.Observe = nil
	require.Error(t, RecordNativeUsageForRelay(ctx, info, metering.Usage{State: "missing"}))
	info.NativeBilling = nil
	require.Error(t, RecordNativeUsageForRelay(ctx, info, metering.Usage{State: "missing"}))
}
