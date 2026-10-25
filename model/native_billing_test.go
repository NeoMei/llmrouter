package model

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func nativeFixture(t *testing.T) (*NativeBillingStore, *OCRBillingStore, NativeAdmission, OCRAdmission) {
	t.Helper()
	s, a := ocrFixture(t)
	require.NoError(t, s.db.AutoMigrate(&NativeBilling{}))
	p := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}
	n := NativeAdmission{CallID: a.IdempotencyKey, UserID: a.UserID, TokenID: a.TokenID, ChannelID: a.ChannelID, Model: a.Model, Group: a.Group, Preference: a.Preference, Price: p, GroupRatio: "1", QuotaPerUnit: "100", Reservation: nativeUsage("1000")}
	return NewNativeBillingStore(s.db, nil), s, n, a
}
func nativeUsage(v string) metering.Usage {
	return metering.Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Source: "fixture", Measurements: []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: v}}}}
}
func TestNativeMissingZeroAndDuplicateFinish(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	ocrBalances(t, s, old, 900, 400, 100)
	require.NoError(t, n.RecordUsage(ctx, j.ID, metering.Usage{State: "missing"}))
	j, err = n.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending_measurement", j.State)
	_, err = n.Finish(ctx, j.ID, "measured")
	require.Error(t, err)
	ocrBalances(t, s, old, 900, 400, 100)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("0")))
	for i := 0; i < 2; i++ {
		j, err = n.Finish(ctx, j.ID, "measured")
		require.NoError(t, err)
		assert.Equal(t, "settled", j.State)
	}
	ocrBalances(t, s, old, 1000, 500, 0)
}

func TestNativeSupplierQuantityBeyondFrozenCapRequiresReconciliation(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("1001")))
	j, err = n.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending_measurement", j.State)
	assert.Equal(t, "reservation_limit_exceeded", j.Reason)
	assert.Contains(t, j.LastObservation, `"value":"1001"`)
	_, err = n.Finish(ctx, j.ID, "measured")
	require.ErrorIs(t, err, ErrOCRPending)
	ocrBalances(t, s, old, 900, 400, 100)
	_, err = n.ResolveMeasurement(ctx, j.ID, "cap-reviewed", 1, "operator_loss_release", nil, "reviewed supplier discrepancy")
	require.NoError(t, err)
	ocrBalances(t, s, old, 1000, 500, 0)
}
func TestNativeFrozenPriceAndRestart(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	a.Price.Terms[0].Price.Numerator = "9"
	_, err = n.Begin(ctx, a)
	require.Error(t, err)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("500")))
	n = NewNativeBillingStore(s.db, nil)
	j, err = n.Finish(ctx, j.ID, "measured")
	require.NoError(t, err)
	assert.Equal(t, 50, j.FinalQuota)
	ocrBalances(t, s, old, 950, 450, 50)
	require.Error(t, n.RecordUsage(ctx, j.ID, nativeUsage("400")))
	ocrBalances(t, s, old, 950, 450, 50)
}
func TestNativePlatformNeverFundsLocally(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	a.Platform = true
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("1000")))
	_, err = n.Finish(ctx, j.ID, "measured")
	require.Error(t, err)
	ocrBalances(t, s, old, 1000, 500, 0)
}

func TestNativeRedisPartialSettlementRecovery(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	mr := miniredis.RunT(t)
	cache := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	n = NewNativeBillingStore(s.db, cache)
	t.Cleanup(func() { require.NoError(t, n.Close()) })
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("500")))
	injected := errors.New("token SQL receipt unavailable")
	require.NoError(t, s.db.Callback().Update().Before("gorm:update").Register("native_partial", func(tx *gorm.DB) {
		if op, ok := tx.Statement.Dest.(*OCROperation); ok && op.Kind == "token" && op.Phase == "native_credit" {
			tx.AddError(injected)
		}
	}))
	_, err = n.Finish(ctx, j.ID, "measured")
	require.ErrorIs(t, err, injected)
	j, err = n.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, "settlement_pending", j.State)
	require.NoError(t, s.db.Callback().Update().Remove("native_partial"))
	restart := NewNativeBillingStore(s.db, cache)
	defer restart.Close()
	require.NoError(t, restart.Recover(ctx, 10))
	ocrBalances(t, s, old, 950, 450, 50)
	require.NoError(t, restart.Recover(ctx, 10))
	ocrBalances(t, s, old, 950, 450, 50)
}
func TestNativeQuotaExactAndReservationCeiling(t *testing.T) {
	_, _, a, _ := nativeFixture(t)
	a.GroupRatio = "1"
	a.QuotaPerUnit = "1"
	a.Price.Terms[0].Price.Denominator = "3"
	q, clamp, err := NativeQuota(a, nativeUsage("1"), true)
	require.NoError(t, err)
	require.Nil(t, clamp)
	assert.Equal(t, 1, q)
	q, clamp, err = NativeQuota(a, nativeUsage("1"), false)
	require.NoError(t, err)
	require.Nil(t, clamp)
	assert.Zero(t, q)
}
func TestNativeSubscriptionPriorPeriodRefund(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	a.Preference = "subscription_only"
	plan := SubscriptionPlan{Title: "native", QuotaResetPeriod: SubscriptionResetNever}
	require.NoError(t, s.db.Create(&plan).Error)
	sub := UserSubscription{UserId: a.UserID, PlanId: plan.Id, AmountTotal: 1000, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, s.db.Create(&sub).Error)
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&sub).Updates(map[string]any{"reset_generation": 1, "amount_used": 37}).Error)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("0")))
	_, err = n.Finish(ctx, j.ID, "measured")
	require.NoError(t, err)
	require.NoError(t, s.db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 37, sub.AmountUsed)
	ocrBalances(t, s, old, 1000, 500, 0)
}

func TestNativeAdminResolutionIdempotencyAndConflictingCommand(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, n.MarkDispatched(ctx, j.ID, j.AttemptID))
	_, err = n.Finish(ctx, j.ID, "pre_dispatch_failure")
	require.Error(t, err)
	j, err = n.ResolveMeasurement(ctx, j.ID, "resolve-1", 1, "operator_loss_release", nil, "provider could not establish output")
	require.NoError(t, err)
	assert.Equal(t, "released", j.State)
	_, err = n.ResolveMeasurement(ctx, j.ID, "resolve-1", 1, "operator_loss_release", nil, "provider could not establish output")
	require.NoError(t, err)
	_, err = n.ResolveMeasurement(ctx, j.ID, "resolve-1", 1, "confirm_no_output", nil, "changed reason")
	require.ErrorIs(t, err, ErrOCRConflict)
	ocrBalances(t, s, old, 1000, 500, 0)
}

func TestNativeReceiptProjectionUsesActualQuantityOnce(t *testing.T) {
	n, s, a, _ := nativeFixture(t)
	ctx := context.Background()
	require.NoError(t, s.db.AutoMigrate(&Log{}, &OCRLogReceipt{}))
	old := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled = old })
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("500")))
	_, err = n.Finish(ctx, j.ID, "measured")
	require.NoError(t, err)
	require.NoError(t, s.ProjectAuditLogs(ctx, s.db, 10))
	require.NoError(t, s.ProjectAuditLogs(ctx, s.db, 10))
	var logs []Log
	require.NoError(t, s.db.Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, 50, logs[0].Quota)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
	assert.Equal(t, "character", other["billing_unit"])
	assert.Equal(t, "speech", other["operation"])
	assert.Contains(t, logs[0].Other, `"value":"500"`)
}

func TestNativeRecoverMeasuredEvidenceAfterRestart(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, n.RecordUsage(ctx, j.ID, nativeUsage("500")))
	st := NewNativeBillingStore(s.db, nil)
	require.NoError(t, n.Close())
	_, err = st.Inspect(ctx, j.ID)
	require.NoError(t, err)
	require.NoError(t, st.Recover(ctx, 10))
	out, err := st.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, "settled", out.State)
	ocrBalances(t, s, old, 950, 450, 50)
}
func TestNativeRecoverAdminIntentAfterRestart(t *testing.T) {
	n, s, a, old := nativeFixture(t)
	ctx := context.Background()
	j, err := n.Begin(ctx, a)
	require.NoError(t, err)

	injected := errors.New("crash before finish intent commit")
	require.NoError(t, s.db.Callback().Update().Before("gorm:update").Register("native_resolution_crash", func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*NativeBilling); ok && row.Outcome != "" {
			tx.AddError(injected)
		}
	}))
	_, err = n.ResolveMeasurement(ctx, j.ID, "op-restart", 1, "confirm_no_output", nil, "operator confirmed no output")
	require.ErrorIs(t, err, injected)
	require.NoError(t, s.db.Callback().Update().Remove("native_resolution_crash"))
	persisted, err := n.Inspect(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "op-restart", persisted.AdminActionID)
	require.Empty(t, persisted.Outcome)

	st := NewNativeBillingStore(s.db, nil)
	require.NoError(t, st.Recover(ctx, 10))
	out, err := st.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, "released", out.State)
	ocrBalances(t, s, old, 1000, 500, 0)
}
