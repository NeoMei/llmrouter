package router

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicProductTariffConvertsEachBandWithoutMutatingFrozenPrice(t *testing.T) {
	term := metering.Rational{Numerator: "1", Denominator: "7"}
	price := billing_setting.ProductCreditPrice{CNYPerUSD: "7", Revision: "frozen-fixture", Tariff: metering.Price{
		InputTokenBands: []metering.InputTokenBand{{MaxInputTokens: 100, Prices: map[string]metering.Rational{"input": term}}},
	}}
	public := publicProductCreditTariff(price)
	require.Equal(t, "CREDIT/6", public.Asset)
	assert.Equal(t, "frozen-fixture", public.Revision)
	assert.EqualValues(t, 100, public.Tariff.InputTokenBands[0].MaxInputTokens)
	assert.Equal(t, metering.Rational{Numerator: "100000000", Denominator: "1"}, public.Tariff.InputTokenBands[0].Prices["input"])
	assert.Equal(t, term, price.Tariff.InputTokenBands[0].Prices["input"])
}

func TestBandedTariffIsHiddenWithoutItsCertifiedRoute(t *testing.T) {
	previous := billing_setting.ProductCreditPricingJSONString()
	t.Cleanup(func() { require.NoError(t, billing_setting.SetProductCreditPricing(previous)) })
	require.NoError(t, billing_setting.SetProductCreditPricing(`{"version":"catalog-band","suppliers":{"banded-model":{"chat":{"a":{"version":"a-1","currency":"CNY","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[{"key":"input","unit":"token","price":{"numerator":"2","denominator":"1000000"}},{"key":"output","unit":"token","price":{"numerator":"8","denominator":"1000000"}}],"input_token_bands":[{"max_input_tokens":100,"prices":{"input":{"numerator":"2","denominator":"1000000"},"output":{"numerator":"8","denominator":"1000000"}}}]}}}}}}`))
	wire := "chat.completions.create"
	projected := ProviderCatalogModel{ID: "banded-model", CapabilityMetadata: platformbilling.CatalogModel{Operations: []platformbilling.CatalogOperation{{
		ID: wire, BillingOperation: "chat", Available: true, Executable: true,
	}}}}
	candidate := modelcatalog.ResolvedModel{ChannelID: 15, UpstreamModel: "upstream"}
	candidate.Capabilities.Operations = []string{wire}
	channels := map[int]model.Channel{15: {Id: 15, Type: constant.ChannelTypeOpenAI}}
	item := productCreditPriceAvailability(projected, []modelcatalog.ResolvedModel{candidate}, channels, nil)
	op := item.CapabilityMetadata.Operations[0]
	assert.False(t, op.Executable)
	assert.Nil(t, op.PublicTariff)
}
