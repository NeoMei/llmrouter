package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useDistRateLimitMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	require.NoError(t, redisClient.Ping(context.Background()).Err())
	t.Cleanup(func() { _ = redisClient.Close() })
	return redisServer, redisClient
}

func TestCheckBucketAllowsUntilCapacityThenDenies(t *testing.T) {
	_, rdb := useDistRateLimitMiniRedis(t)
	ctx := context.Background()

	// capacity 3, no refill — deterministic exhaustion
	for i := range 3 {
		assert.True(t, checkBucket(ctx, rdb, "test:cap3", 3, 0, 1000.0),
			"request %d should be allowed", i+1)
	}
	assert.False(t, checkBucket(ctx, rdb, "test:cap3", 3, 0, 1000.0),
		"4th request must be denied")
}

func TestCheckBucketRefillsOverTime(t *testing.T) {
	_, rdb := useDistRateLimitMiniRedis(t)
	ctx := context.Background()
	const refillRate = 1.0 // 1 token / sec

	// Exhaust capacity-2 bucket
	require.True(t, checkBucket(ctx, rdb, "test:refill", 2, refillRate, 1000.0))
	require.True(t, checkBucket(ctx, rdb, "test:refill", 2, refillRate, 1000.0))
	assert.False(t, checkBucket(ctx, rdb, "test:refill", 2, refillRate, 1000.0))

	// 0.5s later — only 0.5 tokens, still denied
	assert.False(t, checkBucket(ctx, rdb, "test:refill", 2, refillRate, 1000.5))

	// 1.5s later — 1.5 tokens refilled, one allowed then empty
	assert.True(t, checkBucket(ctx, rdb, "test:refill", 2, refillRate, 1001.5))
	assert.False(t, checkBucket(ctx, rdb, "test:refill", 2, refillRate, 1001.5))
}

func TestCheckBucketCapsAtCapacity(t *testing.T) {
	_, rdb := useDistRateLimitMiniRedis(t)
	ctx := context.Background()
	const capacity = 5

	// Consume one, leaving 4 tokens at t=2000
	require.True(t, checkBucket(ctx, rdb, "test:cap5", capacity, 100.0, 2000.0))

	// 1000s later, refill would add 100000 tokens — cap must clamp to 5.
	// Exactly 5 more allowed, then denied.
	for i := range capacity {
		assert.True(t, checkBucket(ctx, rdb, "test:cap5", capacity, 100.0, 3000.0),
			"request %d after refill should be allowed", i+1)
	}
	assert.False(t, checkBucket(ctx, rdb, "test:cap5", capacity, 100.0, 3000.0),
		"bucket must not exceed capacity on refill")
}

func TestCheckBucketAtomicUnderConcurrency(t *testing.T) {
	_, rdb := useDistRateLimitMiniRedis(t)
	ctx := context.Background()
	const capacity, goroutines = 10, 100

	var allowed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if checkBucket(ctx, rdb, "test:concurrent", capacity, 0, 5000.0) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(capacity), allowed.Load(),
		"exactly capacity=%d requests must pass under concurrency", capacity)
}

func TestRateLimitMiddlewareFailsOpenOnNilRedis(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RateLimitMiddleware(nil, DefaultRateLimitConfig()))
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRateLimitMiddlewareDeniesAtUserLayer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, rdb := useDistRateLimitMiniRedis(t)

	cfg := RateLimitConfig{
		GlobalCapacity: 1000, GlobalRefillRate: 100,
		UserCapacity: 1, UserRefillRate: 0, // tight: 1 request, no refill
		TokenCapacity: 1000, TokenRefillRate: 100,
		ProviderCapacity: 1000, ProviderRefillRate: 100,
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("token_id", 7)
	}, RateLimitMiddleware(rdb, cfg))
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "user rate limit")
}

func TestRateLimitMiddlewareProviderLayerOnlyForPlatformKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, rdb := useDistRateLimitMiniRedis(t)

	cfg := RateLimitConfig{
		GlobalCapacity: 1000, GlobalRefillRate: 100,
		UserCapacity: 1000, UserRefillRate: 100,
		TokenCapacity: 1000, TokenRefillRate: 100,
		ProviderCapacity: 1, ProviderRefillRate: 0, // tight: 1 platform request
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 1)
		c.Set("token_id", 1)
		if c.Request.URL.Path == "/platform" {
			c.Set("key_source", "platform")
			c.Set("profile_id", 5)
		} else {
			c.Set("key_source", "byok")
		}
	}, RateLimitMiddleware(rdb, cfg))
	router.GET("/byok", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/platform", func(c *gin.Context) { c.Status(http.StatusOK) })

	// BYOK: provider layer skipped, two requests pass
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/byok", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/byok", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "BYOK must not hit provider limit")

	// Platform: first passes, second hits provider capacity=1
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/platform", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/platform", nil))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "provider busy")
}
