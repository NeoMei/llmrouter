package platformbilling

import (
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/channel/nativespeech"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func qwenWAV(seconds int) []byte {
	byteRate := 16000
	data := seconds * byteRate
	b := make([]byte, 44+data)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 8000)
	binary.LittleEndian.PutUint32(b[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
	return b
}

func qwenASRPolicy(maxSeconds int) AudioPolicy {
	return AudioPolicy{Version: "audio_v1", PublicModel: "voice", WireOperation: "dashscope.audio.transcriptions.create", ChannelID: 1, ChannelType: constant.ChannelTypeAli, UpstreamModel: "qwen3-asr-flash", ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Origin: "https://dashscope.aliyuncs.com", RateIDs: map[string]string{"CNY/6": "rate"}, UsagePolicy: "provider_reported_seconds_v1", MaxQuantity: maxSeconds}
}

func qwenASRJSON(t *testing.T, model, dataURI string) []byte {
	t.Helper()
	return []byte("{\"model\":\"" + model + "\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + dataURI + "\"}}]}],\"asr_options\":{\"enable_itn\":false}}")
}

func TestQwenASRAdmissionBoundsBeforeReservation(t *testing.T) {
	c := Config{MaxInputTokens: 20000, MaxBodyBytes: 1 << 20}
	p := qwenASRPolicy(300)
	c.AudioPolicies = map[string]map[string]AudioPolicy{p.WireOperation: {"voice": p}}
	dataURI := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(qwenWAV(1))

	got, err := parseAudioOperation(c, p, "application/json", qwenASRJSON(t, "voice", dataURI))
	require.NoError(t, err)
	assert.Equal(t, "voice", got.Model)
	assert.Equal(t, p.MaxQuantity, got.Quantity)
	assert.NotEmpty(t, got.Canonical)
	assert.Contains(t, string(got.Canonical), dataURI)

	// Wrong public model is refused.
	_, err = parseAudioOperation(c, p, "application/json", qwenASRJSON(t, "other", dataURI))
	assert.Error(t, err)
	// Wrong content type is refused.
	_, err = parseAudioOperation(c, p, "multipart/form-data; boundary=x", qwenASRJSON(t, "voice", dataURI))
	assert.Error(t, err)
	// Remote URL audio is refused (no arbitrary fetch).
	_, err = parseAudioOperation(c, p, "application/json", qwenASRJSON(t, "voice", "https://example.com/a.wav"))
	assert.Error(t, err)
	// Non-WAV payload is refused.
	_, err = parseAudioOperation(c, p, "application/json", qwenASRJSON(t, "voice", "data:audio/mpeg;base64,AAAA"))
	assert.Error(t, err)
	// Over the private duration ceiling is refused before funds.
	small := qwenASRPolicy(1)
	over := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(qwenWAV(2))
	_, err = parseAudioOperation(c, small, "application/json", qwenASRJSON(t, "voice", over))
	assert.Error(t, err)
	// Within the ceiling is accepted.
	at := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(qwenWAV(1))
	_, err = parseAudioOperation(c, small, "application/json", qwenASRJSON(t, "voice", at))
	require.NoError(t, err)
}

func TestQwenASRMeteringProviderSeconds(t *testing.T) {
	price := metering.Price{Version: "metered_v2", Operation: "transcription", Policy: "provider_reported_seconds_v1", Terms: []metering.Term{{Key: "audio", Unit: "audio_second", Price: metering.Rational{Numerator: "3", Denominator: "1"}}}}
	confirmed := nativespeech.QwenASRResult{Completed: true, UsageState: "confirmed", Seconds: metering.Quantity{Value: "15", Scale: 1}, RequestID: "req-1"}
	u := NativeQwenASRMetering(price, confirmed)
	assert.Equal(t, "confirmed", u.State)
	require.Len(t, u.Measurements, 1)
	assert.Equal(t, "audio", u.Measurements[0].Key)
	assert.Equal(t, "audio_second", u.Measurements[0].Unit)
	assert.Equal(t, "15", u.Measurements[0].Quantity.Value)
	assert.Equal(t, 1, u.Measurements[0].Quantity.Scale)
	assert.Equal(t, "req-1", u.ProviderRequestID)
	amount, err := metering.Calculate(price, u)
	require.NoError(t, err)
	assert.Equal(t, "9/2", amount.RatString())

	missing := nativespeech.QwenASRResult{Completed: false, UsageState: "missing"}
	assert.Equal(t, "missing", NativeQwenASRMetering(price, missing).State)

	invalid := nativespeech.QwenASRResult{Completed: false, UsageState: "invalid"}
	assert.Equal(t, "incomplete", NativeQwenASRMetering(price, invalid).State)
}

func TestQwenASRAudioRequestModel(t *testing.T) {
	c := Config{MaxBodyBytes: 1 << 20}
	dataURI := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(qwenWAV(1))
	model, err := audioRequestModel("dashscope.audio.transcriptions.create", "application/json", qwenASRJSON(t, "voice", dataURI), c)
	require.NoError(t, err)
	assert.Equal(t, "voice", model)
	_, err = audioRequestModel("dashscope.audio.transcriptions.create", "application/json", []byte("{\"model\":\"voice\"}"), c)
	assert.Error(t, err)
	assert.True(t, IsAudioWire("dashscope.audio.transcriptions.create"))
}
