package openai

import (
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeSTTPreservesBoundedServerDuration(t *testing.T) {
	for _, policy := range []string{"server_audio_measurement_v1", "provider_reported_v1"} {
		t.Run(policy, func(t *testing.T) {
			c, _, resp, info := newImageTestContext(t, `{"text":"hello","duration":9.5}`, "application/json", false)
			measured := 1.001
			info.ActualAudioSeconds = &measured
			info.AudioDurationSource = "server_measurement"
			info.NativeBilling = &relaycommon.NativeBillingContext{Price: metering.Price{Policy: policy}}
			apiErr, _ := OpenaiSTTHandler(c, resp, info, "json")
			require.Nil(t, apiErr)
			if policy == "server_audio_measurement_v1" {
				assert.Equal(t, 1.001, *info.ActualAudioSeconds)
				assert.Equal(t, "server_measurement", info.AudioDurationSource)
			} else {
				assert.Equal(t, 9.5, *info.ActualAudioSeconds)
				assert.Equal(t, "provider_reported", info.AudioDurationSource)
			}
		})
	}
}
