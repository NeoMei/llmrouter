package platformbilling

import (
	"bytes"
	"encoding/binary"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mime/multipart"
	"net/textproto"
	"testing"
)

func TestAudioEnvelopePreservesNativeOptions(t *testing.T) {
	c := Config{MaxInputTokens: 20000, MaxBodyBytes: 1 << 20}
	p := AudioPolicy{Version: "audio_v1", PublicModel: "voice", WireOperation: "bailian.audio.speech.create", ChannelID: 1, ChannelType: constant.ChannelTypeAli, UpstreamModel: "qwen3-tts-instruct-flash", ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Origin: "https://dashscope.aliyuncs.com", RateIDs: map[string]string{"CREDIT/6": "rate"}, UsagePolicy: "provider_reported_v1", MaxQuantity: 600}
	c.AudioPolicies = map[string]map[string]AudioPolicy{p.WireOperation: {"voice": p}}
	body := []byte(`{"model":"voice","input":{"text":"你好 A","voice":"Cherry","instructions":"Read calmly","optimize_instructions":false}}`)
	got, err := parseAudioOperation(c, p, "application/json", body)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.UnmarshalUnique(got.Canonical, &decoded))
	assert.Equal(t, false, decoded["input"].(map[string]any)["optimize_instructions"])
	for _, bad := range []string{`{"model":"voice","input":{"text":"ok","voice":"Cherry","optimize_instructions":null}}`, `{"model":"voice","input":{"text":"ok","voice":"Cherry"},"url":"https://bad"}`} {
		_, err = parseAudioOperation(c, p, "application/json", []byte(bad))
		assert.Error(t, err)
	}
	p.MaxQuantity = 3
	_, err = parseAudioOperation(c, p, "application/json", []byte(`{"model":"voice","input":{"text":"four","voice":"Cherry"}}`))
	assert.Error(t, err)
	p.WireOperation = "audio.speech.create"
	p.ChannelType = constant.ChannelTypeMiniMax
	p.UpstreamModel = "speech-2.8-hd"
	p.Origin = "https://api.minimax.io"
	p.MaxQuantity = 10000
	got, err = parseAudioOperation(c, p, "application/json", []byte(`{"model":"voice","input":"hi","voice":"v","speed":1,"response_format":"mp3","metadata":{"audio_setting":{"sample_rate":32000,"bitrate":128000,"channel":1,"force_cbr":false},"voice_setting":{"pitch":0,"latex_read":false}}}`))
	require.NoError(t, err)
	assert.Contains(t, string(got.Canonical), `"force_cbr":false`)
	assert.Contains(t, string(got.Canonical), `"pitch":0`)
}

func TestAudioASRBoundsBeforeReservationAndBoundaryDigest(t *testing.T) {
	c := Config{MaxInputTokens: 20000, MaxBodyBytes: 1 << 20, OperationLimits: map[string]map[string]OperationLimits{"transcription": {"voice": {MaxQuantity: 2}}}}
	p := AudioPolicy{Version: "audio_v1", PublicModel: "voice", WireOperation: "audio.transcriptions.create", ChannelID: 1, ChannelType: 1, UpstreamModel: "whisper-1", ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Origin: "https://api.openai.com", RateIDs: map[string]string{"CREDIT/6": "rate"}, UsagePolicy: "server_audio_measurement_v1", MaxQuantity: 2}
	wav := make([]byte, 16044)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], 16036)
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 16000)
	extraHeader := ""
	build := func(temp string) (string, []byte) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		require.NoError(t, w.WriteField("model", "voice"))
		require.NoError(t, w.WriteField("temperature", temp))
		header := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="short.wav"`}, "Content-Type": {"audio/wav"}}
		if extraHeader != "" {
			header.Set(extraHeader, "surprise")
		}
		part, e := w.CreatePart(header)
		require.NoError(t, e)
		_, e = part.Write(wav)
		require.NoError(t, e)
		require.NoError(t, w.Close())
		return w.FormDataContentType(), b.Bytes()
	}
	ct, b := build("0")
	one, e := parseAudioOperation(c, p, ct, b)
	require.NoError(t, e)
	ct, b = build("0")
	two, e := parseAudioOperation(c, p, ct, b)
	require.NoError(t, e)
	assert.Equal(t, one.Canonical, two.Canonical)
	for _, bad := range []string{"NaN", "+Inf", "-1", "1.1"} {
		ct, b = build(bad)
		_, e = parseAudioOperation(c, p, ct, b)
		assert.Error(t, e, bad)
	}
	extraHeader = "X-File-Directive"
	ct, b = build("0")
	_, e = parseAudioOperation(c, p, ct, b)
	assert.Error(t, e)
	extraHeader = ""
	p.MaxQuantity = 1
	wav = append(wav, 0, 0)
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))
	ct, b = build("0")
	_, e = parseAudioOperation(c, p, ct, b)
	assert.Error(t, e)
}
