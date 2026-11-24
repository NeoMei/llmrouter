package openai

import (
	"encoding/json"
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
		name, model, body     string
		inputRatio, sizeRatio float64
	}{
		{name: "Seedream 5.5 base resolution", model: "doubao-seedream-5-5-261028", body: `{"data":[{},{}],"size":"1600x1600","usage":{"input_images":1}}`, inputRatio: 1 + 0.05/0.26},
		{name: "Seedream 5.5 high resolution", model: "doubao-seedream-5-5-261028", body: `{"data":[{}],"size":"2048x2048","usage":{"input_images":1}}`, inputRatio: 1 + 0.05/0.13, sizeRatio: 0.23 / 0.13},
		{name: "Seedream 5.0 Pro input allowance", model: "doubao-seedream-5-0-pro-260628", body: `{"data":[{},{}],"size":"2048x2048","usage":{"input_images":3}}`, inputRatio: 1 + 0.04/0.60, sizeRatio: 2},
		{name: "Seedream 5.0 Flash flat price", model: "doubao-seedream-5-0-flash-260915", body: `{"data":[{},{}]}`, inputRatio: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := arkImageBillingFixture(tc.model)
			require.NoError(t, applyArkImageBilling(info, []byte(tc.body)))
			if tc.inputRatio == 1 {
				assert.False(t, info.PriceData.HasOtherRatio("ark_input_images"))
			} else {
				assert.InDelta(t, tc.inputRatio, info.PriceData.OtherRatios()["ark_input_images"], 1e-10)
			}
			if tc.sizeRatio == 0 {
				assert.False(t, info.PriceData.HasOtherRatio("ark_resolution"))
			} else {
				assert.InDelta(t, tc.sizeRatio, info.PriceData.OtherRatios()["ark_resolution"], 1e-10)
			}
		})
	}
}

func TestArkImageBillingRejectsUnverifiedContracts(t *testing.T) {
	info := arkImageBillingFixture("doubao-seedream-5-5-261028")
	info.PriceData.UsePrice = false
	require.ErrorContains(t, ValidateArkImageBilling(info, nil), "per-image ModelPrice")

	info = arkImageBillingFixture("doubao-seedream-5-5-261028")
	require.ErrorContains(t, applyArkImageBilling(info, []byte(`{"data":[]}`)), "invalid output count")
	require.ErrorContains(t, applyArkImageBilling(info, []byte(`{"data":[{}],"usage":{"input_images":0}}`)), "actual output dimensions")

	info.IsStream = true
	require.ErrorContains(t, ValidateArkImageBilling(info, nil), "streaming image billing")
	info.IsStream = false
	require.ErrorContains(t, ValidateArkImageBilling(info, &dto.ImageRequest{Extra: map[string]json.RawMessage{"layer_decomposition": json.RawMessage("true")}}), "per-layer price contract")
}
