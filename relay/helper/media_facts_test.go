package helper

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/capability"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"image"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodedMediaAdmission(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	pngURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
	wav := make([]byte, 44+16000)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
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
	wavURI := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav)
	for _, tc := range []struct {
		name, part, limits string
		invalid            bool
	}{
		{"png", `{"type":"image_url","image_url":{"url":"` + pngURI + `"}}`, `{"max_pixels":6,"formats":["png"]}`, false},
		{"pixels", `{"type":"image_url","image_url":{"url":"` + pngURI + `"}}`, `{"max_pixels":5}`, true},
		{"wav", `{"type":"input_audio","input_audio":{"data":"` + wavURI + `"}}`, `{"max_audio_seconds":1,"formats":["wav"]}`, false},
		{"duration", `{"type":"input_audio","input_audio":{"data":"` + wavURI + `"}}`, `{"max_audio_seconds":0.5}`, true},
		{"remote", `{"type":"input_audio","input_audio":{"data":"https://example.test/a.wav"}}`, `{"max_audio_seconds":1}`, true},
		{"wrong-shape", `{"type":"video_url","video_url":{"url":"https://example.test/v","fps":"bad"}}`, `{}`, true},
		{"video-audio-unknown", `{"type":"video_url","video_url":{"url":"https://example.test/v","fps":0}}`, `{"video_audio":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req dto.GeneralOpenAIRequest
			require.NoError(t, common.Unmarshal([]byte(`{"messages":[{"role":"user","content":[`+tc.part+`]}]}`), &req))
			var contract dto.ModelCapabilities
			require.NoError(t, common.Unmarshal([]byte(`{"media_limits":`+tc.limits+`}`), &contract))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
			err := ValidateModelAdmission(c, &relaycommon.RelayInfo{Request: &req}, contract, capability.OperationChatCreate)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStrictMediaFactsRemainClosedForOtherWires(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1beta/models/m:generateContent", strings.NewReader(`{}`))
	var request dto.GeminiChatRequest
	require.NoError(t, common.Unmarshal([]byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}]}`), &request))
	require.Error(t, ValidateModelAdmission(c, &relaycommon.RelayInfo{Request: &request}, dto.ModelCapabilities{MediaLimits: &dto.MediaLimits{MaxPixels: common.GetPointer(int64(256))}}, capability.OperationGeminiGenerate))
}

func TestDecodedJPEGAndMalformedWAV(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 16, 32)), nil))
	fact, err := DecodeMediaFact("image", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(encoded.Bytes()))
	require.NoError(t, err)
	require.Equal(t, "jpeg", fact.Format)
	require.Equal(t, int64(512), fact.Pixels)
	_, err = DecodeMediaFact("audio", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WAVE")))
	require.Error(t, err)
}

func TestUncertifiedWAVKeepsLegacyCarrier(t *testing.T) {
	wav := make([]byte, 48)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], 40)
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 3)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 4)
	binary.LittleEndian.PutUint16(wav[34:], 32)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 4)
	fact, err := DecodeMediaFact("audio", base64.StdEncoding.EncodeToString(wav))
	require.NoError(t, err)
	require.Empty(t, fact.Format)
}
