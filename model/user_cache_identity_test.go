package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestUserCacheIdentityMetadataRoundTrip(t *testing.T) {
	server := useUserCacheMiniRedis(t)
	user := &UserBase{Id: 4301, AuthVersion: 1, Status: common.UserStatusEnabled,
		Role: common.RoleCommonUser, Quota: 0, IdentitySource: UniHubIdentitySource, UniHubUserID: "central-subject"}
	require.NoError(t, writeUserCache(user, true))
	cached, err := cacheGetUserBase(user.Id)
	require.NoError(t, err)
	require.Equal(t, user.IdentitySource, cached.IdentitySource)
	require.Equal(t, user.UniHubUserID, cached.UniHubUserID)
	require.Equal(t, user.Role, cached.Role)
	require.Zero(t, cached.Quota)
	require.Equal(t, time.Duration(userCacheTTLSeconds())*time.Second, server.TTL(getUserCacheKey(user.Id)))

	// An authoritative native snapshot must also clear any previous metadata,
	// without replacing the quota maintained by separate atomic delta writes.
	require.NoError(t, common.RDB.HSet(t.Context(), getUserCacheKey(user.Id), "Quota", 42).Err())
	user.IdentitySource, user.UniHubUserID, user.Quota = "", "", 99
	require.NoError(t, writeUserCache(user, false))
	cached, err = cacheGetUserBase(user.Id)
	require.NoError(t, err)
	require.Empty(t, cached.IdentitySource)
	require.Empty(t, cached.UniHubUserID)
	require.Equal(t, 42, cached.Quota)
}

func TestUserCacheSchemaTwoRehydratesIdentityMetadata(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := User{Username: "cache-identity-migration", AuthVersion: 1,
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
		IdentitySource: UniHubIdentitySource, UniHubUserID: "central-subject"}
	require.NoError(t, DB.Create(&user).Error)
	// Schema 2 hashes were marked complete even though their writer omitted
	// both identity fields. They must miss and hydrate from the owning DB.
	require.NoError(t, common.RDB.HSet(t.Context(), getUserCacheKey(user.Id), map[string]any{
		"Id": strconv.Itoa(user.Id), "AuthVersion": "1", "Status": "1", "Role": "1", "Quota": "0", "CacheSchema": "2",
	}).Err())
	_, err := cacheGetUserBase(user.Id)
	require.Error(t, err)
	cached, err := GetUserCache(user.Id)
	require.NoError(t, err)
	require.Equal(t, UniHubIdentitySource, cached.IdentitySource)
	require.Equal(t, user.UniHubUserID, cached.UniHubUserID)
	cached, err = cacheGetUserBase(user.Id)
	require.NoError(t, err)
	require.Equal(t, UniHubIdentitySource, cached.IdentitySource)
	require.Equal(t, user.UniHubUserID, cached.UniHubUserID)
	require.Equal(t, userCacheSchemaVersion, cached.CacheSchema)
}
