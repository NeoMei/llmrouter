package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestProductFundingDeploymentChangeCannotReplayMoneyIntoAnotherInstance(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	client, err := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: ledger.origin, ProductID: "llmrouter", InstanceID: "other", ServiceToken: strings.Repeat("t", 32)})
	require.NoError(t, err)
	other := NewProductFundingStore(db, client)
	_, err = other.Balance(ctx, 42)
	require.Error(t, err)
	_, err = other.Reserve(ctx, ProductFundingAdmission{CallID: "wrong-scope", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: "{}", Quota: 20})
	require.Error(t, err)
	require.Error(t, other.Import(ctx, 42, "payer", "reviewed"))
	require.Equal(t, 1, ledger.imports)
	var count int64
	require.NoError(t, db.Model(&ProductRuntimeCall{}).Count(&count).Error)
	require.Zero(t, count)
}
