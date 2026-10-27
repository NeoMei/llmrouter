package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductAbsoluteCreditSetReplaysFrozenCASAfterLostReply(t *testing.T) {
	_, db, ledger := productFundingFixture(t)
	var first string
	applied := 0
	loseReply := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/product-runtime/credit-adjustments" {
			ledger.serve(w, r)
			return
		}
		var in userkit.RuntimeAdjustmentRequest
		require.NoError(t, common.DecodeJson(r.Body, &in))
		raw, _ := common.Marshal(in)
		if first == "" {
			first = string(raw)
			require.Equal(t, "500", in.ExpectedAvailable)
			require.NotNil(t, in.SetAvailable)
			ledger.balance = 400
			applied++
		} else if first != string(raw) {
			w.WriteHeader(409)
			return
		}
		if loseReply {
			loseReply = false
			w.WriteHeader(503)
			return
		}
		out, _ := common.Marshal(userkit.RuntimeAdjustment{ID: "set-receipt", ProductID: "llmrouter", InstanceID: "default", AccountID: "payer", Amount: "-100", Status: "APPLIED"})
		_, _ = w.Write(out)
	}))
	t.Cleanup(server.Close)
	client, err := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: server.URL, ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32)})
	require.NoError(t, err)
	s := NewProductFundingStore(db, client)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&ProductCreditAdjustment{}))
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, err = s.Adjust(ctx, "actor", 1, 42, "same-operation", "override", 400)
	require.Error(t, err)
	_, err = s.Adjust(ctx, "fresh-actor", 1, 42, "same-operation", "override", 400)
	require.NoError(t, err)
	require.Equal(t, 1, applied)
	require.Equal(t, 400, ledger.balance)
	_, err = s.Adjust(ctx, "fresh-actor", 1, 42, "same-operation", "override", 450)
	require.Error(t, err)
}
