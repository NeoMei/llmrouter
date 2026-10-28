package minimax

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/nativespeech"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const maxSpeechBytes = 32 << 20

type MiniMaxTTSResponse struct {
	Data *struct {
		Audio  string `json:"audio"`
		Status *int   `json:"status"`
	} `json:"data"`
	ExtraInfo *struct {
		UsageCharacters *int64 `json:"usage_characters"`
		AudioFormat     string `json:"audio_format"`
	} `json:"extra_info"`
	BaseResp *struct {
		StatusCode *int `json:"status_code"`
	} `json:"base_resp"`
}

func getContentTypeByFormat(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	case "pcm":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}
func nativeSpeechError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New("invalid or incomplete MiniMax speech response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
}
func validateSpeechPacket(c *gin.Context, packet *MiniMaxTTSResponse, resp *http.Response, info *relaycommon.RelayInfo) *types.NewAPIError {
	if packet.BaseResp == nil || packet.BaseResp.StatusCode == nil {
		return nativeSpeechError()
	}
	code := *packet.BaseResp.StatusCode
	if code != 0 {
		failure := accountpool.FailureInput{StatusCode: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
		status := http.StatusBadRequest
		switch code {
		case 2049:
			failure.Refusal = accountpool.RefusalInvalidCredential
			status = http.StatusUnauthorized
		case 1008, 2053:
			failure.Refusal = accountpool.RefusalQuotaExhausted
			failure.SharedQuota = true
			status = http.StatusTooManyRequests
		case 1002, 1045:
			failure.StatusCode = http.StatusTooManyRequests
			status = http.StatusTooManyRequests
		}
		if err := accountpool.CompleteNativeOutcome(c, &failure); err != nil {
			return nativeSpeechError()
		}
		return types.NewErrorWithStatusCode(fmt.Errorf("MiniMax speech refused (code %d)", code), types.ErrorCodeBadResponse, status)
	}
	if resp.StatusCode != http.StatusOK || packet.Data == nil || packet.Data.Status == nil || (*packet.Data.Status != 1 && *packet.Data.Status != 2) {
		return nativeSpeechError()
	}
	if *packet.Data.Status == 2 {
		if packet.ExtraInfo == nil || packet.ExtraInfo.UsageCharacters == nil || *packet.ExtraInfo.UsageCharacters < 0 || *packet.ExtraInfo.UsageCharacters > math.MaxInt32 || packet.ExtraInfo.AudioFormat != info.OutputAudioFormat {
			return nativeSpeechError()
		}
		info.NativeUsageCharacters = packet.ExtraInfo.UsageCharacters
	}
	return nil
}
func handleTTSResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, nativeSpeechError()
	}
	defer resp.Body.Close()
	var total []byte
	terminal := false
	var apiErr *types.NewAPIError
	if resp.StatusCode != http.StatusOK {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
		var packet MiniMaxTTSResponse
		if err == nil && len(raw) <= 8192 && common.UnmarshalUnique(raw, &packet) == nil && packet.BaseResp != nil && packet.BaseResp.StatusCode != nil && *packet.BaseResp.StatusCode != 0 {
			return nil, validateSpeechPacket(c, &packet, resp, info)
		}
		failure := accountpool.FailureInput{StatusCode: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
		if err := accountpool.CompleteNativeOutcome(c, &failure); err != nil {
			return nil, nativeSpeechError()
		}
		return nil, types.NewErrorWithStatusCode(errors.New("MiniMax speech HTTP refusal"), types.ErrorCodeBadResponse, resp.StatusCode)
	}
	if info.IsStream && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		helper.StreamScannerHandler(c, resp, info, func(raw string, sr *helper.StreamResult) {
			if c.Request.Context().Err() != nil {
				apiErr = nativeSpeechError()
				sr.Stop(apiErr)
				return
			}
			var packet MiniMaxTTSResponse
			if len(raw) > maxSpeechBytes*2+65536 || common.UnmarshalUnique([]byte(raw), &packet) != nil {
				apiErr = nativeSpeechError()
				sr.Stop(apiErr)
				return
			}
			if apiErr = validateSpeechPacket(c, &packet, resp, info); apiErr != nil {
				sr.Stop(apiErr)
				return
			}
			audio, err := hex.DecodeString(packet.Data.Audio)
			if err != nil {
				apiErr = nativeSpeechError()
				sr.Stop(apiErr)
				return
			}
			terminal = *packet.Data.Status == 2
			// Explicit exclude_aggregated_audio=true is sent. Defensively tolerate a
			// supplier repeating exactly the delivered aggregate in the final packet.
			if terminal && len(total) > 0 && bytes.Equal(audio, total) {
				audio = nil
			}
			if len(total)+len(audio) > maxSpeechBytes {
				apiErr = nativeSpeechError()
				sr.Stop(apiErr)
				return
			}
			if len(audio) > 0 {
				c.Writer.Header().Del("Retry-After")
				n, writeErr := c.Writer.Write(audio)
				if writeErr != nil || n != len(audio) {
					apiErr = nativeSpeechError()
					sr.Stop(apiErr)
					return
				}
				total = append(total, audio...)
				if err := helper.FlushWriter(c); err != nil {
					apiErr = nativeSpeechError()
					sr.Stop(apiErr)
					return
				}
			}
			if terminal {
				sr.Done()
			}
		})
		if apiErr != nil {
			return nil, apiErr
		}
		if !terminal || len(total) == 0 || c.Request.Context().Err() != nil || info.StreamStatus.HasErrors() || !info.StreamStatus.IsNormalEnd() {
			return nil, nativeSpeechError()
		}
	} else {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSpeechBytes*2+65537))
		var packet MiniMaxTTSResponse
		if err != nil || len(raw) > maxSpeechBytes*2+65536 || common.UnmarshalUnique(raw, &packet) != nil {
			return nil, nativeSpeechError()
		}
		if apiErr = validateSpeechPacket(c, &packet, resp, info); apiErr != nil {
			return nil, apiErr
		}
		if *packet.Data.Status != 2 {
			return nil, nativeSpeechError()
		}
		total, err = hex.DecodeString(packet.Data.Audio)
		if err != nil || len(total) == 0 || len(total) > maxSpeechBytes {
			return nil, nativeSpeechError()
		}
		if info.PlatformMode && nativespeech.ValidateAudioFile(total, getContentTypeByFormat(info.OutputAudioFormat), 600) != nil {
			return nil, nativeSpeechError()
		}
		c.Set("binary_stream_content_type", getContentTypeByFormat(info.OutputAudioFormat))
		c.Header("Content-Type", getContentTypeByFormat(info.OutputAudioFormat))
		c.Writer.Header().Del("Retry-After")
		n, err := c.Writer.Write(total)
		if err != nil || n != len(total) {
			return nil, nativeSpeechError()
		}
	}
	if err := accountpool.CompleteNativeOutcome(c, nil); err != nil {
		return nil, nativeSpeechError()
	}
	info.ActualUsageValidated = true
	// The native unit is characters; tokens remain zero and the required exact
	// configured per-request price is settled by the normal atomic lifecycle.
	return &dto.Usage{}, nil
}
