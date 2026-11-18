package platformbilling

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const productGrantJSON = `{"credit_scope":"product_v1","source_verified":true,"funding_product_id":"agentwiki","id":"agentwiki-managed-key","audience":"llmrouter","credential_kind":"api_key","actor_id":"actor","account_id":"team-payer","origin_product_id":"agentwiki","instance_id":"default","asset":"CREDIT/6","funding_mode":"credits","model_scope":"llmrouter_available","pricing_source":"llmrouter_native_credit","credential_version":2,"native_credit_conversion":{"version":"product-credit-v1","numerator":"140","denominator":"1","rounding":"lr-cumulative-ceil-v1"}}`

func TestVerifiedProductCreditCanUseNativePricingForItsOwnProduct(t *testing.T) {
	var grant Claims
	require.NoError(t, common.Unmarshal([]byte(productGrantJSON), &grant))
	assert.True(t, grant.NativePricing())
	a := &Adapter{config: Config{InstanceID: "default"}}
	assert.ErrorIs(t, a.validateModelCredential("uh_key_test", "", "public-model", grant), ErrInvalid, "legacy operations cannot admit a product wallet key")
}

func TestProductCreditGrantRequiresRequestProof(t *testing.T) {
	var grant Claims
	require.NoError(t, common.Unmarshal([]byte(productGrantJSON), &grant))
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(productGrantJSON))
	}))
	t.Cleanup(hub.Close)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/product.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "native-service-token-at-least-32-bytes", InstanceID: "default", Group: "platform", NativeGroup: "native", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	_, err = a.ManagedModelGrant(context.Background(), "uh_key_test")
	assert.ErrorIs(t, err, ErrInvalid, "product grant requires the initiating product's proof on this request")
}

func TestProductCreditRetryAndReceiptUseVerifiedIdentityAndOriginalCallerKey(t *testing.T) {
	grantJSON := productGrantJSON
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		require.NoError(t, common.DecodeJson(r.Body, &request))
		assert.Equal(t, "product-proof", request["product_authorization"])
		if request["purpose"] == "receipt" {
			var historical Claims
			require.NoError(t, common.Unmarshal([]byte(grantJSON), &historical))
			historical.NativeCreditConversion = nil
			raw, err := common.Marshal(historical)
			require.NoError(t, err)
			_, _ = w.Write(raw)
			return
		}
		_, _ = w.Write([]byte(grantJSON))
	}))
	t.Cleanup(hub.Close)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/product.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "native-service-token-at-least-32-bytes", InstanceID: "default", Group: "platform", NativeGroup: "native", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	ctx, err := WithProductAuthorization(context.Background(), "product-proof", "caller-retry-1")
	require.NoError(t, err)
	grant, err := a.ManagedModelGrant(ctx, "uh_key_first")
	require.NoError(t, err)
	id, err := grant.ProductCreditCallID(ctx)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(id, "pc_"))
	provisionedAgain := grant
	provisionedAgain.ID = "different-durable-key"
	replayID, err := provisionedAgain.ProductCreditCallID(ctx)
	require.NoError(t, err)
	assert.Equal(t, id, replayID, "a replacement Key cannot re-dispatch the same logical request")
	wire := WireOperation{ID: "chat.completions.create"}
	body := []byte(`{"model":"public-model"}`)
	_, err = a.AdmitNative(ctx, "uh_key_first", "task_"+id, id, wire, "public-model", body)
	require.NoError(t, err)
	_, err = a.AdmitNative(ctx, "uh_key_token_reissued", "task_"+id, id, wire, "public-model", body)
	assert.ErrorIs(t, err, ErrDuplicate)
	_, err = a.AdmitNative(ctx, "uh_key_first", "task_"+id, id, wire, "public-model", []byte(`{"model":"public-model","stream":true}`))
	assert.ErrorIs(t, err, ErrConflict)
	readCtx, err := WithProductAuthorization(context.Background(), "product-proof", "")
	require.NoError(t, err)
	row, err := a.ManagedTask(readCtx, "uh_key_token_reissued", "caller-retry-1")
	require.NoError(t, err)
	assert.Equal(t, id, row.CallID)
	assert.Equal(t, "agentwiki", row.FundingProductID)
	assert.Equal(t, "team-payer", row.AccountID)
	receipt, err := a.Receipt(readCtx, "uh_key_token_reissued", row.TaskID, row.CallID)
	require.NoError(t, err)
	assert.Equal(t, ProductCreditScope, receipt.CreditScope)
	assert.Equal(t, "agentwiki", receipt.FundingProductID)
	grantJSON = strings.Replace(productGrantJSON, `"credential_version":2`, `"credential_version":3`, 1)
	_, err = a.AdmitNative(ctx, "uh_key_token_reissued", "task_"+id, id, wire, "public-model", body)
	assert.ErrorIs(t, err, ErrConflict)
	_, err = a.ManagedTask(readCtx, "uh_key_token_reissued", "caller-retry-1")
	assert.ErrorIs(t, err, ErrInvalid)
	grantJSON = strings.Replace(productGrantJSON, `"account_id":"team-payer"`, `"account_id":"different-payer"`, 1)
	_, err = a.ManagedTask(readCtx, "uh_key_token_reissued", "caller-retry-1")
	assert.ErrorIs(t, err, ErrInvalid)
}
