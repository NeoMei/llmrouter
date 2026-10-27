package model

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestProductFinancialCommitmentSurvivesUnobservedDispatchLeaseExpiry(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "purchase", UserID: 42, TokenID: 0, Model: "subscription", Operation: "purchase", PriceSnapshot: "{}", Quota: 20, FinancialCommitment: true})
	require.NoError(t, err)
	require.NoError(t, db.Model(&ProductRuntimeCall{}).Where("id = ?", "purchase").Update("dispatch_lease_until", time.Now().Unix()-1).Error)
	require.NoError(t, s.Recover(ctx))
	require.Equal(t, 480, ledger.balance)
	require.Equal(t, 20, ledger.held)
	require.NoError(t, s.Settle(ctx, "purchase", 20, "{}"))
	require.Equal(t, 20, ledger.consumed)
}
