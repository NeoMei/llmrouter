package model

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func getTokenCacheKey(key string) string {
	return fmt.Sprintf("token:%s", common.GenerateHMAC(key))
}

func getTokenCacheFenceKey(key string) string {
	return fmt.Sprintf("token:fence:%s", common.GenerateHMAC(key))
}

func tokenCacheTTLSeconds() int {
	ttl := common.RedisKeyCacheSeconds()
	if ttl <= 0 {
		return 60
	}
	return ttl
}

// tokenCacheFenceSeconds must outlive a token mutation's database write plus
// any in-flight reader's DB-read-to-cache-init gap. The fence is not deleted
// after commit; it expires naturally so a reader holding a pre-mutation
// snapshot cannot publish it right after the mutation cleared the cache.
// While the fence exists readers simply serve the database without caching.
const tokenCacheFenceSeconds = 10

// invalidateTokenCacheForMutation is called before a token metadata mutation
// writes to the database: it raises the fence and drops the cached hash so no
// reader can act on (or re-publish) the pre-mutation state.
func invalidateTokenCacheForMutation(key string) error {
	if !common.RedisEnabled || key == "" {
		return nil
	}
	ctx := context.Background()
	err := common.RDB.Set(ctx, getTokenCacheFenceKey(key), 1, time.Duration(tokenCacheFenceSeconds)*time.Second).Err()
	if err != nil {
		return err
	}
	return common.RDB.Del(ctx, getTokenCacheKey(key)).Err()
}

// cacheInitToken publishes a database snapshot only when no mutation fence is
// active and the hash is cold. An existing hash only gets its TTL refreshed:
// its RemainQuota may already be ahead of this snapshot because atomic
// pre-consume decrements Redis first, so a snapshot must never overwrite any
// field of a live hash.
// 返回值：0=被 fence 拦截，1=完成初始化，2=哈希已存在，仅刷新 TTL。
func cacheInitToken(token Token) (int, error) {
	if !common.RedisEnabled {
		return 0, nil
	}
	// A blank-realm snapshot may predate enrollment. Resolve realm under the
	// current SQL row lock for every publisher; never trust the snapshot to
	// decide whether the native generation fence applies.
	var result int
	err := DB.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			// SQLite has no FOR UPDATE. Acquire its writer fence before
			// reading the generation and publishing a cache snapshot.
			if err := tx.Model(&Token{}).Where("id = ?", token.Id).
				UpdateColumn("quota_policy_generation", gorm.Expr("quota_policy_generation")).Error; err != nil {
				return err
			}
		}
		var current Token
		if err := lockForUpdate(tx).Select("id", "key", "auth_realm", "quota_policy_generation").First(&current, token.Id).Error; err != nil {
			return err
		}
		if current.AuthRealm != token.AuthRealm || current.Key != token.Key || current.QuotaPolicyGeneration != token.QuotaPolicyGeneration {
			return nil
		}
		if current.AuthRealm == NativeFundingLocalOnly {
			state, err := nativeQuotaState(tx, current)
			if err != nil {
				return err
			}
			token.RemainQuota, token.UsedQuota, token.QuotaStateRevision = state.RemainQuota, state.UsedQuota, state.Revision
		}
		// Hold the same row lock as scoped Update through publication: an
		// updater cannot invalidate the hash and then lose to this snapshot.
		var err error
		result, err = cachePublishToken(token)
		return err
	})
	return result, err
}

func cachePublishToken(token Token) (int, error) {
	allowIps := ""
	if token.AllowIps != nil {
		allowIps = *token.AllowIps
	}
	nativeFlag := 0
	if token.AuthRealm == NativeFundingLocalOnly {
		nativeFlag = 1
	}
	const script = `
if redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
if redis.call('EXISTS', KEYS[1]) == 1 and redis.call('HGET', KEYS[1], 'CacheSchema') ~= '2' then
  redis.call('DEL', KEYS[1])
end
if redis.call('EXISTS', KEYS[1]) == 1 then
  if ARGV[19] ~= '1' then
    redis.call('EXPIRE', KEYS[1], ARGV[16])
    return 2
  end
  local generation = tonumber(redis.call('HGET', KEYS[1], 'QuotaPolicyGeneration') or '-1')
  local revision = tonumber(redis.call('HGET', KEYS[1], 'QuotaStateRevision') or '-1')
  if generation > tonumber(ARGV[17]) or (generation == tonumber(ARGV[17]) and revision > tonumber(ARGV[18])) then
    return 0
  end
end
redis.call('HSET', KEYS[1],
  'Id', ARGV[1], 'UserId', ARGV[2], 'Status', ARGV[3], 'Name', ARGV[4],
  'CreatedTime', ARGV[5], 'AccessedTime', ARGV[6], 'ExpiredTime', ARGV[7],
  'UnlimitedQuota', ARGV[8], 'ModelLimitsEnabled', ARGV[9], 'ModelLimits', ARGV[10],
  'AllowIps', ARGV[11], 'Group', ARGV[12], 'CrossGroupRetry', ARGV[13],
  'RemainQuota', ARGV[14], 'UsedQuota', ARGV[15], 'QuotaPolicyGeneration', ARGV[17], 'QuotaStateRevision', ARGV[18], 'AuthRealm', ARGV[20], 'Purpose', ARGV[21], 'BoundOrigin', ARGV[22], 'FundingAccountID', ARGV[23], 'CacheSchema', '2')
redis.call('EXPIRE', KEYS[1], ARGV[16])
return 1`

	return common.RDB.Eval(context.Background(), script, []string{
		getTokenCacheKey(token.Key), getTokenCacheFenceKey(token.Key),
	},
		token.Id, token.UserId, token.Status, token.Name,
		token.CreatedTime, token.AccessedTime, token.ExpiredTime,
		strconv.FormatBool(token.UnlimitedQuota), strconv.FormatBool(token.ModelLimitsEnabled),
		token.ModelLimits, allowIps, token.Group, strconv.FormatBool(token.CrossGroupRetry),
		token.RemainQuota, token.UsedQuota,
		tokenCacheTTLSeconds(), token.QuotaPolicyGeneration, token.QuotaStateRevision, nativeFlag, token.AuthRealm, token.Purpose, token.BoundOrigin, token.FundingAccountID,
	).Int()
}

// cacheGetTokenByKey 从缓存读取 token；不完整的哈希（如仅有配额字段）会被拒绝。
func cacheGetTokenByKey(key string) (*Token, error) {
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var token Token
	if err := common.RedisHGetObj(getTokenCacheKey(key), &token); err != nil {
		return nil, err
	}
	if token.Id <= 0 || token.CacheSchema != 2 {
		return nil, fmt.Errorf("token cache is incomplete")
	}
	token.Key = key
	return &token, nil
}
