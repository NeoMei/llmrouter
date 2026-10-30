package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeTokenDeltaObserved struct{ done chan error }

func (h nativeTokenDeltaObserved) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (h nativeTokenDeltaObserved) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "eval" && len(cmd.Args()) > 1 && cmd.Args()[1] == tokenQuotaDeltaScript {
		h.done <- cmd.Err()
	}
	return nil
}
func (h nativeTokenDeltaObserved) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (h nativeTokenDeltaObserved) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestNativeTokenPendingRefundRemainsSpendableBeforeBatchFlush(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
	enrollNativeQuotaFixture(t, token.Id)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 80, false)
	require.NoError(t, err)
	require.True(t, ok)
	batchUpdate()
	require.Equal(t, 20, getTokenFromDB(t, token.Id).RemainQuota)
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 80))
	require.Equal(t, 20, getTokenFromDB(t, token.Id).RemainQuota)
	ok, err = TryReserveTokenQuota(token.Id, token.Key, 60, false)
	require.NoError(t, err)
	require.True(t, ok, "a completed cache refund is available even before its batch persistence")
	ok, err = TryReserveTokenQuota(token.Id, token.Key, 60, false)
	require.NoError(t, err)
	assert.False(t, ok, "the live Redis balance still caps total admissions")
	batchUpdate()
	assert.Equal(t, 40, getTokenFromDB(t, token.Id).RemainQuota)
	assert.Equal(t, 60, getTokenFromDB(t, token.Id).UsedQuota)
}
