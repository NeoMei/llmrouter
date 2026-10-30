package model

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func checkinRealmFixture(t *testing.T, driverName ...string) (*gorm.DB, *ProductFundingStore) {
	t.Helper()
	dialect := sqlite.Dialector{DSN: t.TempDir() + "/checkin.db?_pragma=busy_timeout(0)"}
	if len(driverName) > 0 {
		dialect.DriverName = driverName[0]
		dialect.DSN += "&_pragma=foreign_keys(1)"
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(4)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	require.NoError(t, db.AutoMigrate(&User{}, &Checkin{}, &ProductCreditAward{}))
	require.NoError(t, RegisterProductFundingModels(db))
	require.NoError(t, db.Create(&User{Id: 42, Username: "native", Status: common.UserStatusEnabled, AuthVersion: 1}).Error)
	previous, redis, batch, dbtype := DB, common.RedisEnabled, common.BatchUpdateEnabled, common.MainDatabaseType()
	DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB, common.RedisEnabled, common.BatchUpdateEnabled = previous, redis, batch
		common.SetMainDatabaseType(dbtype)
	})
	setting := operation_setting.GetCheckinSetting()
	old := *setting
	*setting = operation_setting.CheckinSetting{Enabled: true, MinQuota: 20, MaxQuota: 20}
	t.Cleanup(func() { *setting = old })
	return db, NewProductFundingStore(db, nil)
}

// A pinned native owner cannot enter central funding even while its original
// SQLite checkin path is between the receipt and quota write.
func TestUserCheckinSQLiteLocalOnlyRejectsActivationAtCreditWrite(t *testing.T) {
	db, store := checkinRealmFixture(t)
	require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-native"))
	var activationErr error
	attempted := false
	const callback = "test:activation-at-credit-write"
	require.NoError(t, db.Callback().Update().Before("gorm:begin_transaction").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "users" || attempted {
			return
		}
		attempted = true
		result := make(chan error, 1)
		go func() { result <- store.ActivateZero(context.Background(), 42, "payer") }()
		activationErr = <-result
	}))
	_, err := UserCheckin(42)
	require.NoError(t, db.Callback().Update().Remove(callback))
	require.True(t, attempted)
	require.NoError(t, err)
	require.ErrorIs(t, activationErr, ErrNativeFundingLocalOnly)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20, user.Quota)
	var accounts, awards, records int64
	require.NoError(t, db.Model(&ProductFundingAccount{}).Count(&accounts).Error)
	require.NoError(t, db.Model(&ProductCreditAward{}).Count(&awards).Error)
	require.NoError(t, db.Model(&Checkin{}).Count(&records).Error)
	assert.Zero(t, accounts)
	assert.Zero(t, awards)
	assert.EqualValues(t, 1, records)
	_, err = UserCheckin(42)
	require.Error(t, err)
}

func TestUserCheckinSQLiteRechecksActivationAfterPreflight(t *testing.T) {
	db, store := checkinRealmFixture(t)
	// Activation commits while the public entry finishes its advisory duplicate
	// read. It must then use central ownership inside its credit transaction.
	var activationErr error
	attempted := false
	const callback = "test:activation-after-preflight"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "checkins" || attempted {
			return
		}
		attempted = true
		result := make(chan error, 1)
		go func() { result <- store.ActivateZero(context.Background(), 42, "payer") }()
		activationErr = <-result
	}))
	_, err := UserCheckin(42)
	require.NoError(t, db.Callback().Query().Remove(callback))
	require.NoError(t, activationErr)
	require.NoError(t, err)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Zero(t, user.Quota)
	var awards int64
	require.NoError(t, db.Model(&ProductCreditAward{}).Count(&awards).Error)
	assert.EqualValues(t, 1, awards)
	_, err = UserCheckin(42)
	require.Error(t, err)
}

// Observe completion of the original asynchronous quota-cache delta without
// sleeps or changing production scheduling.
type checkinQuotaDeltaHook struct{ done chan error }

func (h checkinQuotaDeltaHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (h checkinQuotaDeltaHook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "eval" || cmd.Name() == "evalsha" {
		if len(cmd.Args()) > 1 && cmd.Args()[1] == userQuotaDeltaScript {
			h.done <- cmd.Err()
		}
	}
	return nil
}
func (h checkinQuotaDeltaHook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (h checkinQuotaDeltaHook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	return nil
}

func TestUserCheckinRedisLocalOnlyPreservesOriginalCredit(t *testing.T) {
	db, _ := checkinRealmFixture(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-native"))
	cached, err := GetUserCache(42)
	require.NoError(t, err)
	assert.Zero(t, cached.Quota)
	done := make(chan error, 1)
	common.RDB.AddHook(checkinQuotaDeltaHook{done: done})
	_, err = UserCheckin(42)
	require.NoError(t, err)
	require.NoError(t, <-done)
	cached, err = GetUserCache(42)
	require.NoError(t, err)
	assert.Equal(t, 20, cached.Quota)
	_, err = UserCheckin(42)
	require.Error(t, err)
	var records int64
	require.NoError(t, db.Model(&Checkin{}).Count(&records).Error)
	assert.EqualValues(t, 1, records)
	batchUpdate()
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20, user.Quota)
	assert.Equal(t, "LOCAL_ONLY", user.FundingRealm)
	assert.True(t, common.RedisEnabled)
	assert.True(t, common.BatchUpdateEnabled)
}

func TestUserCheckinSQLiteConcurrentEntryAwardsOnce(t *testing.T) {
	db, _ := checkinRealmFixture(t)
	common.BatchUpdateEnabled = true
	require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-native"))
	_, err := GetUserCache(42)
	require.NoError(t, err)
	// Both actual entries pass the advisory duplicate check before either writes.
	var ready sync.WaitGroup
	ready.Add(2)
	release := make(chan struct{})
	var checks atomic.Int32
	const callback = "test:concurrent-checkin-preflight"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "checkins" && checks.Add(1) <= 2 {
			ready.Done()
			<-release
		}
	}))
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := UserCheckin(42); results <- err }()
	}
	ready.Wait()
	close(release)
	first, second := <-results, <-results
	require.NoError(t, db.Callback().Query().Remove(callback))
	// With busy_timeout(0), competing SQLite transactions may reject both
	// contenders. They must roll back without an award, stranded receipt, or
	// poisoned connection. Preserve four connections so failures stay observable.
	t.Logf("concurrent outcomes: first=%v second=%v", first, second)
	succeeded := 0
	for _, result := range []error{first, second} {
		if result == nil {
			succeeded++
		}
	}
	require.LessOrEqual(t, succeeded, 1, "daily receipt must authorize at most one award")
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20*succeeded, user.Quota, "no failed entry may leave a partial credit")
	var records int64
	require.NoError(t, db.Model(&Checkin{}).Count(&records).Error)
	assert.EqualValues(t, succeeded, records, "no failed entry may strand its daily receipt")
	batchUpdate()
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20*succeeded, user.Quota)
	if succeeded == 0 {
		_, err = UserCheckin(42)
		require.NoError(t, err, "after contention ends a rejected daily award remains retryable")
	}
	_, err = UserCheckin(42)
	require.Error(t, err)
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20, user.Quota)
	require.NoError(t, db.Model(&Checkin{}).Count(&records).Error)
	assert.EqualValues(t, 1, records)
}

func TestUserCheckinRedisCentralAwardDoesNotCreditNativeCache(t *testing.T) {
	db, store := checkinRealmFixture(t)
	require.NoError(t, store.ActivateZero(context.Background(), 42, "payer"))
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	_, err := GetUserCache(42)
	require.NoError(t, err)
	_, err = UserCheckin(42)
	require.NoError(t, err)
	_, err = UserCheckin(42)
	require.Error(t, err)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Zero(t, user.Quota)
	cached, err := GetUserCache(42)
	require.NoError(t, err)
	assert.Zero(t, cached.Quota)
	var awards, records int64
	require.NoError(t, db.Model(&ProductCreditAward{}).Count(&awards).Error)
	require.NoError(t, db.Model(&Checkin{}).Count(&records).Error)
	assert.EqualValues(t, 1, awards)
	assert.EqualValues(t, 1, records)
}
