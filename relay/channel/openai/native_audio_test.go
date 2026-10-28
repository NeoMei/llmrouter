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

func TestManagedSpeechRejectsWrongAudioBeforeDelivery(t *testing.T) {
	c, w, resp, info := newImageTestContext(t, "not audio", "audio/mpeg", false)
	info.PlatformMode = true
	text := "hello"
	info.NativeSpeechText = &text
	OpenaiTTSHandler(c, resp, info)
	assert.Equal(t, 502, w.Code)
	assert.Nil(t, info.NativeSpeechText)
	assert.NotContains(t, w.Body.String(), "not audio")
}
func TestManagedTranscriptionRejectsEmptyTranscript(t *testing.T) {
	c, _, resp, info := newImageTestContext(t, `{"text":""}`, "application/json", false)
	info.PlatformMode = true
	e, _ := OpenaiSTTHandler(c, resp, info, "json")
	require.NotNil(t, e)
}

func TestManagedTranscriptionRejectsWrongMIMEAndErrorEnvelope(t *testing.T) {
	for _, tc := range []struct{ body, mime, format string }{{`{"text":"hello"}`, "text/html", "json"}, {`{"error":"failed","text":"hello"}`, "application/json", "json"}, {`{"error":"failed"}`, "application/json", "text"}} {
		c, _, resp, info := newImageTestContext(t, tc.body, tc.mime, false)
		info.PlatformMode = true
		e, _ := OpenaiSTTHandler(c, resp, info, tc.format)
		require.NotNil(t, e)
	}
}
