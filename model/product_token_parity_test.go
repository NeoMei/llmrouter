package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func TestProductWalletAndSubscriptionShareOneOriginalKeyLimit(t *testing.T) {
	s, db, _ := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Update("remain_quota", 100).Error)
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	mr := miniredis.RunT(t)
	cache := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = cache.Close() })
	oldDB, oldRedis, oldRDB := DB, common.RedisEnabled, common.RDB
	DB, common.RedisEnabled, common.RDB = db, true, cache
	t.Cleanup(func() { DB, common.RedisEnabled, common.RDB = oldDB, oldRedis, oldRDB })
	require.NoError(t, cache.HSet(ctx, getTokenCacheKey("original-key"), map[string]any{"Id": 7, "RemainQuota": 100, "UsedQuota": 0}).Err())
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "wallet", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: "{}", Quota: 80})
	require.NoError(t, err)
	reserved, err := TryReserveTokenQuota(7, "original-key", 50, false)
	require.NoError(t, err)
	require.False(t, reserved)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 20, token.RemainQuota)
}
