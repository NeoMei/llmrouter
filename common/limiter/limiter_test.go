package limiter

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowPreservesBucketAfterScriptFlush(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	sha, err := client.ScriptLoad(ctx, rateLimitScript).Result()
	require.NoError(t, err)
	limiter := &RedisLimiter{client: client, limitScriptSHA: sha}
	options := []Option{WithCapacity(2), WithRate(0), WithRequested(1)}
	allowed, err := limiter.Allow(ctx, "retained-bucket", options...)
	require.NoError(t, err)
	assert.True(t, allowed)
	require.NoError(t, client.ScriptFlush(ctx).Err())

	allowed, err = limiter.Allow(ctx, "retained-bucket", options...)
	require.NoError(t, err)
	assert.True(t, allowed)
	allowed, err = limiter.Allow(ctx, "retained-bucket", options...)
	require.NoError(t, err)
	assert.False(t, allowed, "reloading the script must not refill the existing bucket")
}

func TestAllowReportsRedisFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	require.NoError(t, client.Close())
	limiter := &RedisLimiter{client: client, limitScriptSHA: "unavailable"}
	allowed, err := limiter.Allow(context.Background(), "unavailable")
	require.Error(t, err)
	assert.False(t, allowed)
}
