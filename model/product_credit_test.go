package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProductCreditPaymentUsesOriginalReceiptAndNeverCreditsLocalWallet(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	awards := map[string]userkit.RuntimeAdjustment{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/product-runtime/awards" {
			ledger.serve(w, r)
			return
		}
		var in userkit.RuntimeAwardRequest
		_ = common.DecodeJson(r.Body, &in)
		out, found := awards[in.ReceiptID]
		if !found {
			n, _ := strconv.Atoi(in.Amount)
			ledger.balance += n
			out = userkit.RuntimeAdjustment{ID: in.ReceiptID, ProductID: "llmrouter", InstanceID: "default", AccountID: in.AccountID, Amount: in.Amount, Status: "APPLIED"}
			awards[in.ReceiptID] = out
		}
		data, _ := common.Marshal(out)
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)

	require.NoError(t, db.AutoMigrate(&ProductCreditAward{}))
	client, e := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: server.URL, ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32)})
	require.NoError(t, e)
	s = NewProductFundingStore(db, client)
	require.NoError(t, s.Import(ctx, 42, "payer", "approved-batch"))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	queue := func(amount int) error {
		return db.Transaction(func(tx *gorm.DB) error {
			handled, err := QueueProductCreditAward(tx, 42, "payment", "paid-order-1", amount, map[string]any{"order": "paid-order-1", "verified": true})
			require.True(t, handled)
			return err
		})
	}
	require.NoError(t, queue(60))
	require.NoError(t, FlushProductCreditAwards(ctx, 42))
	require.NoError(t, queue(60))
	require.NoError(t, FlushProductCreditAwards(ctx, 42))
	require.Equal(t, 560, ledger.balance)
	require.Equal(t, 1, len(awards))
	require.Error(t, queue(70))
	var u User
	require.NoError(t, db.First(&u, 42).Error)
	require.Zero(t, u.Quota)
}
