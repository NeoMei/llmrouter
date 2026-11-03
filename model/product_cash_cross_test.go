package model

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The companion UniHub test supplies real HTTP authority, PostgreSQL and the
// final Formance ledger. No central financial stub is used in this acceptance.
func TestCrossProductUnifiedCashAcceptance(t *testing.T) {
	origin := os.Getenv("UNIHUB_CROSS_CASH_ORIGIN")
	if origin == "" {
		t.Skip("run through UniHub TestCrossRepositoryUnifiedCashFunding")
	}
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	t.Setenv("UNIHUB_ORIGIN", origin)
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("c", 32))
	oldRate, oldUnit := operation_setting.USDExchangeRate, common.QuotaPerUnit
	operation_setting.USDExchangeRate, common.QuotaPerUnit = 7.3, 500000
	t.Cleanup(func() { operation_setting.USDExchangeRate, common.QuotaPerUnit = oldRate, oldUnit })
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/product.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &ProductCreditAward{}))
	require.NoError(t, RegisterProductFundingModels(db))
	require.NoError(t, RegisterProductRefundModels(db))
	require.NoError(t, db.Create(&User{Id: 42, Username: "cash-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&Token{Id: 7, UserId: 42, Key: "original-product-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 200}).Error)
	client, err := platformaccount.RuntimeClient()
	require.NoError(t, err)
	ctx := context.Background()
	access, err := client.Access(ctx, "42")
	require.NoError(t, err)
	store := NewProductFundingStore(db, client)
	require.NoError(t, store.ActivateZero(ctx, 42, access.Binding.PersonalAccountID))
	_, err = store.Reserve(ctx, ProductFundingAdmission{CallID: "cross-shared-cash", UserID: 42, TokenID: 7, Model: "lr-model-outside-central-catalog", Operation: "original-product-operation", PriceSnapshot: `{"llmrouter_price":"original"}`, Quota: 60})
	require.NoError(t, err)
	require.NoError(t, store.MarkDispatched(ctx, "cross-shared-cash"))
	// The current display/exchange configuration changes after admission. Actual
	// usage must retain the 73/5 micro-CNY per quota frozen in the call.
	operation_setting.USDExchangeRate = 100
	for range 2 {
		require.NoError(t, store.Settle(ctx, "cross-shared-cash", 37, `{"product_quota":37}`))
	}
	balance, err := store.CashBalance(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, "9459", balance.Available)
	require.Equal(t, "541", balance.Consumed)
	for range 2 {
		require.NoError(t, store.Refund(ctx, "cross-shared-cash", "partial", 7, "product adjustment"))
	}
	require.NoError(t, store.Refund(ctx, "cross-shared-cash", "remainder", 30, "full product refund"))
	balance, err = store.CashBalance(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, "10000", balance.Available)
	require.Equal(t, "0", balance.Consumed)
	operation_setting.USDExchangeRate = 7.3
	_, err = store.Reserve(ctx, ProductFundingAdmission{CallID: "cross-cash-task", UserID: 42, TokenID: 7, Model: "lr-async-task", Operation: "task", PriceSnapshot: `{"llmrouter_price":"task"}`, Quota: 1})
	require.NoError(t, err)
	require.NoError(t, store.MarkDispatched(ctx, "cross-cash-task"))
	require.NoError(t, store.Settle(ctx, "cross-cash-task", 1, `{"product_quota":1}`))
	for _, change := range []struct {
		key   string
		quota int
		cash  string
	}{
		{"increase", 3, "9956"},
		{"decrease", 1, "9985"},
		{"increase-again", 3, "9956"},
		{"final-refund", 0, "10000"},
	} {
		for range 2 {
			require.NoError(t, store.Reprice(ctx, "cross-cash-task", change.key, change.quota, "final task measurement"))
		}
		balance, err = store.CashBalance(ctx, 42)
		require.NoError(t, err)
		require.Equal(t, change.cash, balance.Available)
	}
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, "original-product-key", token.Key)
	require.Equal(t, 200, token.RemainQuota)
	require.Zero(t, token.UsedQuota)
}
