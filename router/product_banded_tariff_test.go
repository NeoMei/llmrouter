package router

import (
	"context"
	"strings"
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

func TestBandedChatCatalogUsesExactOpenAIAbilityTarget(t *testing.T) {
	_, db := providerCatalogFixture(t)
	previous := billing_setting.ProductCreditPricingJSONString()
	t.Cleanup(func() { require.NoError(t, billing_setting.SetProductCreditPricing(previous)) })
	require.NoError(t, billing_setting.SetProductCreditPricing(`{"version":"catalog-band","suppliers":{"ability-chat":{"chat":{"a":{"version":"a-1","currency":"CNY","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[{"key":"input","unit":"token","price":{"numerator":"2","denominator":"1000000"}},{"key":"output","unit":"token","price":{"numerator":"8","denominator":"1000000"}}],"input_token_bands":[{"max_input_tokens":1000,"prices":{"input":{"numerator":"2","denominator":"1000000"},"output":{"numerator":"8","denominator":"1000000"}}}]}}}}}}`))
	const wire = "chat.completions.create"
	const endpoint = "https://coding.dashscope.aliyuncs.com"
	config := platformbilling.Config{
		Enabled: true, Origin: "https://billing.example.test", ServiceToken: strings.Repeat("s", 32), InstanceID: "fixture",
		PrincipalID: 1, Group: "legacy", NativeGroup: "native", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100,
		ProductCreditTextInputPolicies: map[string]map[string]platformbilling.TextInputPolicy{wire: {"ability-chat": {
			Version: "full_body_bytes_v1", ChannelID: 1, ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "certified-model",
			Endpoint: endpoint + "/v1/chat/completions", MaxInputTokens: 1000, ContextTokens: 1100, MaxOutputTokens: 100,
			ProviderReference: "https://help.aliyun.com/zh/model-studio/coding-plan",
		}}},
	}
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "ability-chat", ChannelId: 1, Enabled: true}).Error)
	grant := platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"}
	for _, tt := range []struct {
		name, mapping, origin, certifiedUpstream string
		executable                               bool
	}{
		{"exact ability", `{"ability-chat":"certified-model"}`, endpoint, "certified-model", true},
		{"identity", "", endpoint, "ability-chat", true},
		{"mapping chain", `{"ability-chat":"alias","alias":"certified-model"}`, endpoint, "certified-model", true},
		{"different upstream", `{"ability-chat":"different-model"}`, endpoint, "certified-model", false},
		{"different endpoint", `{"ability-chat":"certified-model"}`, "https://other.example.test", "certified-model", false},
		{"mapping cycle", `{"ability-chat":"alias","alias":"ability-chat"}`, endpoint, "certified-model", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Updates(map[string]any{"base_url": tt.origin, "model_mapping": tt.mapping}).Error)
			policies := config.ProductCreditTextInputPolicies[wire]
			policy := policies["ability-chat"]
			policy.UpstreamModel = tt.certifiedUpstream
			policies["ability-chat"] = policy
			adapter, err := platformbilling.New(db, config)
			require.NoError(t, err)
			catalog, err := buildProviderCatalog(context.Background(), adapter, grant, true)
			require.NoError(t, err)
			require.Len(t, catalog.Models, 1)
			m := catalog.Models[0]
			require.Equal(t, "ability-chat", m.ID)
			require.Len(t, m.CapabilityMetadata.Operations, 1)
			op := m.CapabilityMetadata.Operations[0]
			assert.Equal(t, tt.executable, op.Executable)
			if tt.executable {
				require.NotNil(t, op.PublicTariff)
				assert.Equal(t, "CREDIT/6", op.PublicTariff.Asset)
				assert.Equal(t, []string{"text"}, m.InputModalities)
			} else {
				assert.Nil(t, op.PublicTariff)
			}
		})
	}
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
	item := productCreditPriceAvailability(projected, []modelcatalog.ResolvedModel{candidate}, nil, channels, nil)
	op := item.CapabilityMetadata.Operations[0]
	assert.False(t, op.Executable)
	assert.Nil(t, op.PublicTariff)
}
