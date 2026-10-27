package model

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFinalTopUpKeepsObservedKeyUsagePendingAfterKnownRefusal(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "actual-cost", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{}`, Quota: 20})
	require.NoError(t, e)
	require.NoError(t, s.MarkDispatched(ctx, "actual-cost"))
	ledger.rejectTopUp = true
	require.Error(t, s.Settle(ctx, "actual-cost", 40, `{"actual":40}`))
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 160, token.RemainQuota)
	require.Equal(t, 40, token.UsedQuota)
	var intents []ProductFundingTopUp
	require.NoError(t, db.Find(&intents).Error)
	require.Len(t, intents, 1)
	require.True(t, intents[0].AcceptedCall)
	require.Equal(t, "pending", intents[0].State)
	ledger.rejectTopUp = false
	require.NoError(t, s.Recover(ctx))
	require.NoError(t, s.Recover(ctx))
	require.Equal(t, 460, ledger.balance)
	require.Equal(t, 40, ledger.consumed)
	require.Equal(t, 1, ledger.settlements)
	var after []ProductFundingTopUp
	require.NoError(t, db.Find(&after).Error)
	require.Len(t, after, 1)
	require.Equal(t, intents[0].ID, after[0].ID)
	require.Equal(t, "applied", after[0].State)
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 160, token.RemainQuota)
	require.Equal(t, 40, token.UsedQuota)
}
