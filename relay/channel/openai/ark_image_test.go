package openai

import (
	"encoding/json"

	"github.com/tidwall/gjson"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func arkImageBillingFixture(model string) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:       constant.ChannelTypeVolcEngine,
		UpstreamModelName: model,
	}}
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", 1)
	return info
}

func TestArkImageBillingUsesOfficialDimensions(t *testing.T) {
	for _, tc := range []struct {
		name, model, body string
		basePrice, cost   float64
	}{
		{name: "Seedream 5.5 base resolution", model: "doubao-seedream-5-5-261028", body: `{"data":[{"size":"1600x1600"},{"size":"1600x1600"}],"usage":{"input_images":1}}`, basePrice: 0.13, cost: 0.31},
		{name: "Seedream 5.5 high resolution does not multiply input charge", model: "doubao-seedream-5-5-261028", body: `{"data":[{"size":"2048x2048"}],"usage":{"input_images":1}}`, basePrice: 0.13, cost: 0.28},
		{name: "Seedream 5.0 Pro input allowance", model: "doubao-seedream-5-0-pro-260628", body: `{"data":[{"size":"2048x2048"},{"size":"2048x2048"}],"usage":{"input_images":3}}`, basePrice: 0.30, cost: 1.24},
		{name: "Seedream 5.0 Flash flat price", model: "doubao-seedream-5-0-flash-260915", body: `{"data":[{},{}]}`, basePrice: 0.12, cost: 0.24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := arkImageBillingFixture(tc.model)
			info.PriceData.ModelPrice = tc.basePrice / 7.3
			updateOpenAIImageCount(info, gjson.GetBytes([]byte(tc.body), "data.#").Int())
			require.NoError(t, applyArkImageBilling(info, []byte(tc.body)))
			cost := info.PriceData.ApplyOtherRatiosToFloat(info.PriceData.ModelPrice) * 7.3
			assert.InDelta(t, tc.cost, cost, 1e-10)
		})
	}
}

func TestArkImageBillingRejectsUnverifiedContracts(t *testing.T) {
	info := arkImageBillingFixture("doubao-seedream-5-5-261028")
	info.PriceData.UsePrice = false
	require.ErrorContains(t, ValidateArkImageBilling(info, nil), "per-image ModelPrice")

	info = arkImageBillingFixture("doubao-seedream-5-5-261028")
	require.ErrorContains(t, applyArkImageBilling(info, []byte(`{"data":[]}`)), "invalid output count")
	require.ErrorContains(t, applyArkImageBilling(info, []byte(`{"data":[{}],"usage":{"input_images":0}}`)), "actual dimensions")

	unknown := arkImageBillingFixture("doubao-seedream-9-9-991231")
	require.ErrorContains(t, ValidateArkImageBilling(unknown, nil), "no verified per-image price")

	info.IsStream = true
	require.ErrorContains(t, ValidateArkImageBilling(info, nil), "streaming image billing")
	info.IsStream = false
	require.ErrorContains(t, ValidateArkImageBilling(info, &dto.ImageRequest{Extra: map[string]json.RawMessage{"layer_decomposition": json.RawMessage("true")}}), "per-layer price contract")
}
