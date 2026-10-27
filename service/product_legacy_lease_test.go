package service

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type failedLeaseWallet struct{ fail func() error }

func (f failedLeaseWallet) Source() string       { return BillingSourceWallet }
func (f failedLeaseWallet) PreConsume(int) error { return f.fail() }
func (f failedLeaseWallet) Settle(int) error     { return nil }
func (f failedLeaseWallet) Refund() error        { return nil }

func legacyLeaseFixture(t *testing.T) (*gorm.DB, *gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/legacy-lease.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, model.RegisterProductFundingModels(db))
	old, redis, batch := model.DB, common.RedisEnabled, common.BatchUpdateEnabled
	model.DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
	t.Cleanup(func() { model.DB, common.RedisEnabled, common.BatchUpdateEnabled = old, redis, batch })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	require.NoError(t, db.Create(&model.User{Id: 42, Username: "old-local", Quota: 500, Status: common.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 7, UserId: 42, Key: "original-key", RemainQuota: 200, ExpiredTime: -1, Status: common.TokenStatusEnabled}).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RequestId: "unadmitted", UserId: 42, TokenId: 7, TokenKey: "original-key", ForcePreConsume: true}
	info.UserSetting.BillingPreference = "wallet_only"
	return db, c, info
}
func assertLegacyLease(t *testing.T, db *gorm.DB, state string) {
	t.Helper()
	var lease model.ProductLegacyFundingLease
	require.NoError(t, db.First(&lease, "id = ?", "unadmitted").Error)
	require.Equal(t, state, lease.State)
}

func TestRejectedLocalKeyClosesMigrationLeaseBeforeSessionIsDiscarded(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 7).Update("remain_quota", 10).Error)
	session, e := NewBillingSession(c, info, 20)
	require.Nil(t, session)
	require.NotNil(t, e)
	assertLegacyLease(t, db, "refunded")
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 500, user.Quota)
}

func TestDefiniteLocalFundingDenialClosesLeaseOnlyAfterKeyRollback(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	s := &BillingSession{relayInfo: info, funding: failedLeaseWallet{fail: func() error { return ErrInsufficientWalletQuota }}}
	e := s.preConsume(c, 20)
	require.NotNil(t, e)
	assertLegacyLease(t, db, "refunded")
	require.Zero(t, s.tokenConsumed)
	var token model.Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
}

func TestUncertainKeyRollbackRetainsLeaseAndConsumedAmountAndBlocksFallback(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	const callback = "test:reject-token-rollback"
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	s := &BillingSession{relayInfo: info, funding: failedLeaseWallet{fail: func() error {
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "tokens" {
				tx.AddError(errors.New("rollback write unavailable"))
			}
		}))
		return ErrInsufficientWalletQuota
	}}}
	e := s.preConsume(c, 20)
	require.NotNil(t, e)
	require.Equal(t, types.ErrorCodeUpdateDataError, e.GetErrorCode(), "wallet_first must not proceed after uncertain Key compensation")
	require.Equal(t, 20, s.tokenConsumed)
	assertLegacyLease(t, db, "active")
	var token model.Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 180, token.RemainQuota)
}

func TestUncertainLocalFundingAndKeyReserveKeepMigrationFence(t *testing.T) {
	for _, phase := range []string{"funding", "key"} {
		t.Run(phase, func(t *testing.T) {
			db, c, info := legacyLeaseFixture(t)
			if phase == "key" {
				const callback = "test:unknown-key-reserve"
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "tokens" {
						tx.AddError(errors.New("key reserve unavailable"))
					}
				}))
				t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
			}
			s := &BillingSession{relayInfo: info, funding: failedLeaseWallet{fail: func() error { return errors.New("funding reserve status unknown") }}}
			require.NotNil(t, s.preConsume(c, 20))
			assertLegacyLease(t, db, "active")
		})
	}
}

func TestZeroPreconsumeFailureClosesLeaseWithoutInventingRefund(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	s := &BillingSession{relayInfo: info, funding: &WalletFunding{userId: info.UserId}}
	require.Nil(t, s.preConsume(c, 0))
	assertLegacyLease(t, db, "active")
	s.Refund(c)
	assertLegacyLease(t, db, "refunded")
	var token model.Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 500, user.Quota)
}
