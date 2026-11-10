package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// buildChannelAudioProbe returns the request shape a speech or transcription
// model accepts. Voice is mandatory for synthesis; transcription carries the
// WAV separately in the multipart body.
func buildChannelAudioProbe(modelName string) *dto.AudioRequest {
	if isTranscriptionTestModel(modelName) {
		return &dto.AudioRequest{Model: modelName}
	}
	return &dto.AudioRequest{Model: modelName, Input: "LLMRouter connection test.", Voice: "Cherry"}
}

// channelProbeWAV renders one second of 16 kHz mono silence as PCM WAV. Speech
// models accept it as valid audio, and it costs a single second of ASR pricing.
func channelProbeWAV() ([]byte, error) {
	const (
		rate          = 16000
		seconds       = 1
		channels      = 1
		bitsPerSample = 16
	)
	dataBytes := rate * seconds * channels * bitsPerSample / 8
	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+dataBytes))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate*channels*bitsPerSample/8))
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels*bitsPerSample/8))
	_ = binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(dataBytes))
	buf.Write(make([]byte, dataBytes))
	if buf.Len() != 44+dataBytes {
		return nil, fmt.Errorf("unexpected WAV size %d", buf.Len())
	}
	return buf.Bytes(), nil
}

// channelProbeAudioBody builds the request payload for one audio probe.
func channelProbeAudioBody(modelName string, speech bool) ([]byte, string) {
	if speech {
		raw, err := marshalJSON(buildChannelAudioProbe(modelName))
		if err != nil {
			return nil, "application/json"
		}
		return raw, "application/json"
	}
	wav, err := channelProbeWAV()
	if err != nil {
		return nil, "application/json"
	}
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	_ = writer.WriteField("model", modelName)
	if part, err := writer.CreateFormFile("file", "probe.wav"); err == nil {
		_, _ = part.Write(wav)
	}
	_ = writer.Close()
	return payload.Bytes(), writer.FormDataContentType()
}

// testAudioChannel probes speech and transcription models with their own wire
// shape. Connection tests never bill or record usage, so success is a 2xx with a
// non-empty body.
func testAudioChannel(ctx context.Context, channel *model.Channel, testUserID int, modelName string, speech bool, tik time.Time) testResult {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	requestPath := "/v1/audio/transcriptions"
	if speech {
		requestPath = "/v1/audio/speech"
	}
	body, contentType := channelProbeAudioBody(modelName, speech)
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, requestPath, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", contentType)
	// Marks a router connection probe: no billing, no usage, audio shape only.
	c.Set("channel_test_probe", true)
	c.Set("channel", channel.Type)
	c.Set("base_url", channel.GetBaseURL())
	group, err := model.GetUserGroup(testUserID, false)
	if err != nil {
		return testResult{context: c, localErr: err}
	}
	c.Set("group", group)
	if newAPIError := middleware.SetupContextForSelectedChannel(c, channel, modelName); newAPIError != nil {
		return testResult{context: c, localErr: newAPIError, newAPIError: newAPIError}
	}

	request := buildChannelAudioProbe(modelName)
	info, relayErr := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAIAudio, request, nil)
	if relayErr != nil {
		return testResult{context: c, localErr: relayErr, newAPIError: types.NewError(relayErr, types.ErrorCodeGenRelayInfoFailed)}
	}
	info.IsChannelTest = true
	info.InitChannelMeta(c)

	adaptor, ok := relay.GetAdaptor(info.ApiType).(audioAdaptor)
	if !ok || adaptor == nil {
		return testResult{context: c, localErr: fmt.Errorf("channel %d has no audio adaptor", channel.Id)}
	}
	adaptor.Init(info)
	converted, err := adaptor.ConvertAudioRequest(c, info, *request)
	if err != nil {
		return testResult{context: c, localErr: err, newAPIError: types.NewError(err, types.ErrorCodeConvertRequestFailed)}
	}
	resp, sendErr := adaptor.DoRequest(c, info, converted)
	return finishAudioChannelTest(c, info, resp, sendErr, tik)
}

// finishAudioChannelTest mirrors the chat success criteria for speech.
func finishAudioChannelTest(c *gin.Context, info *relaycommon.RelayInfo, resp any, sendErr error, tik time.Time) testResult {
	if sendErr != nil {
		return testResult{context: c, localErr: sendErr, newAPIError: types.NewOpenAIError(sendErr, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)}
	}
	httpResp, ok := resp.(*http.Response)
	if !ok || httpResp == nil {
		bad := fmt.Errorf("audio channel test returned no response")
		return testResult{context: c, localErr: bad, newAPIError: types.NewError(bad, types.ErrorCodeBadResponse)}
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		err := service.RelayErrorHandler(c.Request.Context(), httpResp, true)
		return testResult{context: c, localErr: err, newAPIError: types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)}
	}
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, (1<<20)+1))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		bad := fmt.Errorf("audio channel test returned an empty or oversized response")
		return testResult{context: c, localErr: bad, newAPIError: types.NewOpenAIError(bad, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "time": float64(time.Since(tik).Milliseconds()) / 1000.0})
	return testResult{context: c}
}

// audioAdaptor narrows the fields used by the audio probe.
type audioAdaptor interface {
	Init(*relaycommon.RelayInfo)
	ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error)
	DoRequest(*gin.Context, *relaycommon.RelayInfo, io.Reader) (any, error)
}

func marshalJSON(value any) ([]byte, error) {
	return common.Marshal(value)
}
