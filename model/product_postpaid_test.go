package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductFinalUsageRetainsOriginalPostpaidKeyAccounting(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Update("remain_quota", 25).Error)
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "confirmed-actual", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"ratio":1}`, Quota: 20})
	require.NoError(t, err)
	require.NoError(t, s.MarkDispatched(ctx, "confirmed-actual"))
	require.NoError(t, s.Settle(ctx, "confirmed-actual", 40, `{"verified_actual":40}`))
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, -15, token.RemainQuota)
	require.Equal(t, 40, token.UsedQuota)
	require.Equal(t, 460, ledger.balance)
	_, err = s.Reserve(ctx, ProductFundingAdmission{CallID: "next-call", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"ratio":1}`, Quota: 1})
	require.Error(t, err)
}
