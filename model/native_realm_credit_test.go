package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOrdinaryUserUpdateCannotChangeFundingRealm(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "cannot_enroll", true: "cannot_replace"}[pinned], func(t *testing.T) {
			_, db, _ := productFundingFixture(t)
			old := DB
			DB = db
			t.Cleanup(func() { DB = old })
			if pinned {
				require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-original"))
			}
			input := User{Id: 42, DisplayName: "edited", FundingRealm: "LOCAL_ONLY", FundingRealmEvidence: "untrusted-profile-input"}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return input.UpdateWithTx(tx, false) }))
			var actual User
			require.NoError(t, db.First(&actual, 42).Error)
			assert.Equal(t, "edited", actual.DisplayName)
			if pinned {
				assert.Equal(t, "LOCAL_ONLY", actual.FundingRealm)
				assert.Equal(t, "reviewed-original", actual.FundingRealmEvidence)
			} else {
				assert.Empty(t, actual.FundingRealm)
				assert.Empty(t, actual.FundingRealmEvidence)
			}
		})
	}
}

func TestCreditFundingFenceFailureRollsBackBusinessReceipt(t *testing.T) {
	for _, kind := range []string{"redemption", "checkin"} {
		t.Run(kind, func(t *testing.T) {
			_, db, _ := productFundingFixture(t)
			require.NoError(t, db.AutoMigrate(&Redemption{}, &Checkin{}, &ProductCreditAward{}, &Log{}))
			old, oldLog, redis, batch := DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled
			DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = db, db, false, false
			t.Cleanup(func() { DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = old, oldLog, redis, batch })
			code := Redemption{Key: "realm-test-code", Name: "fixture", Status: common.RedemptionCodeStatusEnabled, Quota: 20}
			require.NoError(t, db.Create(&code).Error)
			const callback = "test:owner-fence-unavailable"
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "users" {
					tx.AddError(errors.New("owner row unavailable"))
				}
			}))
			var err error
			if kind == "redemption" {
				_, err = Redeem(code.Key, 42)
			} else {
				_, err = userCheckinWithTransaction(&Checkin{UserId: 42, CheckinDate: "2026-10-30", QuotaAwarded: 20}, 42, 20)
			}
			require.NoError(t, db.Callback().Query().Remove(callback))
			require.Error(t, err)
			var user User
			require.NoError(t, db.First(&user, 42).Error)
			assert.Equal(t, 500, user.Quota)
			require.NoError(t, db.First(&code, code.Id).Error)
			assert.Equal(t, common.RedemptionCodeStatusEnabled, code.Status)
			var checkins, awards int64
			require.NoError(t, db.Model(&Checkin{}).Count(&checkins).Error)
			require.NoError(t, db.Model(&ProductCreditAward{}).Count(&awards).Error)
			assert.Zero(t, checkins)
			assert.Zero(t, awards)
		})
	}
}

func TestCreditFundingOwnershipRespectsActivationOrder(t *testing.T) {
	for _, kind := range []string{"redemption", "checkin"} {
		for _, order := range []string{"credit_first", "activation_first", "local_only"} {
			t.Run(kind+"/"+order, func(t *testing.T) {
				store, db, _ := productFundingFixture(t)
				require.NoError(t, db.AutoMigrate(&Redemption{}, &Checkin{}, &ProductCreditAward{}, &Log{}))
				old, oldLog, redis, batch := DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled
				DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = db, db, false, false
				t.Cleanup(func() { DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = old, oldLog, redis, batch })
				require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("quota", 0).Error)
				ctx := context.Background()
				if order == "activation_first" {
					require.NoError(t, store.ActivateZero(ctx, 42, "payer"))
				}
				if order == "local_only" {
					require.NoError(t, PinNativeFundingRealm(ctx, 42, "reviewed-original"))
				}
				code := Redemption{Key: "realm-test-code", Name: "fixture", Status: common.RedemptionCodeStatusEnabled, Quota: 20}
				require.NoError(t, db.Create(&code).Error)
				credit := func() error {
					if kind == "redemption" {
						_, err := Redeem(code.Key, 42)
						return err
					}
					_, err := userCheckinWithTransaction(&Checkin{UserId: 42, CheckinDate: time.Now().Format("2006-01-02"), QuotaAwarded: 20}, 42, 20)
					return err
				}
				require.NoError(t, credit())
				require.Error(t, credit())
				var user User
				require.NoError(t, db.First(&user, 42).Error)
				var awards []ProductCreditAward
				require.NoError(t, db.Where("user_id = ?", 42).Find(&awards).Error)
				if order == "activation_first" {
					assert.Zero(t, user.Quota)
					require.Len(t, awards, 1)
				} else {
					assert.Equal(t, 20, user.Quota)
					assert.Empty(t, awards)
					require.Error(t, store.ActivateZero(ctx, 42, "payer"))
				}
			})
		}
	}
}

func TestCreditAndZeroActivationCannotSplitFundingOwnership(t *testing.T) {
	for _, kind := range []string{"redemption", "checkin"} {
		t.Run(kind, func(t *testing.T) {
			store, db, _ := productFundingFixture(t)
			require.NoError(t, db.AutoMigrate(&Redemption{}, &Checkin{}, &ProductCreditAward{}, &Log{}))
			old, oldLog, redis, batch := DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled
			DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = db, db, false, false
			t.Cleanup(func() { DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = old, oldLog, redis, batch })
			conn, err := db.DB()
			require.NoError(t, err)
			conn.SetMaxOpenConns(1)
			require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("quota", 0).Error)
			code := Redemption{Key: "concurrent-realm-code", Name: "fixture", Status: common.RedemptionCodeStatusEnabled, Quota: 20}
			require.NoError(t, db.Create(&code).Error)
			start := make(chan struct{})
			var wg sync.WaitGroup
			var creditErr, activationErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				activationErr = store.ActivateZero(context.Background(), 42, "payer")
			}()
			go func() {
				defer wg.Done()
				<-start
				if kind == "redemption" {
					_, creditErr = Redeem(code.Key, 42)
				} else {
					_, creditErr = userCheckinWithTransaction(&Checkin{UserId: 42, CheckinDate: "2026-10-30", QuotaAwarded: 20}, 42, 20)
				}
			}()
			close(start)
			wg.Wait()
			require.NoError(t, creditErr)
			var user User
			require.NoError(t, db.First(&user, 42).Error)
			var awards, accounts int64
			require.NoError(t, db.Model(&ProductCreditAward{}).Count(&awards).Error)
			require.NoError(t, db.Model(&ProductFundingAccount{}).Count(&accounts).Error)
			if activationErr == nil {
				assert.Zero(t, user.Quota)
				assert.EqualValues(t, 1, awards)
				assert.EqualValues(t, 1, accounts)
			} else {
				require.ErrorIs(t, activationErr, ErrProductFundingMigration)
				assert.Equal(t, 20, user.Quota)
				assert.Zero(t, awards)
				assert.Zero(t, accounts)
			}
		})
	}
}

func TestCreditWriteFailureRollsBackReceiptAndRemainsRetryable(t *testing.T) {
	for _, kind := range []string{"redemption", "checkin"} {
		t.Run(kind, func(t *testing.T) {
			_, db, _ := productFundingFixture(t)
			require.NoError(t, db.AutoMigrate(&Redemption{}, &Checkin{}, &ProductCreditAward{}, &Log{}))
			old, oldLog, redis, batch := DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled
			DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = db, db, false, false
			t.Cleanup(func() { DB, LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = old, oldLog, redis, batch })
			require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-original"))
			code := Redemption{Key: "rollback-realm-code", Name: "fixture", Status: common.RedemptionCodeStatusEnabled, Quota: 20}
			require.NoError(t, db.Create(&code).Error)
			credit := func() error {
				if kind == "redemption" {
					_, err := Redeem(code.Key, 42)
					return err
				}
				_, err := userCheckinWithTransaction(&Checkin{UserId: 42, CheckinDate: "2026-10-30", QuotaAwarded: 20}, 42, 20)
				return err
			}
			const callback = "test:credit-write-unavailable"
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "users" {
					tx.AddError(errors.New("quota unavailable"))
				}
			}))
			require.Error(t, credit())
			require.NoError(t, db.Callback().Update().Remove(callback))
			var user User
			require.NoError(t, db.First(&user, 42).Error)
			assert.Equal(t, 500, user.Quota)
			require.NoError(t, db.First(&code, code.Id).Error)
			assert.Equal(t, common.RedemptionCodeStatusEnabled, code.Status)
			var checkins int64
			require.NoError(t, db.Model(&Checkin{}).Count(&checkins).Error)
			assert.Zero(t, checkins)
			require.NoError(t, credit())
			require.Error(t, credit())
			require.NoError(t, db.First(&user, 42).Error)
			assert.Equal(t, 520, user.Quota)
			assert.Equal(t, "LOCAL_ONLY", user.FundingRealm)
		})
	}
}
