package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestAcceptedTaskRepriceRetainsOriginalPayerAfterCredentialAndUserRevocation(t *testing.T) {
	for _, mode := range []string{"expired_key", "revoked_key", "deleted_key", "disabled_user", "deleted_user"} {
		t.Run(mode, func(t *testing.T) {
			s, db, ledger := productFundingFixture(t)
			ctx := context.Background()
			require.NoError(t, RegisterProductRefundModels(db))
			require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
			_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "accepted-task", UserID: 42, TokenID: 7, Model: "video", Operation: "task", PriceSnapshot: `{"price":1}`, Quota: 20})
			require.NoError(t, e)
			require.NoError(t, s.MarkDispatched(ctx, "accepted-task"))
			require.NoError(t, s.Settle(ctx, "accepted-task", 20, `{"task":true}`))
			switch mode {
			case "expired_key":
				require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Update("expired_time", time.Now().Unix()-10).Error)
			case "revoked_key":
				require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Update("status", common.TokenStatusDisabled).Error)
			case "deleted_key":
				require.NoError(t, db.Delete(&Token{}, 7).Error)
			case "disabled_user":
				require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("status", common.UserStatusDisabled).Error)
			case "deleted_user":
				require.NoError(t, db.Delete(&User{}, 42).Error)
			}
			_, e = s.Reserve(ctx, ProductFundingAdmission{CallID: "new-request", UserID: 42, TokenID: 7, Model: "video", Operation: "task", PriceSnapshot: `{"price":1}`, Quota: 10})
			require.Error(t, e, "revoked credentials still forbid new model requests")
			require.NoError(t, s.Reprice(ctx, "accepted-task", "provider-actual", 30, "verified supplier cost"))
			require.NoError(t, s.Reprice(ctx, "accepted-task", "provider-actual", 30, "verified supplier cost"))
			require.Equal(t, 470, ledger.balance)
			require.Equal(t, 2, ledger.settlements)
			var token Token
			require.NoError(t, db.Unscoped().First(&token, 7).Error)
			require.Equal(t, 170, token.RemainQuota)
			require.Equal(t, 30, token.UsedQuota)
			var user User
			require.NoError(t, db.Unscoped().First(&user, 42).Error)
			require.Equal(t, 30, user.UsedQuota)
			require.Equal(t, 1, user.RequestCount)
		})
	}
}

func TestParentObligationCannotChangeOriginalUserKeyPriceOrPayer(t *testing.T) {
	s, db, _ := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "parent", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 20})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, "parent", 20, `{"accepted":true}`))
	var parent ProductRuntimeCall
	require.NoError(t, db.First(&parent, "id = ?", "parent").Error)
	base := ProductFundingAdmission{CallID: "child", ParentCallID: "parent", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: parent.ReserveJSON, Quota: 10, ExternalCounters: true, FinancialCommitment: true}
	for _, alter := range []func(*ProductFundingAdmission){
		func(a *ProductFundingAdmission) { a.FinancialCommitment = false }, func(a *ProductFundingAdmission) { a.ExternalCounters = false }, func(a *ProductFundingAdmission) { a.TokenID = 0 }, func(a *ProductFundingAdmission) { a.Model = "other" }, func(a *ProductFundingAdmission) { a.Operation = "chat" }, func(a *ProductFundingAdmission) { a.PriceSnapshot = `{"p":2}` }, func(a *ProductFundingAdmission) { a.ParentCallID = "missing" },
	} {
		a := base
		alter(&a)
		_, e = s.Reserve(ctx, a)
		require.Error(t, e)
	}
	_, e = s.Reserve(ctx, base)
	require.NoError(t, e)
	var child ProductRuntimeCall
	require.NoError(t, db.First(&child, "id = ?", "child").Error)
	require.Equal(t, parent.PricingDigest, child.PricingDigest)
	require.NoError(t, db.Model(&ProductRuntimeCall{}).Where("id = ?", "parent").Update("state", "released").Error)
	_, e = s.Reserve(ctx, base)
	require.Error(t, e)
}

func TestProductTopUpPinsAcceptedObligationOnlyAfterSupplierDispatch(t *testing.T) {
	s, db, _ := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "accepted-topup", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 20})
	require.NoError(t, e)
	require.NoError(t, s.TopUp(ctx, "accepted-topup", 30))
	require.NoError(t, s.MarkDispatched(ctx, "accepted-topup"))
	require.NoError(t, s.Settle(ctx, "accepted-topup", 40, `{"actual":40}`))
	var intents []ProductFundingTopUp
	require.NoError(t, db.Where("call_id = ?", "accepted-topup").Order("target_quota").Find(&intents).Error)
	require.Len(t, intents, 2)
	require.False(t, intents[0].AcceptedCall, "new pre-dispatch admission still requires current product permission")
	require.True(t, intents[1].AcceptedCall, "confirmed final usage completes the already accepted call")
}

func TestAcceptedTaskRepriceWaitsForRealFundsAndRecoversSameChildIntent(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "waiting-task", UserID: 42, TokenID: 7, Model: "video", Operation: "task", PriceSnapshot: `{"price":1}`, Quota: 20})
	require.NoError(t, e)
	require.NoError(t, s.MarkDispatched(ctx, "waiting-task"))
	require.NoError(t, s.Settle(ctx, "waiting-task", 20, `{"task":true}`))
	ledger.rejectReserve = true
	require.ErrorIs(t, s.Reprice(ctx, "waiting-task", "provider-actual", 30, "verified task cost"), ErrProductFundingPending)
	var child ProductRuntimeCall
	require.NoError(t, db.Where("parent_call_id = ?", "waiting-task").First(&child).Error)
	require.Equal(t, "reserve_pending", child.State)
	require.Equal(t, 10, child.TokenReserved)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 30, token.UsedQuota, "confirmed obligation retains original Key accounting while payment waits")
	ledger.rejectReserve = false
	// A definitely rejected central attempt can be retried only by the
	// central parent's recovery protocol. The product keeps the same debt.
	var req userkit.RuntimeReserveRequest
	require.NoError(t, common.UnmarshalJsonStr(child.ReserveJSON, &req))
	ledger.calls[child.ID] = userkit.RuntimeCall{RuntimeReserveRequest: req, ProductID: "llmrouter", InstanceID: "default", Status: "REJECTED"}
	require.ErrorIs(t, s.RecoverRefunds(ctx), ErrProductFundingPending)
	require.NoError(t, db.First(&child, "id = ?", child.ID).Error)
	require.Equal(t, "reserve_pending", child.State)
	require.Equal(t, 10, child.TokenReserved)
	delete(ledger.calls, child.ID) // Central authorizes a new posting attempt for the same immutable call.
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, s.RecoverRefunds(ctx))
	require.Equal(t, 470, ledger.balance)
	require.Equal(t, 2, ledger.settlements)
	var children int64
	require.NoError(t, db.Model(&ProductRuntimeCall{}).Where("parent_call_id = ?", "waiting-task").Count(&children).Error)
	require.EqualValues(t, 1, children)
}
