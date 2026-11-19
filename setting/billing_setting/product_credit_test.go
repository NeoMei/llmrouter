package billing_setting

import (
	"math/big"
	"testing"

	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixedPublicCNYPriceIsIndependentOfExchangeRateAndMultiplier(t *testing.T) {
	previous := ProductCreditPricingJSONString()
	t.Cleanup(func() { require.NoError(t, SetProductCreditPricing(previous)) })
	require.NoError(t, SetProductCreditPricing(`{
	  "version":"paddle-public-1","multiplier":{"numerator":"9","denominator":"2"},
	  "fixed_public_cny":{"PaddleOCR-VL-1.6":{"ocr":{"version":"metered_v2","operation":"ocr","policy":"success_request",
	    "terms":[{"key":"requests","unit":"request","price":{"numerator":"1","denominator":"20"}}]}}},
	  "suppliers":{"PaddleOCR-VL-1.6":{"ocr":{"paddle":{"version":"paddle-free","currency":"CNY",
	    "tariff":{"version":"metered_v2","operation":"ocr","policy":"success_request",
	      "terms":[{"key":"requests","unit":"request","price":{"numerator":"0","denominator":"1"}}]}}}}}
	}`))
	for _, fx := range []string{"1", "7.3", "7.65813"} {
		quote, ok := ResolveProductCreditPrice("PaddleOCR-VL-1.6", "ocr", fx)
		require.True(t, ok)
		assert.Equal(t, "fixed_public_cny", quote.Source)
		assert.Equal(t, "paddle-public-1", quote.PolicyVersion)
		assert.Empty(t, quote.SupplierTariffs)
		assert.Empty(t, quote.EvidenceDigest)
		assert.NotEmpty(t, quote.Revision)
		rate := quote.Tariff.Terms[0].Price
		usd, ok := new(big.Rat).SetString(rate.Numerator + "/" + rate.Denominator)
		require.True(t, ok)
		credits, ok := new(big.Rat).SetString(quote.MicrocreditsPerUSD())
		require.True(t, ok)
		assert.Equal(t, "5000000", usd.Mul(usd, credits).RatString())
	}
	assert.Contains(t, ProductCreditPricingJSONString(), `"fixed_public_cny"`)
}

func TestProductCreditDefaultPriceUsesMaximumComparablePayAsYouGoPerDimension(t *testing.T) {
	previous := ProductCreditPricingJSONString()
	t.Cleanup(func() { require.NoError(t, SetProductCreditPricing(previous)) })
	require.NoError(t, SetProductCreditPricing(`{
	  "version":"public-price-rev1",
	  "suppliers":{"public-model":{"chat":{
	    "a":{"version":"a-1","currency":"USD","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	      {"key":"input","unit":"token","price":{"numerator":"2","denominator":"1000000"}},
	      {"key":"output","unit":"token","price":{"numerator":"8","denominator":"1000000"}}]}},
	    "b":{"version":"b-1","currency":"CNY","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	      {"key":"output","unit":"token","price":{"numerator":"32","denominator":"1000000"}},
	      {"key":"input","unit":"token","price":{"numerator":"24","denominator":"1000000"}}]}}
	  }}}
	}`))
	quote, ok := ResolveProductCreditPrice("public-model", "chat", "8")
	require.True(t, ok)
	require.Len(t, quote.Tariff.Terms, 2)
	assert.Equal(t, "supplier_paygo_max", quote.Source)
	assert.Equal(t, metering.Rational{Numerator: "3", Denominator: "2"}, quote.Multiplier)
	assert.Equal(t, "input", quote.Tariff.Terms[0].Key)
	assert.Equal(t, metering.Rational{Numerator: "9", Denominator: "2000000"}, quote.Tariff.Terms[0].Price)
	assert.Equal(t, "output", quote.Tariff.Terms[1].Key)
	assert.Equal(t, metering.Rational{Numerator: "3", Denominator: "250000"}, quote.Tariff.Terms[1].Price)
	assert.NotEmpty(t, quote.Revision)

	raw := ProductCreditPricingJSONString()
	require.NoError(t, SetProductCreditPricing(raw))
	again, ok := ResolveProductCreditPrice("public-model", "chat", "8")
	require.True(t, ok)
	assert.Equal(t, quote, again)
	_, ok = ResolveProductCreditPrice("supplier-billing-model", "chat", "8")
	assert.False(t, ok)
}

func TestProductCreditPricingAppliesMultiplierWithoutChangingLegacyPrice(t *testing.T) {
	previous := ProductCreditPricingJSONString()
	oldPrices := GetModelNativePricingCopy()
	t.Cleanup(func() {
		require.NoError(t, SetProductCreditPricing(previous))
		require.NoError(t, SetModelNativePricing(oldPrices))
	})
	require.NoError(t, SetProductCreditPricing(`{"version":"rev2","multiplier":{"numerator":"11","denominator":"5"},"suppliers":{"test-model":{"embedding":{
	  "a":{"version":"a-1","currency":"USD","tariff":{"version":"metered_v2","operation":"embedding","policy":"provider_reported_v1","terms":[
	    {"key":"input","unit":"token","price":{"numerator":"1","denominator":"1000000"}}]}}
	}}}}`))
	derived, ok := ResolveProductCreditPrice("test-model", "embedding", "7")
	require.True(t, ok)
	assert.Equal(t, metering.Rational{Numerator: "11", Denominator: "5000000"}, derived.Tariff.Terms[0].Price)
	override := derived.Tariff
	override.Terms = []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "4", Denominator: "1000000"}}}
	require.NoError(t, SetModelNativePricing(map[string]map[string]metering.Price{"test-model": {"embedding": override}}))
	configured, ok := ResolveProductCreditPrice("test-model", "embedding", "7")
	require.True(t, ok)
	assert.Equal(t, derived, configured)
	legacy, ok := ResolveModelNativePrice("test-model", "embedding")
	require.True(t, ok)
	assert.Equal(t, override, legacy)
	require.NoError(t, SetProductCreditPricing(`{"version":"no-supplier"}`))
	fallback, ok := ResolveProductCreditPrice("test-model", "embedding", "7")
	require.True(t, ok)
	assert.Equal(t, "published_override", fallback.Source)
	assert.Equal(t, override, fallback.Tariff)
	assert.NotEqual(t, derived.Revision, fallback.Revision)
	assert.Error(t, SetProductCreditPricing(`{"version":"rev3","multiplier":{"numerator":"0","denominator":"1"}}`))
}

func TestProductCreditRejectsIncomparableSupplierMeasurements(t *testing.T) {
	assert.Error(t, ValidateProductCreditPricing(`{
	  "version":"invalid",
	  "suppliers":{"public-model":{"chat":{
	    "a":{"version":"a-1","currency":"USD","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	      {"key":"input","unit":"token","price":{"numerator":"1","denominator":"1000000"}}]}},
	    "b":{"version":"b-1","currency":"USD","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	      {"key":"output","unit":"token","price":{"numerator":"1","denominator":"1000000"}}]}}
	  }}}
	}`))
}

func TestProductCreditVideoSupplierRatesKeepResolutionDimensions(t *testing.T) {
	previous := ProductCreditPricingJSONString()
	t.Cleanup(func() { require.NoError(t, SetProductCreditPricing(previous)) })
	require.NoError(t, SetProductCreditPricing(`{
	  "version":"video-revision",
	  "suppliers":{"priced-video":{"video":{
	    "a":{"version":"a-1","currency":"CNY","tariff":{"version":"metered_v2","operation":"video","policy":"ali_wan26_t2v_billable_duration_v1","terms":[
	      {"key":"video_720p","unit":"video_second","price":{"numerator":"4","denominator":"5"}},
	      {"key":"video_1080p","unit":"video_second","price":{"numerator":"3","denominator":"2"}}]}}
	  }}}
	}`))
	price, ok := ResolveProductCreditPrice("priced-video", "video", "8")
	require.True(t, ok)
	rates := map[string]metering.Rational{}
	for _, term := range price.Tariff.Terms {
		rates[term.Key] = term.Price
	}
	assert.Equal(t, metering.Rational{Numerator: "3", Denominator: "20"}, rates["video_720p"])
	assert.Equal(t, metering.Rational{Numerator: "9", Denominator: "32"}, rates["video_1080p"])
}

func TestProductCreditInputBandsPickCurrencyNormalizedMaximaWithinEachBand(t *testing.T) {
	previous := ProductCreditPricingJSONString()
	t.Cleanup(func() { require.NoError(t, SetProductCreditPricing(previous)) })
	require.NoError(t, SetProductCreditPricing(`{
	  "version":"band-rev",
	  "suppliers":{"banded-model":{"chat":{
	    "usd":{"version":"usd-1","currency":"USD","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	      {"key":"input","unit":"token","price":{"numerator":"2","denominator":"1000000"}},
	      {"key":"output","unit":"token","price":{"numerator":"8","denominator":"1000000"}}],"input_token_bands":[
	      {"max_input_tokens":100,"prices":{"input":{"numerator":"2","denominator":"1000000"},"output":{"numerator":"8","denominator":"1000000"}}},
	      {"max_input_tokens":200,"prices":{"input":{"numerator":"4","denominator":"1000000"},"output":{"numerator":"16","denominator":"1000000"}}}]}},
	    "cny":{"version":"cny-1","currency":"CNY","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	      {"key":"input","unit":"token","price":{"numerator":"24","denominator":"1000000"}},
	      {"key":"output","unit":"token","price":{"numerator":"32","denominator":"1000000"}}],"input_token_bands":[
	      {"max_input_tokens":100,"prices":{"input":{"numerator":"24","denominator":"1000000"},"output":{"numerator":"32","denominator":"1000000"}}},
	      {"max_input_tokens":200,"prices":{"input":{"numerator":"80","denominator":"1000000"},"output":{"numerator":"96","denominator":"1000000"}}}]}}
	  }}}
	}`))
	quote, ok := ResolveProductCreditPrice("banded-model", "chat", "8")
	require.True(t, ok)
	require.Len(t, quote.Tariff.InputTokenBands, 2)
	assert.Equal(t, metering.Rational{Numerator: "9", Denominator: "2000000"}, quote.Tariff.InputTokenBands[0].Prices["input"])
	assert.Equal(t, metering.Rational{Numerator: "3", Denominator: "250000"}, quote.Tariff.InputTokenBands[0].Prices["output"])
	assert.Equal(t, metering.Rational{Numerator: "3", Denominator: "200000"}, quote.Tariff.InputTokenBands[1].Prices["input"])
	assert.Equal(t, metering.Rational{Numerator: "3", Denominator: "125000"}, quote.Tariff.InputTokenBands[1].Prices["output"])
	quote.Tariff.InputTokenBands[1].Prices["output"] = metering.Rational{Numerator: "1", Denominator: "1"}
	replayed, ok := ResolveProductCreditPrice("banded-model", "chat", "8")
	require.True(t, ok)
	assert.Equal(t, metering.Rational{Numerator: "3", Denominator: "125000"}, replayed.Tariff.InputTokenBands[1].Prices["output"])
}
