package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

// ponytail: fail-open on Redis unavailability — a limiter outage must not take down the API.
// The Lua script uses client-provided `now` (float, nanosecond precision) instead of
// redis.call('TIME') so tests are deterministic and sub-second refill rates work.

var rateLimitTokenBucketScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local bucket = redis.call("HMGET", key, "tokens", "last_refill")
local tokens = tonumber(bucket[1]) or capacity
local last_refill = tonumber(bucket[2]) or now

local delta = math.max(0, now - last_refill) * refill_rate
tokens = math.min(capacity, tokens + delta)

if tokens < 1 then
	return 0
end

tokens = tokens - 1
redis.call("HMSET", key, "tokens", tokens, "last_refill", now)
redis.call("EXPIRE", key, 300)
return 1
`

// RateLimitConfig holds token-bucket parameters for each rate-limit layer.
type RateLimitConfig struct {
	GlobalCapacity     float64
	GlobalRefillRate   float64
	UserCapacity       float64
	UserRefillRate     float64
	TokenCapacity      float64
	TokenRefillRate    float64
	ProviderCapacity   float64
	ProviderRefillRate float64
}

// DefaultRateLimitConfig returns conservative defaults. Capacities allow a
// short burst; refill rates sustain steady-state throughput (tokens/sec).
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		GlobalCapacity:     5000,
		GlobalRefillRate:   500,
		UserCapacity:       100,
		UserRefillRate:     10,
		TokenCapacity:      60,
		TokenRefillRate:    5,
		ProviderCapacity:   200,
		ProviderRefillRate: 20,
	}
}

// DistributedRateLimit applies the repository-wide Redis-backed defaults.
// When Redis is disabled, RateLimitMiddleware deliberately fails open.
func DistributedRateLimit() gin.HandlerFunc {
	return RateLimitMiddleware(common.RDB, DefaultRateLimitConfig())
}

// RateLimitMiddleware applies a 4-layer distributed token-bucket rate limiter:
// global → per-user → per-token → per-provider (platform keys only).
// Fails open (allows traffic) when Redis is nil or returns an error.
//
// Context keys: "id" (user), "token_id", "key_source" ("platform" activates
// layer ④), "profile_id" (provider identity).
func RateLimitMiddleware(rdb *redis.Client, cfg RateLimitConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rdb == nil {
			c.Next()
			return
		}

		ctx := c.Request.Context()
		now := float64(time.Now().UnixNano()) / 1e9

		// ① Global — protects overall system capacity
		if !checkBucket(ctx, rdb, "ratelimit:global", cfg.GlobalCapacity, cfg.GlobalRefillRate, now) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "system busy"})
			return
		}

		// ② Per-user — prevents single-user abuse
		if !checkBucket(ctx, rdb, "ratelimit:user:"+strconv.Itoa(c.GetInt("id")), cfg.UserCapacity, cfg.UserRefillRate, now) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "user rate limit"})
			return
		}

		// ③ Per-token — individual token limits
		if !checkBucket(ctx, rdb, "ratelimit:token:"+strconv.Itoa(c.GetInt("token_id")), cfg.TokenCapacity, cfg.TokenRefillRate, now) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "token rate limit"})
			return
		}

		// ④ Per-provider — protects platform keys from upstream rate limits
		if c.GetString("key_source") == "platform" {
			key := "ratelimit:provider:" + strconv.Itoa(c.GetInt("profile_id"))
			if !checkBucket(ctx, rdb, key, cfg.ProviderCapacity, cfg.ProviderRefillRate, now) {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "provider busy, try again"})
				return
			}
		}

		c.Next()
	}
}

// checkBucket atomically consumes one token from the bucket via a Lua script.
// Returns true if allowed, false if empty. Fails open (true) on Redis error.
func checkBucket(ctx context.Context, rdb *redis.Client, key string, capacity, refillRate, now float64) bool {
	result, err := rdb.Eval(ctx, rateLimitTokenBucketScript, []string{key}, capacity, refillRate, now).Int()
	if err != nil {
		return true
	}
	return result == 1
}
