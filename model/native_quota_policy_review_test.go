package model

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
	"time"
)

// A policy edit must fence a delayed reader even after the Redis fence expires.
func TestNativeLateSnapshotCannotRestorePreMutationQuota(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	owner := createReserveTestUser(t, 1000)
	token := createReserveTestToken(t, 1000)
	require.NoError(t, DB.Model(&token).Updates(map[string]any{"user_id": owner.Id, "auth_realm": NativeFundingLocalOnly, "unlimited_quota": true}).Error)
	require.NoError(t, DB.First(&token, token.Id).Error)
	enrollNativeQuotaFixture(t, token.Id)
	read, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("native_late_cache_snapshot", func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" && paused.CompareAndSwap(false, true) {
			close(read)
			<-release
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Query().Remove("native_late_cache_snapshot") })
	done := make(chan error, 1)
	go func() { _, err := GetTokenByKey(token.Key, false); done <- err }()
	<-read
	token.UnlimitedQuota = false
	token.RemainQuota = 100
	updateErr := token.Update(NativeFundingLocalOnly)
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	close(release)
	require.NoError(t, updateErr)
	require.NoError(t, <-done)
	// Two independent callers use current SQL policy, but the delayed original
	// SQL read has now hydrated an old cache balance through the real reader.
	type result struct {
		ok  bool
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { ok, err := TryReserveTokenQuota(token.Id, token.Key, 60, true); results <- result{ok, err} }()
	}
	var accepted int
	for i := 0; i < 2; i++ {
		got := <-results
		require.NoError(t, got.err)
		if got.ok {
			accepted++
		}
	}
	batchUpdate()
	assert.Equal(t, 1, accepted, "a supported scoped limit edit must cap the combined admissions")
	current := getTokenFromDB(t, token.Id)
	assert.Equal(t, 40, current.RemainQuota)
	cached, err := common.RDB.HGet(context.Background(), getTokenCacheKey(token.Key), "RemainQuota").Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(40), cached)
}

func TestNativeQuotaGenerationSurvivesCacheLossAndMutationRollback(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	owner := createReserveTestUser(t, 1000)
	token := createReserveTestToken(t, 1000)
	require.NoError(t, DB.Model(&token).Updates(map[string]any{"user_id": owner.Id, "auth_realm": NativeFundingLocalOnly, "unlimited_quota": true}).Error)
	require.NoError(t, DB.First(&token, token.Id).Error)
	enrollNativeQuotaFixture(t, token.Id)
	stale := token
	token.UnlimitedQuota = false
	token.RemainQuota = 100
	require.NoError(t, token.Update(NativeFundingLocalOnly))
	current := getTokenFromDB(t, token.Id)
	require.EqualValues(t, 1, current.QuotaPolicyGeneration)
	// Redis loss cannot erase the authoritative SQL policy generation.
	server.FlushAll()
	code, err := cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, code)
	code, err = cacheInitToken(current)
	require.NoError(t, err)
	assert.Equal(t, 1, code)
	token.RemainQuota = 500
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("native_reject_policy_update", func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" {
			tx.AddError(errors.New("synthetic SQL write failure"))
		}
	}))
	err = token.Update(NativeFundingLocalOnly)
	require.NoError(t, DB.Callback().Update().Remove("native_reject_policy_update"))
	require.Error(t, err)
	current = getTokenFromDB(t, token.Id)
	assert.EqualValues(t, 1, current.QuotaPolicyGeneration)
	assert.Equal(t, 100, current.RemainQuota)
	server.FlushAll()
	code, err = cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, code)
	code, err = cacheInitToken(current)
	require.NoError(t, err)
	assert.Equal(t, 1, code)
}

func TestNativeQuotaPolicyChangeBetweenReadAndReserveCannotBypassLimit(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	owner := createReserveTestUser(t, 1000)
	token := createReserveTestToken(t, 1000)
	require.NoError(t, DB.Model(&token).Updates(map[string]any{"user_id": owner.Id, "auth_realm": NativeFundingLocalOnly, "unlimited_quota": true}).Error)
	require.NoError(t, DB.First(&token, token.Id).Error)
	enrollNativeQuotaFixture(t, token.Id)
	loaded, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	require.True(t, loaded.UnlimitedQuota)
	read, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("native_stale_admission_policy", func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" && paused.CompareAndSwap(false, true) {
			close(read)
			<-release
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Query().Remove("native_stale_admission_policy") })
	deltaDone := make(chan error, 1)
	common.RDB.AddHook(nativeTokenDeltaObserved{done: deltaDone})
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() { ok, err := TryReserveTokenQuota(token.Id, token.Key, 200, true); done <- result{ok, err} }()
	<-read
	token.UnlimitedQuota = false
	token.RemainQuota = 100
	updateErr := token.Update(NativeFundingLocalOnly)
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	close(release)
	require.NoError(t, updateErr)
	got := <-done
	require.NoError(t, got.err)
	if got.ok {
		require.NoError(t, <-deltaDone)
	}
	batchUpdate()
	assert.False(t, got.ok, "the persisted policy changed before atomic quota admission")
	assert.Equal(t, 100, getTokenFromDB(t, token.Id).RemainQuota)
}

// Prior-generation accepted operations must not debit a later absolute
// remaining-balance edit again when the durable batch projection catches up.
func TestNativeQuotaPolicyEditWithUnflushedPriorGeneration(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	owner := createReserveTestUser(t, 1000)
	token := createReserveTestToken(t, 1000)
	require.NoError(t, DB.Model(&token).Updates(map[string]any{"user_id": owner.Id, "auth_realm": NativeFundingLocalOnly}).Error)
	require.NoError(t, DB.First(&token, token.Id).Error)
	enrollNativeQuotaFixture(t, token.Id)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 80, false)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1000, getTokenFromDB(t, token.Id).RemainQuota)
	token.RemainQuota = 100
	require.NoError(t, token.Update(NativeFundingLocalOnly))
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	ok, err = TryReserveTokenQuota(token.Id, token.Key, 60, false)
	require.NoError(t, err)
	require.True(t, ok)
	batchUpdate()
	assert.Equal(t, 40, getTokenFromDB(t, token.Id).RemainQuota, "an old-generation delta must not debit a newly set remaining balance twice")
}

func TestPreEnrollmentKeySnapshotCannotBypassNativePolicyGeneration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/enrollment-cache.db"), &gorm.Config{})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB; require.NoError(t, conn.Close()) })
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	require.NoError(t, RegisterProductFundingModels(DB))
	require.NoError(t, DB.AutoMigrate(&ProductAuthLifecycle{}))
	owner := createReserveTestUser(t, 1000)
	token := createReserveTestToken(t, 1000)
	require.NoError(t, DB.Model(&token).Update("user_id", owner.Id).Error)
	require.NoError(t, DB.First(&token, token.Id).Error)
	require.Empty(t, token.AuthRealm)
	read, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("pre_enrollment_snapshot", func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" && paused.CompareAndSwap(false, true) {
			close(read)
			<-release
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Query().Remove("pre_enrollment_snapshot") })
	done := make(chan error, 1)
	go func() { _, err := GetTokenByKey(token.Key, false); done <- err }()
	<-read
	manifest := NativeRealmManifest{Version: 1, Users: []NativeRealmEnrollment{{UserID: owner.Id, Role: owner.Role, Status: owner.Status, AuthVersion: owner.AuthVersion, PasswordSHA256: nativeEnrollmentDigest(owner.Password), Keys: []NativeKeyEnrollment{{ID: token.Id, SecretSHA256: nativeEnrollmentDigest(token.Key)}}}}}
	raw, marshalErr := common.Marshal(manifest)
	enrollErr := EnrollNativeRealm(context.Background(), DB, raw, nativeEnrollmentDigest(string(raw)))
	enrollNativeQuotaFixture(t, token.Id)
	token.RemainQuota = 100
	updateErr := token.Update(NativeFundingLocalOnly)
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	close(release)
	require.NoError(t, marshalErr)
	require.NoError(t, enrollErr)
	require.NoError(t, updateErr)
	require.NoError(t, <-done)
	var accepted int
	for i := 0; i < 2; i++ {
		ok, err := TryReserveTokenQuota(token.Id, token.Key, 60, false)
		require.NoError(t, err)
		if ok {
			accepted++
		}
	}
	batchUpdate()
	assert.Equal(t, 1, accepted, "a pre-enrollment blank-realm reader must not resurrect its old balance")
	assert.Equal(t, 40, getTokenFromDB(t, token.Id).RemainQuota)
}
