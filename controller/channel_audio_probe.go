package controller

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// buildChannelAudioProbe returns the request shape a speech or transcription
// model accepts. Voice is mandatory for synthesis; transcription carries the
// WAV separately in the multipart body.
func buildChannelAudioProbe(model string) *dto.AudioRequest {
	if isTranscriptionTestModel(model) {
		return &dto.AudioRequest{Model: model, ResponseFormat: "json"}
	}
	return &dto.AudioRequest{Model: model, Input: "LLMRouter connection test.", Voice: "Cherry", ResponseFormat: "mp3"}
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

// channelProbeASRContentType keeps the request shape the relay validates:
// OpenAI-compatible multipart everywhere. The domestic Flash ASR path derives its
// inline audio carrier from the same multipart body.
func channelProbeASRContentType() string {
	var payload bytes.Buffer
	return multipart.NewWriter(&payload).FormDataContentType()
}

func channelProbeASRBody(model string, wav []byte) []byte {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	_ = writer.WriteField("model", model)
	_ = writer.WriteField("response_format", "json")
	part, err := writer.CreateFormFile("file", "probe.wav")
	if err != nil {
		return wav
	}
	_, _ = part.Write(wav)
	_ = writer.Close()
	return payload.Bytes()
}

// testAudioChannelResponse mirrors the chat path for speech and transcription:
// the adaptor already wrote the final body and success is a 2xx with a
// non-empty payload, so no token usage is fabricated.
func testAudioChannelResponse(c *gin.Context, channel *model.Channel, info *relaycommon.RelayInfo, resp any, sendErr error, tik time.Time) testResult {
	if sendErr != nil {
		return testResult{context: c, localErr: sendErr, newAPIError: types.NewOpenAIError(sendErr, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)}
	}
	httpResp, ok := resp.(*http.Response)
	if !ok || httpResp == nil {
		return testResult{context: c, localErr: fmt.Errorf("audio channel test returned no response"), newAPIError: types.NewError(fmt.Errorf("audio channel test returned no response"), types.ErrorCodeBadResponse)}
	}
	defer service.CloseResponseBodyGracefully(httpResp)
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
