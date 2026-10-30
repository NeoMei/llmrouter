package model

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func separateFundingDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "global-funding.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, RegisterProductFundingModels(db))
	return db
}

func TestOCRReserveUsesOwnedDatabaseWhenGlobalOwnerAbsent(t *testing.T) {
	store, admission := ocrFixture(t)
	global := separateFundingDatabase(t)
	old := DB
	DB = global
	t.Cleanup(func() { DB = old })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, RegisterProductFundingModels(store.db))
	var count int64
	require.NoError(t, global.Model(&User{}).Where("id = ?", admission.UserID).Count(&count).Error)
	require.Zero(t, count)
	job, err := store.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	accepted, err := store.Reserve(context.Background(), job.ID)
	require.NoError(t, err)
	assert.Equal(t, "reserved", accepted.BillingState)
	ocrBalances(t, store, admission, 900, 400, 100)
	require.NoError(t, global.Model(&User{}).Count(&count).Error)
	assert.Zero(t, count, "owned reserve cannot create or select an unrelated global owner")
}

func TestOCRReserveCannotUseGlobalLocalOwnerToBypassOwnedFundingPending(t *testing.T) {
	store, admission := ocrFixture(t)
	global := separateFundingDatabase(t)
	old := DB
	DB = global
	t.Cleanup(func() { DB = old })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, RegisterProductFundingModels(store.db))
	require.NoError(t, global.Create(&User{Id: admission.UserID, Username: "unrelated-local", AffCode: "unrelated-local", Status: common.UserStatusEnabled, Quota: 777}).Error)
	job, err := store.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	require.NoError(t, store.db.Create(&ProductFundingAccount{UserID: admission.UserID, State: "provisioning"}).Error)
	_, err = store.Reserve(context.Background(), job.ID)
	require.ErrorIs(t, err, ErrProductFundingPending)
	ocrBalances(t, store, admission, 1000, 500, 0)
	var unrelated User
	require.NoError(t, global.First(&unrelated, admission.UserID).Error)
	assert.Equal(t, 777, unrelated.Quota)
}

func TestOCRReserveCannotUseGlobalCentralOwnerForOwnedLocalFunding(t *testing.T) {
	store, admission := ocrFixture(t)
	global := separateFundingDatabase(t)
	old := DB
	DB = global
	t.Cleanup(func() { DB = old })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, RegisterProductFundingModels(store.db))
	require.NoError(t, global.Create(&User{Id: admission.UserID, Username: "unrelated-central", AffCode: "unrelated-central", Status: common.UserStatusEnabled, Quota: 777}).Error)
	require.NoError(t, global.Create(&ProductFundingAccount{UserID: admission.UserID, State: "central", AccountID: "unrelated-central-account"}).Error)
	job, err := store.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	accepted, err := store.Reserve(context.Background(), job.ID)
	require.NoError(t, err)
	assert.Equal(t, "reserved", accepted.BillingState)
	ocrBalances(t, store, admission, 900, 400, 100)
	var calls int64
	require.NoError(t, global.Model(&ProductRuntimeCall{}).Count(&calls).Error)
	assert.Zero(t, calls)
}

func TestOCRReserveMissingOwnedUserCannotFallBackToMatchingGlobalOwner(t *testing.T) {
	store, admission := ocrFixture(t)
	global := separateFundingDatabase(t)
	old := DB
	DB = global
	t.Cleanup(func() { DB = old })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, RegisterProductFundingModels(store.db))
	require.NoError(t, global.Create(&User{Id: admission.UserID, Username: "unrelated-present", AffCode: "unrelated-present", Status: common.UserStatusEnabled, Quota: 777}).Error)
	job, err := store.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	require.NoError(t, store.db.Unscoped().Delete(&User{}, admission.UserID).Error)
	_, err = store.Reserve(context.Background(), job.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var token Token
	require.NoError(t, store.db.First(&token, admission.TokenID).Error)
	assert.Equal(t, 500, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	var unrelated User
	require.NoError(t, global.First(&unrelated, admission.UserID).Error)
	assert.Equal(t, 777, unrelated.Quota)
}

func TestOCRReserveOwnedLegacySchemaMetadataControlsFunding(t *testing.T) {
	for _, state := range []string{"local", "pending", "missing", "sql-error"} {
		t.Run(state, func(t *testing.T) {
			store, admission := ocrFixture(t)
			global := separateFundingDatabase(t)
			old := DB
			DB = global
			t.Cleanup(func() { DB = old })
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
			require.NoError(t, global.Create(&User{Id: admission.UserID, Username: "unrelated-global", AffCode: "unrelated-global", Status: common.UserStatusEnabled, Quota: 777}).Error)
			require.False(t, store.db.Migrator().HasTable(&ProductFundingAccount{}), "legacy store has no funding schema")
			job, err := store.CreateIntent(context.Background(), admission)
			require.NoError(t, err)
			switch state {
			case "pending":
				require.NoError(t, store.db.Model(&User{}).Where("id = ?", admission.UserID).Update("product_registration_pending", true).Error)
			case "missing":
				require.NoError(t, store.db.Unscoped().Delete(&User{}, admission.UserID).Error)
			case "sql-error":
				require.NoError(t, store.db.Callback().Query().Before("gorm:query").Register("owned-funding-metadata-error", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("owned funding metadata unavailable"))
					}
				}))
				t.Cleanup(func() { _ = store.db.Callback().Query().Remove("owned-funding-metadata-error") })
			}
			result, err := store.Reserve(context.Background(), job.ID)
			switch state {
			case "local":
				require.NoError(t, err)
				assert.Equal(t, "reserved", result.BillingState)
				ocrBalances(t, store, admission, 900, 400, 100)
			case "pending":
				require.ErrorIs(t, err, ErrProductFundingPending)
			case "missing":
				require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			case "sql-error":
				require.ErrorContains(t, err, "owned funding metadata unavailable")
			}
			if state != "local" {
				var token Token
				require.NoError(t, store.db.First(&token, admission.TokenID).Error)
				assert.Equal(t, 500, token.RemainQuota)
				assert.Zero(t, token.UsedQuota)
			}
			var unrelated User
			require.NoError(t, global.First(&unrelated, admission.UserID).Error)
			assert.Equal(t, 777, unrelated.Quota)
		})
	}
}

func TestOwnedFundingStoreBindsCentralAccountAndRetainsAcceptedDeletedOwner(t *testing.T) {
	store, admission := ocrFixture(t)
	global := separateFundingDatabase(t)
	old := DB
	DB = global
	t.Cleanup(func() { DB = old })
	require.NoError(t, RegisterProductFundingModels(store.db))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "bound-database-fixture")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	require.NoError(t, store.db.Create(&ProductFundingAccount{UserID: admission.UserID, State: "central", AccountID: "owned-central-account"}).Error)
	require.NoError(t, global.Create(&ProductFundingAccount{UserID: admission.UserID, State: "provisioning", AccountID: "unrelated-global-account"}).Error)
	bound, managed, err := productFundingStoreForDB(context.Background(), store.db, admission.UserID)
	require.NoError(t, err)
	require.True(t, managed)
	require.Same(t, store.db, bound.db)
	require.NoError(t, store.db.Model(&ProductFundingAccount{}).Where("user_id = ?", admission.UserID).Update("scope_digest", bound.ScopeDigest()).Error)
	account, err := bound.Account(context.Background(), admission.UserID)
	require.NoError(t, err)
	assert.Equal(t, "owned-central-account", account.AccountID)
	// A pre-existing accepted central obligation remains bound to its original
	// account after soft deletion. This lookup grants no new provider dispatch.
	require.NoError(t, store.db.Delete(&User{}, admission.UserID).Error)
	bound, managed, err = productFundingStoreForDB(context.Background(), store.db, admission.UserID)
	require.NoError(t, err)
	require.True(t, managed)
	account, err = bound.Account(context.Background(), admission.UserID)
	require.NoError(t, err)
	assert.Equal(t, "owned-central-account", account.AccountID)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	state, err := productFundingStateForDB(context.Background(), store.db, admission.UserID)
	require.NoError(t, err)
	assert.Equal(t, "central", state, "accepted deleted-owner metadata remains visible with runtime off")
}

func TestOCRReserveRejectsOwnedFundingSchemaSQLFailure(t *testing.T) {
	store, admission := ocrFixture(t)
	global := separateFundingDatabase(t)
	old := DB
	DB = global
	t.Cleanup(func() { DB = old })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, RegisterProductFundingModels(store.db))
	job, err := store.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	require.NoError(t, store.db.Create(&ProductFundingAccount{UserID: admission.UserID, State: "central", AccountID: "owned-central-account"}).Error)
	var calls int
	wrapped := store.db.Session(&gorm.Session{NewDB: true})
	wrapped.Statement.ConnPool = fundingSchemaFaultPool{ConnPool: store.db.Statement.ConnPool, faults: &calls}
	store.db = wrapped
	result, reserveErr := store.Reserve(context.Background(), job.ID)
	var wallet User
	var token Token
	require.NoError(t, store.db.First(&wallet, admission.UserID).Error)
	require.NoError(t, store.db.First(&token, admission.TokenID).Error)
	t.Logf("metadata_faults=%d reserve_error=%v state=%s wallet=%d token_remaining=%d token_used=%d", calls, reserveErr, result.BillingState, wallet.Quota, token.RemainQuota, token.UsedQuota)
	require.Error(t, reserveErr, "metadata SQL failure must not authorize local wallet")
	require.NotZero(t, calls)
	ocrBalances(t, store, admission, 1000, 500, 0)
}

type fundingSchemaFaultPool struct {
	gorm.ConnPool
	faults *int
}

func (p fundingSchemaFaultPool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	if strings.Contains(query, "sqlite_master") {
		(*p.faults)++
		return p.ConnPool.QueryRowContext(ctx, "SELECT * FROM review_missing_funding_metadata")
	}
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}
func (p fundingSchemaFaultPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	return p.ConnPool.(*sql.DB).BeginTx(ctx, opts)
}
