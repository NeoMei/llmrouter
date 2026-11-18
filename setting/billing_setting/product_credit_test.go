package billing_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestProductCreditPricingCanAdjustMultiplierAndKeepPublishedOverride(t *testing.T) {
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
	published, ok := ResolveProductCreditPrice("test-model", "embedding", "7")
	require.True(t, ok)
	assert.Equal(t, "published_override", published.Source)
	assert.Equal(t, override, published.Tariff)
	assert.NotEqual(t, derived.Revision, published.Revision)
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
