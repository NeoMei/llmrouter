package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Replaying a native success audit must restore its old monitoring projection
// without replaying a wallet/token debit or increasing the logical call count.
func TestNativeAuditRestoresDashboardTokensExactlyOnce(t *testing.T) {
	native, ledger, admission, old := nativeFixture(t)
	require.NoError(t, ledger.db.AutoMigrate(&Log{}, &OCRLogReceipt{}, &QuotaData{}, &NativeUsageReceipt{}))
	oldEnabled := common.LogConsumeEnabled
	oldExport := common.DataExportEnabled
	common.LogConsumeEnabled = true
	common.DataExportEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled, common.DataExportEnabled = oldEnabled, oldExport })
	admission.Price = metering.Price{Version: "metered_v2", Operation: "embedding", Policy: "sum", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}, {Key: "cache_read", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}
	admission.Reservation = metering.Usage{Version: "metered_v2", Operation: "embedding", State: "confirmed", Source: "server_bound", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "1000"}}, {Key: "cache_read", Unit: "token", Quantity: metering.Quantity{Value: "0"}}}}
	ctx := context.Background()
	row, err := native.Begin(ctx, admission)
	require.NoError(t, err)
	require.NoError(t, native.MarkDispatched(ctx, row.ID, row.AttemptID))
	actual := admission.Reservation
	actual.Source = "provider"
	actual.Measurements = []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "70"}}, {Key: "cache_read", Unit: "token", Quantity: metering.Quantity{Value: "30"}}}
	require.NoError(t, native.RecordUsage(ctx, row.ID, actual))
	_, err = native.Finish(ctx, row.ID, "measured")
	require.NoError(t, err)
	var event OCRAuditEvent
	require.NoError(t, ledger.db.Where("job_id = ?", row.ID).First(&event).Error)
	require.NoError(t, ledger.DeliverAuditLog(ctx, ledger.db, event))
	require.NoError(t, ledger.DeliverAuditLog(ctx, ledger.db, event))
	var logRows []Log
	require.NoError(t, ledger.db.Find(&logRows).Error)
	require.Len(t, logRows, 1)
	assert.Equal(t, 100, logRows[0].PromptTokens)
	assert.Zero(t, logRows[0].CompletionTokens)
	var data []QuotaData
	require.NoError(t, ledger.db.Find(&data).Error)
	require.Len(t, data, 1)
	assert.Equal(t, 100, data[0].TokenUsed)
	assert.Equal(t, 1, data[0].Count)
	assert.Equal(t, 10, data[0].Quota)
	assert.Equal(t, admission.UserID, data[0].UserID)
	assert.Equal(t, admission.TokenID, data[0].TokenID)
	assert.Equal(t, admission.ChannelID, data[0].ChannelID)
	assert.Equal(t, "default", data[0].UseGroup)
	ocrBalances(t, ledger, old, 990, 490, 10)
	var user User
	require.NoError(t, ledger.db.First(&user, admission.UserID).Error)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 10, user.UsedQuota)
	// A pre-upgrade audit may already have delivered its log. The new dashboard
	// projection must backfill that settled receipt without creating another log.
	require.NoError(t, ledger.db.Where("1 = 1").Delete(&NativeUsageReceipt{}).Error)
	require.NoError(t, ledger.db.Where("1 = 1").Delete(&QuotaData{}).Error)
	require.NoError(t, ledger.db.Model(&OCRAuditEvent{}).Where("id = ?", event.ID).Update("projected_at", event.CreatedAt).Error)
	require.NoError(t, ledger.db.Model(&Log{}).Where("id = ?", logRows[0].Id).Update("prompt_tokens", 0).Error)
	require.NoError(t, native.ProjectAuditLogs(ctx, ledger.db, 100))
	var backfill []QuotaData
	require.NoError(t, ledger.db.Find(&backfill).Error)
	require.Len(t, backfill, 1)
	assert.Equal(t, 100, backfill[0].TokenUsed)
	var restored []Log
	require.NoError(t, ledger.db.Find(&restored).Error)
	require.Len(t, restored, 1)
	assert.Equal(t, 100, restored[0].PromptTokens)
	require.NoError(t, native.ProjectAuditLogs(ctx, ledger.db, 100))
	ocrBalances(t, ledger, old, 990, 490, 10)
}

func TestNativeAuditDisabledConsumptionDoesNotStarveLaterFailure(t *testing.T) {
	native, ledger, admission, _ := nativeFixture(t)
	require.NoError(t, ledger.db.AutoMigrate(&Log{}, &OCRLogReceipt{}))
	oldConsume, oldExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	t.Cleanup(func() { common.LogConsumeEnabled, common.DataExportEnabled = oldConsume, oldExport })
	ctx := context.Background()
	first, err := native.Begin(ctx, admission)
	require.NoError(t, err)
	require.NoError(t, native.RecordUsage(ctx, first.ID, nativeUsage("10")))
	_, err = native.Finish(ctx, first.ID, "measured")
	require.NoError(t, err)
	require.NoError(t, native.ProjectAuditLogs(ctx, ledger.db, 100))
	// This success has a log receipt but no dashboard receipt, as on an upgrade.
	common.LogConsumeEnabled, common.DataExportEnabled = false, true
	admission.CallID = "later-definitive-failure"
	second, err := native.Begin(ctx, admission)
	require.NoError(t, err)
	_, err = native.Finish(ctx, second.ID, "no_output")
	require.NoError(t, err)
	var success OCRAuditEvent
	require.NoError(t, ledger.db.Where("job_id = ?", first.ID).First(&success).Error)
	require.NoError(t, ledger.db.Model(&OCRAuditEvent{}).Where("id = ?", success.ID).Update("created_at", success.CreatedAt-1).Error)
	require.NoError(t, native.ProjectAuditLogs(ctx, ledger.db, 1))
	var failure OCRAuditEvent
	require.NoError(t, ledger.db.Where("job_id = ?", second.ID).First(&failure).Error)
	assert.NotZero(t, failure.ProjectedAt)
	var log Log
	require.NoError(t, ledger.db.Where("request_id = ?", "ocr-audit-"+failure.ID).First(&log).Error)
	assert.Equal(t, LogTypeError, log.Type)
	var count int64
	require.NoError(t, ledger.db.Model(&NativeUsageReceipt{}).Count(&count).Error)
	assert.Zero(t, count)
	// Re-enabling consumption still backfills the success exactly once.
	common.LogConsumeEnabled = true
	require.NoError(t, native.ProjectAuditLogs(ctx, ledger.db, 100))
	require.NoError(t, ledger.db.Model(&NativeUsageReceipt{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
