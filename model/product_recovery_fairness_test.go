package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/require"
)

type productRecoveryTransport func(*http.Request) (*http.Response, error)

func (f productRecoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProductRefundRecoveryRotatesPastSlowReceiptAfterDeadline(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, s.Import(ctx, 42, "payer", "slow-refund"))
	for _, id := range []string{"blocked", "healthy"} {
		_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: id, UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 10})
		require.NoError(t, e)
		require.NoError(t, s.Settle(ctx, id, 10, `{}`))
		ledger.loseRefundReply = true
		require.Error(t, s.Refund(ctx, id, "failed", 10, "failed"))
	}
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "healthy-adjustment", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 10})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, "healthy-adjustment", 10, `{}`))
	ledger.loseSettleReply = true
	require.Error(t, s.Reprice(ctx, "healthy-adjustment", "measured", 20, "measured"))
	// Force the bad remote receipt first without relying on hash iteration.
	require.NoError(t, db.Model(&ProductCallRefund{}).Where("call_id = ?", "blocked").Update("updated_at", 0).Error)
	require.NoError(t, db.Model(&ProductCallRefund{}).Where("call_id = ?", "healthy").Update("updated_at", 1).Error)
	client, e := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: ledger.origin, ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32), HTTPClient: &http.Client{Transport: productRecoveryTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/blocked/refund") {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return http.DefaultTransport.RoundTrip(r)
	})}})
	require.NoError(t, e)
	s = NewProductFundingStore(db, client)
	for i := 0; i < 2; i++ {
		run, stop := context.WithTimeout(ctx, 150*time.Millisecond)
		require.Error(t, s.RecoverRefunds(run))
		stop()
	}
	var healthy ProductCallRefund
	require.NoError(t, db.Where("call_id = ?", "healthy").First(&healthy).Error)
	require.Equal(t, "applied", healthy.State, "a repeatedly slow older receipt cannot monopolize every recovery deadline")
	var adjustment ProductCallAdjustment
	require.NoError(t, db.Where("parent_call_id = ?", "healthy-adjustment").First(&adjustment).Error)
	require.Equal(t, "applied", adjustment.State, "the independent adjustment queue retains a deadline even when refunds are slow")
}

func TestProductFundingRecoveryContinuesAfterBrokenTopUp(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "fair-topup"))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "recover-after-bad-topup", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, e)
	ledger.loseSettleReply = true
	require.Error(t, s.Settle(ctx, "recover-after-bad-topup", 30, `{}`))
	require.NoError(t, db.Create(&ProductFundingTopUp{ID: "broken", CallID: "missing-call", TargetQuota: 1, Delta: 1, State: "pending"}).Error)
	require.Error(t, s.Recover(ctx), "bad receipt remains visible to the operator")
	var call ProductRuntimeCall
	require.NoError(t, db.First(&call, "id = ?", "recover-after-bad-topup").Error)
	require.Equal(t, "settled", call.State, "independent recoverable settlement is not starved")
	require.Equal(t, 1, ledger.settlements)
}

func TestProductRefundRecoveryContinuesAfterBrokenRefundAndAdjustment(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, s.Import(ctx, 42, "payer", "fair-refund"))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "fair-parent", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, "fair-parent", 50, `{}`))
	ledger.loseSettleReply = true
	require.Error(t, s.Reprice(ctx, "fair-parent", "measured", 80, "task measured"))
	require.NoError(t, db.Create(&ProductCallRefund{ID: "broken-refund", CallID: "missing-call", Amount: 1, State: "pending"}).Error)
	require.NoError(t, db.Create(&ProductCallAdjustment{ID: "broken-adjustment", ParentCallID: "missing-call", State: "pending"}).Error)
	require.Error(t, s.RecoverRefunds(ctx))
	var op ProductCallAdjustment
	require.NoError(t, db.Where("parent_call_id = ?", "fair-parent").First(&op).Error)
	require.Equal(t, "applied", op.State, "malformed older receipts do not prevent a valid adjustment")
	require.Equal(t, 420, ledger.balance)
}

func TestProductFundingWalletFirstSubscriptionFallbackRetainsDispatch(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&NativeBilling{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}, &Channel{}, &UserSubscription{}, &SubscriptionPlan{}, &NativeUsageReceipt{}))
	require.NoError(t, db.Create(&Channel{Id: 8, Name: "speech"}).Error)
	require.NoError(t, db.Create(&SubscriptionPlan{Id: 1, Title: "prepaid"}).Error)
	require.NoError(t, db.Create(&UserSubscription{Id: 1, UserId: 42, PlanId: 1, Status: "active", AmountTotal: 100, StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix()}).Error)
	require.NoError(t, s.Import(ctx, 42, "payer", "fallback"))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_ORIGIN", ledger.origin)
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	ledger.rejectReserve = true
	native := NewNativeBillingStore(db, nil)
	price := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "10"}}}}
	n, e := native.Begin(ctx, NativeAdmission{CallID: "wallet-fallback", UserID: 42, TokenID: 7, ChannelID: 8, Model: "speech", Group: "default", Preference: "wallet_first", Price: price, GroupRatio: "1", QuotaPerUnit: "1", Reservation: nativeUsage("400")})
	require.NoError(t, e)
	require.NoError(t, native.MarkDispatched(ctx, n.ID, n.AttemptID))
	require.NoError(t, MarkProductFundingDispatched(ctx, n.ID), "the rejected wallet candidate cannot block the purchased subscription dispatch")
	var wallet ProductRuntimeCall
	require.NoError(t, db.First(&wallet, "id = ?", n.ID).Error)
	require.Equal(t, "rejected", wallet.State)
	require.Zero(t, wallet.TokenReserved)
	require.Zero(t, wallet.DispatchedAt)
	require.NoError(t, native.RecordUsage(ctx, n.ID, nativeUsage("200")))
	_, e = native.Finish(ctx, n.ID, "measured")
	require.NoError(t, e)
	require.Equal(t, 500, ledger.balance)
	var sub UserSubscription
	require.NoError(t, db.First(&sub, 1).Error)
	require.EqualValues(t, 20, sub.AmountUsed)
}
