package platformbilling

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWanPlatformCashPriceVersionUsesMicroYuan(t *testing.T) {
	for _, policy := range []string{"ali_wan26_t2v_cny6_v2", "ali_wan27_t2v_cny6_v1"} {
		t.Run(policy, func(t *testing.T) {
			price := metering.Price{Version: "metered_v2", Operation: "video", Policy: policy, Terms: []metering.Term{
				{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "600000", Denominator: "1"}},
				{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1000000", Denominator: "1"}},
			}}
			require.True(t, validWanVideoPrice(price))
			for _, tc := range []struct{ tier, want string }{{"video_720p", "3000000"}, {"video_1080p", "5000000"}} {
				got, err := meteredReserve(price, operationRequest{Quantity: 5, MeasurementKey: tc.tier})
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
			bad := price
			bad.Terms = append([]metering.Term(nil), price.Terms...)
			bad.Terms[0].Price = metering.Rational{Numerator: "3", Denominator: "5"}
			assert.False(t, validWanVideoPrice(bad))
			bad.Terms[0] = price.Terms[0]
			bad.Terms[0].Step = &metering.Quantity{Value: "10"}
			assert.False(t, validWanVideoPrice(bad))
		})
	}
	assert.False(t, validWanVideoPrice(wanTierPrice()), "old yuan-scale policy is not valid for new platform reservations")
}

func TestWanPlatformEvidenceMustMatchFrozenReservation(t *testing.T) {
	a, db, _ := meteredFixture(t, "speech")
	policy := "ali_wan27_t2v_cny6_v1"
	price := metering.Price{Version: "metered_v2", Operation: "video", Policy: policy, Terms: []metering.Term{{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "600000", Denominator: "1"}}, {Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1000000", Denominator: "1"}}}}
	rateBytes, err := common.Marshal(Rate{Metered: &price})
	require.NoError(t, err)
	frozen, err := common.Marshal(frozenVideo{Resolution: "720P"})
	require.NoError(t, err)
	row := Call{CallID: "wan-frozen", Operation: "video", WireOperation: "videos.create", Status: "UNKNOWN", Asset: "CNY/6", ProviderTaskID: "provider-1", RateSnapshot: string(rateBytes), MediaSnapshot: string(frozen), ReservationQuantity: 5}
	require.NoError(t, db.Create(&row).Error)
	session := &Session{adapter: a, call: row}
	for name, u := range map[string]metering.Usage{
		"short duration": {Version: "metered_v2", Operation: "video", State: "confirmed", Source: policy, ProviderRequestID: "provider-1", Measurements: []metering.Measurement{{Key: "video_720p", Unit: "video_second", Quantity: metering.Quantity{Value: "3"}}}},
		"wrong tier":     {Version: "metered_v2", Operation: "video", State: "confirmed", Source: policy, ProviderRequestID: "provider-1", Measurements: []metering.Measurement{{Key: "video_1080p", Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}},
		"wrong provider": {Version: "metered_v2", Operation: "video", State: "confirmed", Source: policy, ProviderRequestID: "other", Measurements: []metering.Measurement{{Key: "video_720p", Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}},
	} {
		t.Run(name, func(t *testing.T) { assert.Error(t, session.ObserveMetering(context.Background(), u)) })
	}
	var saved Call
	require.NoError(t, db.First(&saved, "call_id = ?", row.CallID).Error)
	assert.Empty(t, saved.Evidence)
}

func TestWanPlatformCashPolicyRequiresCertifiedModelAndDenomination(t *testing.T) {
	p := wanVideoPolicyFixture(t)
	p.Version = "video_v2"
	p.UsagePolicy = "ali_wan26_t2v_cny6_v2"
	p.RateIDs = map[string]string{"CNY/6": "wan-cny"}
	require.True(t, p.valid(p.PublicModel, p.WireOperation))
	bad := p
	bad.RateIDs = map[string]string{"CREDIT/6": "credit"}
	assert.False(t, bad.valid(bad.PublicModel, bad.WireOperation))
	bad = p
	bad.RateIDs = map[string]string{"CNY/6": "cash", "CREDIT/6": "credit"}
	assert.False(t, bad.valid(bad.PublicModel, bad.WireOperation))
	bad = p
	bad.UpstreamModel = "wan2.7-t2v"
	bad.UsagePolicy = "ali_wan27_t2v_cny6_v1"
	assert.False(t, bad.valid(bad.PublicModel, bad.WireOperation), "2.7 remains disabled until its entire transport and settlement chain is certified")
}

func TestWanProductCertificateDoesNotRequireHistoricalCashRate(t *testing.T) {
	legacy := wanVideoPolicyFixture(t)
	product := legacy
	product.Version = "video_v2"
	product.UsagePolicy = "ali_wan26_t2v_cny6_v2"
	product.ChannelID++
	product.RateIDs = nil
	encode := func(p VideoPolicy) string {
		t.Helper()
		raw, err := common.Marshal(map[string]map[string]VideoPolicy{
			p.WireOperation: {p.PublicModel: p},
		})
		require.NoError(t, err)
		return string(raw)
	}
	env := map[string]string{
		"UNIHUB_PLATFORM_ENABLED":              "true",
		"UNIHUB_ROUTING_PRINCIPAL_ID":          "1",
		"UNIHUB_MAX_INPUT_TOKENS":              "1000",
		"UNIHUB_MAX_OUTPUT_TOKENS":             "100",
		"UNIHUB_MAX_BODY_BYTES":                "8192",
		"UNIHUB_VIDEO_POLICIES":                encode(legacy),
		"UNIHUB_PRODUCT_CREDIT_VIDEO_POLICIES": encode(product),
	}
	get := func(name string) string { return env[name] }
	config, err := ConfigFromEnv(get)
	require.NoError(t, err)
	adapter := &Adapter{config: config}
	id, upstream := adapter.ProductCertifiedModelTarget(product.PublicModel, product.WireOperation)
	assert.Equal(t, product.ChannelID, id)
	assert.Equal(t, product.UpstreamModel, upstream)
	legacyPolicy, ok := adapter.VideoPolicy(legacy.PublicModel)
	require.True(t, ok)
	assert.Equal(t, legacy, legacyPolicy)
	assert.False(t, adapter.ProductCreditVideoTarget(product.PublicModel, legacy.ChannelID, legacy.ChannelType, legacy.UpstreamModel, "fixture-key", legacy.Origin))
	assert.True(t, adapter.ProductCreditVideoTarget(product.PublicModel, product.ChannelID, product.ChannelType, product.UpstreamModel, "fixture-key", product.Origin))

	snapshot, err := common.Marshal(frozenVideo{Policy: product, Resolution: "720P"})
	require.NoError(t, err)
	session := &Session{adapter: adapter, call: Call{
		CreditScope: ProductCreditScope, Model: product.PublicModel,
		WireOperation: product.WireOperation, MediaSnapshot: string(snapshot),
	}}
	require.NoError(t, session.ValidateVideoTarget(product.ChannelID, product.ChannelType, product.UpstreamModel, "fixture-key", product.Origin))
	delete(adapter.config.ProductCreditVideoPolicies, product.WireOperation)
	assert.Error(t, session.ValidateVideoTarget(product.ChannelID, product.ChannelType, product.UpstreamModel, "fixture-key", product.Origin))
	frozen, err := session.FrozenVideoPolicy()
	require.NoError(t, err)
	assert.Equal(t, product.ChannelID, frozen.Policy.ChannelID)

	env["UNIHUB_VIDEO_POLICIES"] = encode(product)
	_, err = ConfigFromEnv(get)
	require.Error(t, err)

	env["UNIHUB_VIDEO_POLICIES"] = encode(legacy)
	env["UNIHUB_PRODUCT_CREDIT_VIDEO_POLICIES"] = encode(legacy)
	_, err = ConfigFromEnv(get)
	require.Error(t, err)
}
