package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func subscriptionPaymentFixture(t *testing.T) *runtimeLedgerFixture {
	t.Helper()
	paymentParityFixture(t)
	require.NoError(t, DB.AutoMigrate(&ProductSubscriptionPurchase{}, &ProductRuntimeCall{}, &ProductFundingTopUp{}, &ProductLegacyFundingLease{}))
	oldUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 100
	t.Cleanup(func() { common.QuotaPerUnit = oldUnit })
	plan := &SubscriptionPlan{Id: 901, Title: "Original subscription", PriceAmount: 2, Currency: "USD", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true, TotalAmount: 1000, MaxPurchasePerUser: 1}
	require.NoError(t, DB.Create(plan).Error)
	ledger := &runtimeLedgerFixture{balance: 500, calls: map[string]userkit.RuntimeCall{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/product-runtime/calls/reserve" {
			var in userkit.RuntimeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			call, exists := ledger.calls[in.CallID]
			if !exists {
				amount, _ := strconv.Atoi(in.Amount)
				ledger.balance -= amount
				ledger.held += amount
				call = userkit.RuntimeCall{RuntimeReserveRequest: in, ProductID: "llmrouter", InstanceID: "default", ActorID: "central-user", Held: in.Amount, Charged: "0", Status: "RESERVED"}
				ledger.calls[in.CallID] = call
			}
			raw, _ := common.Marshal(call)
			_, _ = w.Write(raw)
			return
		}
		ledger.serve(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	scope, err := ProductRuntimeScopeDigest()
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ProductFundingAccount{}).Where("user_id = ?", 42).Update("scope_digest", scope).Error)
	return ledger
}

func TestProductSubscriptionBalancePurchaseUsesCentralMoneyAndOriginalEntitlement(t *testing.T) {
	ledger := subscriptionPaymentFixture(t)
	require.NoError(t, PurchaseSubscriptionWithBalance(42, 901, "purchase-one"))
	require.NoError(t, PurchaseSubscriptionWithBalance(42, 901, "purchase-one"))
	require.Equal(t, 1, ledger.settlements)
	require.Equal(t, 300, ledger.balance)
	var subs []UserSubscription
	require.NoError(t, DB.Where("user_id = ?", 42).Find(&subs).Error)
	require.Len(t, subs, 1)
	require.Equal(t, int64(1000), subs[0].AmountTotal)
	var user User
	require.NoError(t, DB.First(&user, 42).Error)
	require.Zero(t, user.Quota)
	require.Zero(t, user.UsedQuota)
	require.Zero(t, user.RequestCount)
	var orders []SubscriptionOrder
	require.NoError(t, DB.Where("user_id = ?", 42).Find(&orders).Error)
	require.Len(t, orders, 1)
	require.Equal(t, common.TopUpStatusSuccess, orders[0].Status)
}

func TestProductSubscriptionPaidIntentRecoversSQLFailureWithoutChargingTwice(t *testing.T) {
	ledger := subscriptionPaymentFixture(t)
	require.NoError(t, DB.Callback().Create().Before("gorm:create").Register("parity_subscription_failure", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*UserSubscription); ok {
			tx.AddError(gorm.ErrInvalidTransaction)
		}
	}))
	require.Error(t, PurchaseSubscriptionWithBalance(42, 901, "purchase-recover"))
	require.Equal(t, 1, ledger.settlements)
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", 42).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, DB.Callback().Create().Remove("parity_subscription_failure"))
	require.NoError(t, RecoverProductSubscriptionPurchases(context.Background()))
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", 42).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.Equal(t, 1, ledger.settlements)
	require.Equal(t, 300, ledger.balance)
	require.NoError(t, PurchaseSubscriptionWithBalance(42, 901, "purchase-recover"))
	require.Error(t, PurchaseSubscriptionWithBalance(42, 901, "purchase-another"))
	require.Equal(t, 1, ledger.settlements)
}

func TestProductSubscriptionPendingPurchaseReservesOriginalPlanLimit(t *testing.T) {
	subscriptionPaymentFixture(t)
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	scope, err := ProductRuntimeScopeDigest()
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ProductFundingAccount{}).Where("user_id = ?", 42).Update("scope_digest", scope).Error)
	require.Error(t, PurchaseSubscriptionWithBalance(42, 901, "pending-one"))
	var plan SubscriptionPlan
	require.NoError(t, DB.First(&plan, 901).Error)
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, err := CreateUserSubscriptionFromPlanTx(tx, 42, &plan, "admin")
		return err
	})
	require.Error(t, err)
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", 42).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductSubscriptionRecoveryDoesNotStarveNewPaidWorkBehindBadIntents(t *testing.T) {
	ledger := subscriptionPaymentFixture(t)
	var plan SubscriptionPlan
	require.NoError(t, DB.First(&plan, 901).Error)
	plan.MaxPurchasePerUser = 0
	raw, err := common.Marshal(plan)
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		require.NoError(t, DB.Create(&ProductSubscriptionPurchase{ID: fmt.Sprintf("bad-%03d", i), UserID: 42, PlanID: 901, AccountID: "payer", State: "pending", PlanSnapshot: "{", RequiredQuota: 200, CreatedAt: 1}).Error)
	}
	require.NoError(t, DB.Create(&ProductSubscriptionPurchase{ID: "valid-after-old-failures", UserID: 42, PlanID: 901, AccountID: "payer", State: "pending", PlanSnapshot: string(raw), RequiredQuota: 200, CreatedAt: 2}).Error)
	require.Error(t, RecoverProductSubscriptionPurchases(context.Background()))
	_ = RecoverProductSubscriptionPurchases(context.Background())
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", 42).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.Equal(t, 1, ledger.settlements)
}
