package service

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedNativeCreditPriceUsesVerifiedConversionWithoutCashRate(t *testing.T) {
	old := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 0
	t.Cleanup(func() { operation_setting.USDExchangeRate = old })
	conversion := platformbilling.NativeConversion{Version: "test-points-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
	grant := platformbilling.Claims{ID: "key", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", CredentialKind: "api_key", CredentialVersion: 1, Asset: "CREDIT/6", FundingMode: "credits", ModelScope: "llmrouter_available", PricingSource: platformbilling.NativeCreditPricingSource, NativeCreditConversion: &conversion}
	var quote platformbilling.NativeQuote
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			out = grant
		case "/internal/v1/calls/native-reserve":
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			quote = in.Quote
			out = platformbilling.RemoteCall{CallID: in.CallID, CredentialID: grant.ID, CredentialVersion: 1, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, InstanceID: grant.InstanceID, TaskID: in.TaskID, RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, Rate: platformbilling.Rate{Model: in.Model, Asset: "CREDIT/6", Semantics: "llmrouter_native"}, NativePricing: &in.Quote, Held: in.Quote.MaxAmount, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Status: "RESERVED"}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		data, e := common.Marshal(out)
		require.NoError(t, e)
		_, _ = w.Write(data)
	}))
	defer hub.Close()
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/credit.db"), &gorm.Config{})
	require.NoError(t, e)
	adapter, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("s", 32), InstanceID: "default", Group: "legacy", NativeGroup: "native", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, e)
	session, e := adapter.AdmitNative(context.Background(), "uh_key_test", "task", "call", platformbilling.WireOperation{ID: "chat.completions.create"}, "model", []byte(`{"model":"model"}`))
	require.NoError(t, e)
	require.NoError(t, ReserveManagedNativePrice(context.Background(), session, "model", "native", 0, 1, 3, []byte(`{"price":"fixture"}`)))
	require.Equal(t, "CREDIT/6", quote.Asset)
	require.Equal(t, "5", quote.MaxAmount)
	require.Equal(t, conversion, quote.Conversion)
	require.Equal(t, platformbilling.NativeCreditPricingVersion, quote.PricingVersion)
	frozen, e := session.NativeQuote()
	require.NoError(t, e)
	require.Equal(t, quote, frozen)
}
