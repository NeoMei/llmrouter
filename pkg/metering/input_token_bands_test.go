package metering

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputTokenBandsApplyToTheWholeRequest(t *testing.T) {
	const tariff = `{
		"version":"metered_v2", "operation":"chat", "policy":"provider_reported_v1",
		"terms":[
			{"key":"input","unit":"token","price":{"numerator":"1","denominator":"1"}},
			{"key":"cache_read","unit":"token","price":{"numerator":"1","denominator":"5"}},
			{"key":"output","unit":"token","price":{"numerator":"4","denominator":"1"}}
		],
		"input_token_bands":[
			{"max_input_tokens":100,"prices":{"input":{"numerator":"1","denominator":"1"},"cache_read":{"numerator":"1","denominator":"5"},"output":{"numerator":"4","denominator":"1"}}},
			{"max_input_tokens":200,"prices":{"input":{"numerator":"2","denominator":"1"},"cache_read":{"numerator":"2","denominator":"5"},"output":{"numerator":"8","denominator":"1"}}}
		]
	}`
	var p Price
	require.NoError(t, json.Unmarshal([]byte(tariff), &p))
	for _, tt := range []struct{ name, input, cache, want string }{
		{"inclusive first band", "80", "20", "124/1"},
		{"one cached token into the next band", "80", "21", "1242/5"},
		{"past the frozen maximum", "181", "20", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usage := Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Measurements: []Measurement{
				{Key: "input", Unit: "token", Quantity: qty(tt.input, 0)},
				{Key: "cache_read", Unit: "token", Quantity: qty(tt.cache, 0)},
				{Key: "output", Unit: "token", Quantity: qty("10", 0)},
			}}
			charge, err := Calculate(p, usage)
			if tt.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, charge.String())
		})
	}
}

func TestInputTokenBandReservationUsesTheWorstReachableWholeRequest(t *testing.T) {
	p := Price{Version: "metered_v2", Operation: "chat", Policy: "provider_reported_v1", Terms: []Term{
		{Key: "input", Unit: "token", Price: Rational{"2", "1"}},
		{Key: "cache_read", Unit: "token", Price: Rational{"1", "1"}},
		{Key: "output", Unit: "token", Price: Rational{"20", "1"}},
	}, InputTokenBands: []InputTokenBand{
		{MaxInputTokens: 100, Prices: map[string]Rational{"input": {"2", "1"}, "cache_read": {"1", "1"}, "output": {"20", "1"}}},
		{MaxInputTokens: 200, Prices: map[string]Rational{"input": {"3", "1"}, "cache_read": {"2", "1"}, "output": {"2", "1"}}},
	}}
	u := Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Measurements: []Measurement{
		{Key: "input", Unit: "token", Quantity: qty("150", 0)},
		{Key: "cache_read", Unit: "token", Quantity: qty("150", 0)},
		{Key: "output", Unit: "token", Quantity: qty("30", 0)},
	}}
	// Band one is 100 * 2 + 30 * 20 = 800. Band two is
	// 150 * 3 + 30 * 2 = 510. A request never uses both bands.
	hold, err := CalculateReservation(p, u)
	require.NoError(t, err)
	assert.Equal(t, "800/1", hold.String())
	u.Measurements[0].Quantity.Value = "201"
	u.Measurements[1].Quantity.Value = "201"
	_, err = CalculateReservation(p, u)
	assert.ErrorContains(t, err, "input exceeds frozen tariff bands")
}
