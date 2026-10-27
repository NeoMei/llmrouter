package service

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func mediaPrice(op, policy, key, unit string) metering.Price {
	return metering.Price{Version: "metered_v2", Operation: op, Policy: policy, Terms: []metering.Term{{Key: key, Unit: unit, Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
}
func TestNativeSpeechCharacters(t *testing.T) {
	p := mediaPrice("speech", "unicode_scalar_v1", "characters", "character")
	for _, tt := range []struct {
		text  string
		count string
	}{{"中文🙂e\u0301", "5"}, {"", "0"}} {
		u := NativeSpeechUsage(p, tt.text, nil, true)
		require.NoError(t, metering.ValidateUsage(p, u))
		assert.Equal(t, tt.count, u.Measurements[0].Quantity.Value)
	}
	p.Policy = "provider_reported_v1"
	assert.Equal(t, "missing", NativeSpeechUsage(p, "estimated", nil, true).State)
	n := int64(2)
	u := NativeSpeechUsage(p, "estimated", &n, true)
	assert.Equal(t, "2", u.Measurements[0].Quantity.Value)
	assert.Equal(t, "incomplete", NativeSpeechUsage(p, "estimated", &n, false).State)
}

func TestProviderSpeechAdmissionRequiresExplicitReservationLimit(t *testing.T) {
	old := billing_setting.GetModelNativeReservationLimitsCopy()
	t.Cleanup(func() { require.NoError(t, billing_setting.SetModelNativeReservationLimits(old)) })
	require.NoError(t, billing_setting.SetModelNativeReservationLimits(nil))
	p := mediaPrice("speech", "provider_reported_v1", "characters", "character")
	info := &common.RelayInfo{RelayMode: constant.RelayModeAudioSpeech, BillingModelName: "speech-model"}
	req := dto.AudioRequest{Input: "hello"}
	_, err := EstimateNativeMediaUsage(info, req, p)
	require.Error(t, err)
	require.NoError(t, billing_setting.SetModelNativeReservationLimits(map[string]map[string]metering.Quantity{
		"speech-model": {"speech": {Value: "10"}},
	}))
	u, err := EstimateNativeMediaUsage(info, req, p)
	require.NoError(t, err)
	require.Equal(t, "10", u.Measurements[0].Quantity.Value)
	_, err = EstimateNativeMediaUsage(info, dto.AudioRequest{Input: "01234567890"}, p)
	require.Error(t, err)
}
func TestNativeAudioSeconds(t *testing.T) {
	p := mediaPrice("transcription", "provider_reported_v1", "audio", "audio_second")
	for _, tt := range []struct {
		seconds float64
		value   string
		scale   int
	}{{.1, "1", 1}, {1, "1", 0}, {1.001, "1001", 3}, {0, "0", 0}} {
		u := NativeAudioUsage(p, &tt.seconds, true)
		require.NoError(t, metering.ValidateUsage(p, u))
		assert.Equal(t, metering.Quantity{Value: tt.value, Scale: tt.scale}, u.Measurements[0].Quantity)
	}
	n := -1.
	assert.Equal(t, "invalid", NativeAudioUsage(p, &n, true).State)
}
func TestNativeCompletedImages(t *testing.T) {
	p := mediaPrice("image_generation", "successful_outputs_v1", "images", "image")
	for _, tt := range []struct {
		outputs      []NativeImageOutput
		final        bool
		state, count string
	}{{[]NativeImageOutput{{ID: "a"}, {ID: "b"}}, true, "confirmed", "2"}, {[]NativeImageOutput{}, true, "confirmed", "0"}, {[]NativeImageOutput{{ID: "a"}, {ID: "a"}}, true, "confirmed", "1"}, {[]NativeImageOutput{{ID: "a"}}, false, "incomplete", ""}} {
		u := NativeImageUsage(p, tt.outputs, tt.final)
		assert.Equal(t, tt.state, u.State)
		if tt.count != "" {
			require.NoError(t, metering.ValidateUsage(p, u))
			assert.Equal(t, tt.count, u.Measurements[0].Quantity.Value)
		}
	}
	p.Terms[0].Key = "1024x1024"
	assert.Equal(t, "missing", NativeImageUsage(p, []NativeImageOutput{{ID: "a"}}, true).State)
	assert.Equal(t, "confirmed", NativeImageUsage(p, []NativeImageOutput{{ID: "a", Width: 1024, Height: 1024}}, true).State)
	assert.Equal(t, "invalid", NativeImageUsage(p, []NativeImageOutput{{ID: "a", Width: 512, Height: 512}}, true).State)
}

func TestNativeImageMultipleTiersAndConflictingDuplicate(t *testing.T) {
	p := mediaPrice("image_generation", "successful_outputs_v1", "1024x1024", "image")
	p.Terms = append(p.Terms, metering.Term{Key: "512x512", Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "1"}})
	u := NativeImageUsage(p, []NativeImageOutput{{"a", 1024, 1024}, {"b", 512, 512}}, true)
	require.NoError(t, metering.ValidateUsage(p, u))
	assert.Equal(t, "1", u.Measurements[0].Quantity.Value)
	assert.Equal(t, "1", u.Measurements[1].Quantity.Value)
	u = NativeImageUsage(p, nil, true)
	assert.Equal(t, "0", u.Measurements[0].Quantity.Value)
	assert.Equal(t, "0", u.Measurements[1].Quantity.Value)
	assert.Equal(t, "invalid", NativeImageUsage(p, []NativeImageOutput{{"a", 1024, 1024}, {"a", 512, 512}}, true).State)
}
