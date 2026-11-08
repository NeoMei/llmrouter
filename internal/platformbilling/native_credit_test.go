package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNativeCreditPricingPreservesManagedPayerAndSettlesFrozenConversion(t *testing.T) {
	grant := Claims{ID: "same-key", Audience: "llmrouter", CredentialKind: "api_key", ActorID: "actor", AccountID: "team-payer", OriginProductID: "superwagie", InstanceID: "default", Asset: "CREDIT/6", FundingMode: "credits", ModelScope: "llmrouter_available", PricingSource: "llmrouter_native_credit", CredentialVersion: 2}
	conversionJSON := []byte(`{"version":"test-points-v1","numerator":"5","denominator":"2","rounding":"lr-cumulative-ceil-v1"}`)
	raw, _ := common.Marshal(grant)
	var grantMap map[string]any
	require.NoError(t, common.Unmarshal(raw, &grantMap))
	var conversion any
	require.NoError(t, common.Unmarshal(conversionJSON, &conversion))
	grantMap["native_credit_conversion"] = conversion
	raw, _ = common.Marshal(grantMap)
	require.NoError(t, common.Unmarshal(raw, &grant))
	var remote RemoteCall
	var reserveCount, settleCount int
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			out = grant
		case "/internal/v1/calls/native-reserve":
			reserveCount++
			var in NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "uh_key_existing", in.Credential)
			assert.Equal(t, "new-model", in.Model)
			remote = RemoteCall{CredentialVersion: 2, TaskID: in.TaskID, CallID: in.CallID, CredentialID: grant.ID, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, InstanceID: grant.InstanceID, RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, Rate: Rate{Model: in.Model, Asset: "CREDIT/6", Semantics: "llmrouter_native"}, NativePricing: &in.Quote, Held: in.Quote.MaxAmount, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Status: "RESERVED"}
			out = remote
		case "/internal/v1/calls/call/native-settle":
			settleCount++
			var in NativeSettleRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "3", in.NativeQuota)
			assert.Equal(t, "8", in.Amount)
			assert.Equal(t, remote.NativePricing.PricingDigest, in.PricingDigest)
			remote.Status, remote.Charged = "SETTLED", in.Amount
			out = remote
		default:
			t.Errorf("unexpected service path %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		b, err := common.Marshal(out)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	defer hub.Close()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/native.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "native-service-token-at-least-32-bytes", InstanceID: "default", Group: "platform", NativeGroup: "default", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	a.SetNativeDispatchAuthorizer(func(context.Context, Claims, string, NativeQuote) error { return nil })
	s, err := a.AdmitNative(context.Background(), "uh_key_existing", "task", "call", WireOperation{ID: "chat.completions.create"}, "new-model", []byte(`{"model":"new-model","messages":[]}`))
	require.NoError(t, err)
	snapshot := `{"price_snapshot":"existing","conversion":{"version":"test-points-v1","numerator":"5","denominator":"2","rounding":"lr-cumulative-ceil-v1"}}`
	sum := sha256.Sum256([]byte(snapshot))
	quote := NativeQuote{PricingVersion: "lr-native-credit-v1", PricingDigest: hex.EncodeToString(sum[:]), BillingModel: "billing-model", WireOperation: "chat.completions.create", RouteID: "0", ChannelID: "1", Group: "default", NativeQuotaMax: "10", Conversion: NativeConversion{Version: "test-points-v1", Numerator: "5", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}, Asset: "CREDIT/6", MaxAmount: "25"}
	require.NoError(t, s.ReserveNative(context.Background(), quote, snapshot))
	require.NoError(t, s.Dispatch(context.Background(), 1))
	require.NoError(t, s.SettleNative(context.Background(), 3, nil))
	require.NoError(t, s.Finish(context.Background()))
	assert.Equal(t, 1, reserveCount)
	assert.Equal(t, 1, settleCount)
	var saved Call
	require.NoError(t, db.First(&saved, "call_id = ?", "call").Error)
	assert.Equal(t, "team-payer", saved.AccountID)
	assert.Equal(t, "same-key", saved.CredentialID)
	assert.Equal(t, "SETTLED", saved.Status)
	assert.Equal(t, "8", saved.Charged)
	assert.NotContains(t, saved.NativePriceSnapshot, "uh_key_")
}

func TestNativeCreditSettlementAboveHoldWaitsForBudgetedTopupAndRecovers(t *testing.T) {
	allowTopup, wrongReceipt := false, true
	var topups, settles int
	var remote RemoteCall
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/calls/call/topup":
			topups++
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "5", in["amount"])
			if !allowTopup {
				w.WriteHeader(402)
				return
			}
			remote.Held = "8"
			remote.Allocations = append(remote.Allocations, Allocation{BatchID: "second", Amount: "5"})
		case "/internal/v1/calls/call/native-settle":
			settles++
			assert.Equal(t, "8", remote.Held)
			var in NativeSettleRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "3", in.NativeQuota)
			assert.Equal(t, "8", in.Amount)
			remote.Status, remote.Charged = "SETTLED", "8"
			if wrongReceipt {
				remote.Charged = "9"
			}
		default:
			t.Errorf("unexpected recovery endpoint %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		b, e := common.Marshal(remote)
		require.NoError(t, e)
		_, _ = w.Write(b)
	}))
	defer hub.Close()
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/recovery.db"), &gorm.Config{})
	require.NoError(t, e)
	a, e := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "native-service-token-at-least-32-bytes", InstanceID: "default", Group: "platform", NativeGroup: "default", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, e)
	snapshot := `{"price_snapshot":"frozen","conversion":{"version":"test-points-v1","numerator":"5","denominator":"2","rounding":"lr-cumulative-ceil-v1"}}`
	sum := sha256.Sum256([]byte(snapshot))
	quote := NativeQuote{PricingVersion: "lr-native-credit-v1", PricingDigest: hex.EncodeToString(sum[:]), BillingModel: "model", WireOperation: "chat.completions.create", RouteID: "0", ChannelID: "1", Group: "default", NativeQuotaMax: "1", Conversion: NativeConversion{Version: "test-points-v1", Numerator: "5", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}, Asset: "CREDIT/6", MaxAmount: "3"}
	q, e := common.Marshal(quote)
	require.NoError(t, e)
	row := Call{CallID: "call", CredentialID: "key", CredentialVersion: 2, ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", InstanceID: "default", TaskID: "task", Model: "model", Operation: "chat", WireOperation: "chat.completions.create", PricingSource: "llmrouter_native_credit", NativePriceSnapshot: snapshot, NativeQuoteJSON: string(q), Digest: "request", Amount: "3", Asset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Status: "UNKNOWN"}
	require.NoError(t, db.Create(&row).Error)
	remote = RemoteCall{CredentialVersion: 2, TaskID: "task", CallID: "call", CredentialID: "key", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", InstanceID: "default", RequestDigest: "request", WireOperation: row.WireOperation, NativePricing: &quote, Rate: Rate{Model: "model", Asset: "CREDIT/6", Semantics: "llmrouter_native"}, Held: "3", ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Status: "RESERVED"}
	remote.Allocations = []Allocation{{BatchID: "first", Amount: "3"}}
	row.FundingSnapshot = remote.fundingSnapshot()
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("funding_snapshot", row.FundingSnapshot).Error)
	s := &Session{adapter: a, call: row}
	require.Error(t, s.SettleNative(context.Background(), 3, nil))
	var pending Call
	require.NoError(t, db.First(&pending, "call_id = ?", "call").Error)
	assert.Equal(t, "SETTLE_PENDING", pending.Status)
	assert.Empty(t, pending.Charged)
	assert.Zero(t, settles)
	allowTopup = true
	require.NoError(t, a.Recover(context.Background()))
	require.NoError(t, db.First(&pending, "call_id = ?", "call").Error)
	assert.Equal(t, "SETTLE_PENDING", pending.Status, "wrong receipt amount must not complete")
	assert.Empty(t, pending.Charged)
	wrongReceipt = false
	require.NoError(t, a.Recover(context.Background()))
	require.NoError(t, db.First(&pending, "call_id = ?", "call").Error)
	assert.Equal(t, "SETTLED", pending.Status)
	assert.Equal(t, "8", pending.Charged)
	assert.Equal(t, 2, topups)
	assert.Equal(t, 2, settles)
	require.NoError(t, a.Recover(context.Background()))
	assert.Equal(t, 2, settles)
	// A stored price mutation is not a legitimate recovery input.
	s.call.NativePriceSnapshot = `{"native_price":"changed"}`
	_, e = s.NativeQuote()
	require.Error(t, e)
}

func TestNativeCreditGrantAndQuoteRejectMissingOrCrossAssetConversion(t *testing.T) {
	conversion := NativeConversion{Version: "test-points-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
	valid := Claims{ID: "key", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", CredentialKind: "api_key", CredentialVersion: 1, Asset: "CREDIT/6", FundingMode: "credits", ModelScope: "llmrouter_available", PricingSource: NativeCreditPricingSource, NativeCreditConversion: &conversion}
	a := &Adapter{config: Config{InstanceID: "default"}}
	require.NoError(t, a.validateModelCredential("uh_key_test", "", "new-model", valid))
	for _, mutate := range []func(*Claims){
		func(c *Claims) { c.NativeCreditConversion = nil }, func(c *Claims) { c.Asset = "CNY/6" }, func(c *Claims) { c.FundingMode = "prepaid_cash" }, func(c *Claims) { c.PricingSource = "llmrouter_native" },
	} {
		grant := valid
		mutate(&grant)
		require.Error(t, a.validateModelCredential("uh_key_test", "", "new-model", grant))
	}
	q := NativeQuote{Asset: "CREDIT/6", Conversion: conversion}
	for _, tc := range []struct {
		quota  int
		amount string
	}{{0, "0"}, {1, "2"}, {3, "5"}, {4, "6"}} {
		amount, e := q.Amount(tc.quota)
		require.NoError(t, e)
		require.Equal(t, tc.amount, amount)
	}
	q.Conversion.Numerator = "9223372036854775807"
	_, e := q.Amount(3)
	require.Error(t, e)
	q.Asset = "CNY/6"
	_, e = q.Amount(1)
	require.Error(t, e)
}
