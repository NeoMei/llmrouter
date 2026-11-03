package model

import (
	"context"
	"strconv"
	"testing"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductCashAdjustmentTelemetryRequiresInheritedFrozenContract(t *testing.T) {
	s, _ := cashFundingFixture(t, 7.3)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(s.db))
	require.NoError(t, s.db.AutoMigrate(&Task{}, &Channel{}, &Log{}))
	oldDB, oldLogs := DB, LOG_DB
	oldConsume, oldExport := common.LogConsumeEnabled, common.DataExportEnabled
	DB, LOG_DB, common.LogConsumeEnabled, common.DataExportEnabled = s.db, s.db, true, false
	t.Cleanup(func() {
		DB, LOG_DB = oldDB, oldLogs
		common.LogConsumeEnabled, common.DataExportEnabled = oldConsume, oldExport
	})
	parent, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "cash-telemetry-task", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 4})
	require.NoError(t, err)
	require.NoError(t, s.MarkDispatched(ctx, parent.ID))
	require.NoError(t, s.Settle(ctx, parent.ID, 4, `{"quota":4}`))
	require.NoError(t, s.db.Create(&Channel{Id: 9, UsedQuota: 4}).Error)
	require.NoError(t, s.db.Create(&Task{ID: 1, UserId: 42, ChannelId: 9, ProductCallID: parent.ID, TaskID: "cash-telemetry-provider-task", Quota: 4, Status: TaskStatusSuccess}).Error)
	require.NoError(t, s.Reprice(ctx, parent.ID, "up", 7, "provider measurement"))
	require.NoError(t, s.Reprice(ctx, parent.ID, "down", 5, "provider measurement"))
	payer, err := s.Account(ctx, 42)
	require.NoError(t, err)
	var addition, refund ProductCallAdjustment
	require.NoError(t, s.db.First(&addition, "parent_call_id = ? AND request_key = ?", parent.ID, "up").Error)
	require.NoError(t, s.db.First(&refund, "parent_call_id = ? AND request_key = ?", parent.ID, "down").Error)
	var child ProductRuntimeCall
	require.NoError(t, s.db.First(&child, "id = ?", addition.ChildCallID).Error)
	assert.Equal(t, 4, addition.BaseQuota)
	assert.Equal(t, 7, refund.BaseQuota)
	assert.Equal(t, 4, child.CashBaseQuota)
	for _, op := range []ProductCallAdjustment{addition, refund} {
		var log Log
		require.NoError(t, s.db.First(&log, "request_id = ?", op.ID).Error)
		entry, admitted, err := readProductAdjustmentTelemetryLog(ctx, &log, op, payer, s.ScopeDigest())
		require.NoError(t, err)
		require.True(t, admitted, "real committed adjustment %s must remain visible", op.RequestKey)
		assert.True(t, entry.Adjustment)
		assert.Equal(t, userkit.RuntimeCashAsset, entry.Asset)
		assert.Equal(t, parent.ID, entry.ParentCallID)
	}

	for _, mutation := range []string{"rate", "base"} {
		t.Run(mutation, func(t *testing.T) {
			changed := child
			reserve, err := ProductRuntimeCallContract(changed)
			require.NoError(t, err)
			switch mutation {
			case "rate":
				numerator, err := strconv.ParseInt(changed.ConversionNumerator, 10, 64)
				require.NoError(t, err)
				changed.ConversionNumerator = strconv.FormatInt(numerator*2, 10)
			case "base":
				changed.CashBaseQuota++
			}
			// A self-consistent standalone child still cannot change the frozen
			// parent rate or the cumulative base accepted for its creation.
			changed.PricingDigest = productCashPricingDigest(changed.SnapshotDigest, changed)
			changed.ContractDigest = productCallContractDigest(changed, reserve.Model, reserve.Operation)
			reserve.PricingDigest, reserve.RequestDigest = changed.PricingDigest, changed.ContractDigest
			amount, err := productCallAmount(changed, changed.ReservedQuota)
			require.NoError(t, err)
			reserve.Amount = strconv.FormatInt(amount, 10)
			raw, err := common.Marshal(reserve)
			require.NoError(t, err)
			changed.ReserveJSON = string(raw)
			_, err = ProductRuntimeCallContract(changed)
			require.NoError(t, err, "the child must remain valid on its own to exercise parent inheritance")
			require.NoError(t, s.db.Save(&changed).Error)
			t.Cleanup(func() { require.NoError(t, s.db.Save(&child).Error) })
			for _, op := range []ProductCallAdjustment{addition, refund} {
				var log Log
				require.NoError(t, s.db.First(&log, "request_id = ?", op.ID).Error)
				_, admitted, err := readProductAdjustmentTelemetryLog(ctx, &log, op, payer, s.ScopeDigest())
				require.NoError(t, err)
				assert.False(t, admitted, "%s must reject a child with a different %s", op.RequestKey, mutation)
			}
		})
	}
}
