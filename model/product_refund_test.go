package model

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

// A financial receipt must not become locally complete while the original
// task still displays its old charge and the channel keeps its old usage.
func TestProductRepriceRecoversTaskAccountingAfterFinancialCommit(t *testing.T) {
	for _, kind := range []string{"task", "midjourney"} {
		t.Run(kind, func(t *testing.T) {
			s, db, ledger := productFundingFixture(t)
			ctx := context.Background()
			require.NoError(t, RegisterProductRefundModels(db))
			require.NoError(t, db.AutoMigrate(&Task{}, &Midjourney{}, &Channel{}, &Log{}, &QuotaData{}))
			oldDB, oldLogs, oldEnabled, oldExport := DB, LOG_DB, common.LogConsumeEnabled, common.DataExportEnabled
			DB, LOG_DB, common.LogConsumeEnabled, common.DataExportEnabled = db, db, true, true
			t.Cleanup(func() {
				DB, LOG_DB, common.LogConsumeEnabled, common.DataExportEnabled = oldDB, oldLogs, oldEnabled, oldExport
			})
			require.NoError(t, s.Import(ctx, 42, "payer", "accounting-batch"))
			_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "accounting", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 50})
			require.NoError(t, err)
			require.NoError(t, s.Settle(ctx, "accounting", 50, `{"used":50}`))
			require.NoError(t, db.Create(&Channel{Id: 9, UsedQuota: 50}).Error)
			table := "tasks"
			if kind == "task" {
				require.NoError(t, db.Create(&Task{ID: 1, UserId: 42, ChannelId: 9, ProductCallID: "accounting", TaskID: "original-task", Quota: 50, Status: TaskStatusSuccess}).Error)
			} else {
				table = "midjourneys"
				require.NoError(t, db.Create(&Midjourney{Id: 1, UserId: 42, ChannelId: 9, ProductCallID: "accounting", MjId: "original-task", Quota: 50, Status: "SUCCESS"}).Error)
			}
			require.NoError(t, db.Exec(fmt.Sprintf("CREATE TRIGGER fail_accounting BEFORE UPDATE OF quota ON %s BEGIN SELECT RAISE(ABORT, 'task accounting unavailable'); END", table)).Error)
			require.Error(t, s.Reprice(ctx, "accounting", "measured", 80, "task measured"))
			require.Equal(t, 420, ledger.balance, "central charge already committed")
			var user User
			require.NoError(t, db.First(&user, 42).Error)
			require.Equal(t, 50, user.UsedQuota, "local financial marker and task update roll back together")
			require.NoError(t, db.Exec("DROP TRIGGER fail_accounting").Error)
			require.NoError(t, db.Exec("CREATE TRIGGER fail_adjustment_log BEFORE INSERT ON logs BEGIN SELECT RAISE(ABORT, 'log database unavailable'); END").Error)
			require.Error(t, s.RecoverRefunds(ctx), "log failure leaves the durable outbox recoverable")
			require.NoError(t, db.Exec("DROP TRIGGER fail_adjustment_log").Error)
			require.NoError(t, s.RecoverRefunds(ctx))
			require.NoError(t, s.RecoverRefunds(ctx))
			// A polling snapshot created before recovery can finish later. Its
			// ordinary status save must not rewind the journal-owned charge.
			if kind == "task" {
				stale := &Task{ID: 1, UserId: 42, ChannelId: 9, ProductCallID: "accounting", TaskID: "original-task", Quota: 50, Status: TaskStatusSuccess}
				require.NoError(t, stale.Update())
				won, e := stale.UpdateWithStatus(TaskStatusSuccess)
				require.NoError(t, e)
				require.True(t, won)
			} else {
				stale := &Midjourney{Id: 1, UserId: 42, ChannelId: 9, ProductCallID: "accounting", MjId: "original-task", Quota: 50, Status: "SUCCESS"}
				require.NoError(t, stale.Update())
				won, e := stale.UpdateWithStatus("SUCCESS")
				require.NoError(t, e)
				require.True(t, won)
				require.NoError(t, stale.UpdateBillingState())
			}
			var quota int
			var version int64
			require.NoError(t, db.Table(table).Select("quota").Where("id = 1").Scan(&quota).Error)
			require.NoError(t, db.Table(table).Select("product_billing_version").Where("id = 1").Scan(&version).Error)
			require.Equal(t, 80, quota)
			require.EqualValues(t, 1, version)
			var channel Channel
			require.NoError(t, db.First(&channel, 9).Error)
			require.EqualValues(t, 80, channel.UsedQuota)
			require.NoError(t, db.First(&user, 42).Error)
			require.Equal(t, 80, user.UsedQuota)
			var logs []Log
			require.NoError(t, db.Find(&logs).Error)
			require.Len(t, logs, 1)
			require.Equal(t, 30, logs[0].Quota)
			require.Equal(t, LogTypeConsume, logs[0].Type)
			var stats []QuotaData
			require.NoError(t, db.Find(&stats).Error)
			require.Len(t, stats, 1)
			require.Equal(t, 30, stats[0].Quota)
			require.Equal(t, 1, stats[0].Count)
		})
	}
}

func TestProductRepriceSeparateLogDatabaseRecoversCommittedDelivery(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, db.AutoMigrate(&Task{}, &Channel{}, &QuotaData{}))
	logs, e := gorm.Open(sqlite.Open(t.TempDir()+"/logs.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, logs.AutoMigrate(&Log{}))
	oldLogs := LOG_DB
	LOG_DB = logs
	t.Cleanup(func() { LOG_DB = oldLogs })
	require.NoError(t, s.Import(ctx, 42, "payer", "separate-logs-batch"))
	_, e = s.Reserve(ctx, ProductFundingAdmission{CallID: "separate-logs", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, "separate-logs", 50, `{"used":50}`))
	require.NoError(t, db.Create(&Channel{Id: 9, UsedQuota: 50}).Error)
	require.NoError(t, db.Create(&Task{ID: 1, UserId: 42, ChannelId: 9, ProductCallID: "separate-logs", TaskID: "original-task", Quota: 50}).Error)
	require.NoError(t, db.Exec("CREATE TRIGGER fail_log_delivery_marker BEFORE UPDATE OF log_delivered_at ON product_call_adjustments WHEN NEW.log_delivered_at > 0 BEGIN SELECT RAISE(ABORT, 'source marker unavailable'); END").Error)
	require.Error(t, s.Reprice(ctx, "separate-logs", "refund", 0, "failed"))
	require.Equal(t, 500, ledger.balance)
	var count int64
	require.NoError(t, logs.Model(&Log{}).Count(&count).Error)
	require.EqualValues(t, 1, count, "log DB committed before source receipt acknowledgement failed")
	require.NoError(t, db.Exec("DROP TRIGGER fail_log_delivery_marker").Error)
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, logs.Model(&Log{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestProductRepriceChildRetainsCommitmentAfterLostReserveReply(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, s.Import(ctx, 42, "payer", "commitment-batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "committed-parent", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, err)
	require.NoError(t, s.Settle(ctx, "committed-parent", 50, `{"used":50}`))
	ledger.loseReserveReply = true
	require.Error(t, s.Reprice(ctx, "committed-parent", "measured", 80, "task measured"))
	var op ProductCallAdjustment
	require.NoError(t, db.Where("parent_call_id = ?", "committed-parent").First(&op).Error)
	var child ProductRuntimeCall
	require.NoError(t, db.First(&child, "id = ?", op.ChildCallID).Error)
	require.True(t, child.FinancialCommitment, "durable financial intent must exist before a remote reserve can commit")
	require.NoError(t, db.Model(&child).Update("dispatch_lease_until", 1).Error)
	require.NoError(t, s.RecoverRefunds(ctx))
	require.Equal(t, 420, ledger.balance)
	require.Zero(t, ledger.held)
}

func TestProductRefundRecoversLostReplyWithoutDoubleKeyOrUserCredit(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, s.Import(ctx, 42, "payer", "refund-batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "refund-call", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, err)
	require.NoError(t, s.Settle(ctx, "refund-call", 30, `{"used":30}`))
	ledger.loseRefundReply = true
	require.Error(t, s.Refund(ctx, "refund-call", "task-failed", 10, "provider failure"))
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, s.Refund(ctx, "refund-call", "task-failed", 10, "provider failure"))
	var user User
	var token Token
	require.NoError(t, db.First(&user, 42).Error)
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 480, ledger.balance)
	require.Equal(t, 20, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
	require.Equal(t, 180, token.RemainQuota)
	require.Equal(t, 20, token.UsedQuota)
	require.Error(t, s.Refund(ctx, "refund-call", "task-failed", 11, "provider failure"))
}
func TestProductRepriceUsesFixedParentAndRefundsAllChargeSegments(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, s.Import(ctx, 42, "payer", "adjust-batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "parent", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, err)
	require.NoError(t, s.Settle(ctx, "parent", 50, `{"used":50}`))
	require.NoError(t, s.Reprice(ctx, "parent", "actual-80", 80, "task measured"))
	require.NoError(t, s.Reprice(ctx, "parent", "actual-80", 80, "task measured"))
	require.Equal(t, 420, ledger.balance)
	require.NoError(t, s.Reprice(ctx, "parent", "actual-60", 60, "task corrected"))
	require.Equal(t, 440, ledger.balance)
	require.NoError(t, s.Reprice(ctx, "parent", "failure", 0, "task failed"))
	require.NoError(t, s.Reprice(ctx, "parent", "failure", 0, "task failed"))
	require.Equal(t, 500, ledger.balance)
	var user User
	var token Token
	require.NoError(t, db.First(&user, 42).Error)
	require.NoError(t, db.First(&token, 7).Error)
	require.Zero(t, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
	require.Equal(t, 200, token.RemainQuota)
	require.Zero(t, token.UsedQuota)
}

func TestProductRepriceRecoversPartialSegmentRefundAndChildSettlement(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, s.Import(ctx, 42, "payer", "segments-batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "segments", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, err)
	require.NoError(t, s.Settle(ctx, "segments", 50, `{"used":50}`))
	ledger.loseSettleReply = true
	require.Error(t, s.Reprice(ctx, "segments", "increase", 80, "measured"))
	require.NoError(t, s.RecoverRefunds(ctx))
	require.Equal(t, 420, ledger.balance)
	ledger.loseRefundReply = true
	require.Error(t, s.Reprice(ctx, "segments", "failure", 0, "failed"))
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, s.Reprice(ctx, "segments", "failure", 0, "failed"))
	require.Equal(t, 500, ledger.balance)
	var user User
	var token Token
	require.NoError(t, db.First(&user, 42).Error)
	require.NoError(t, db.First(&token, 7).Error)
	require.Zero(t, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
	require.Equal(t, 200, token.RemainQuota)
}
