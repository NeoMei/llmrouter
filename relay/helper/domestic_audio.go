package helper

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

const DomesticASRDataKey = "domestic_asr_wav_data"

// ValidateDomesticAudio runs before reservation, using only the final mapped
// model and bounded original body. The immutable string is replayed verbatim.
func ValidateDomesticAudio(c *gin.Context, info *relaycommon.RelayInfo, channelType int, model string, billingModel string) error {
	request, ok := info.Request.(*dto.AudioRequest)
	if !ok {
		return nil
	}
	switch channelType {
	case constant.ChannelTypeAli:
		if info.RelayMode != relayconstant.RelayModeAudioTranscription || model != "qwen3-asr-flash" {
			return fmt.Errorf("Ali audio supports qwen3-asr-flash transcription only")
		}
		if request.ResponseFormat != "json" && request.ResponseFormat != "text" {
			return fmt.Errorf("Flash ASR supports json/text transcript only; timestamps and translation are unsupported")
		}
		if c.GetString(DomesticASRDataKey) != "" {
			return nil
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return err
		}
		if storage.Size() > MaxInlineMediaBytes {
			return fmt.Errorf("ASR multipart body exceeds 10 MiB")
		}
		raw, err := storage.Bytes()
		if err != nil {
			return err
		}
		mediaType, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			return fmt.Errorf("ASR requires multipart WAV")
		}
		reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
		seen := map[string]bool{}
		carrier := ""
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("invalid ASR multipart")
			}
			name := part.FormName()
			if seen[name] {
				return fmt.Errorf("duplicate ASR field")
			}
			seen[name] = true
			switch name {
			case "model", "response_format", "file":
			default:
				return fmt.Errorf("unsupported ASR field %q", name)
			}
			data, err := io.ReadAll(io.LimitReader(part, MaxInlineMediaBytes+1))
			part.Close()
			if err != nil || len(data) > MaxInlineMediaBytes {
				return fmt.Errorf("invalid ASR part")
			}
			if name != "file" {
				if part.FileName() != "" {
					return fmt.Errorf("invalid ASR scalar field")
				}
				continue
			}
			if part.FileName() == "" {
				return fmt.Errorf("ASR file is required")
			}
			carrier = "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(data)
			if len(carrier) > MaxInlineMediaBytes {
				return fmt.Errorf("encoded ASR audio exceeds 10 MiB")
			}
			fact, err := DecodeMediaFact("audio", carrier)
			if err != nil || fact.Format != "wav" || fact.Seconds <= 0 || fact.Seconds > 300 {
				return fmt.Errorf("ASR requires valid PCM WAV up to five minutes")
			}
		}
		if !seen["model"] || carrier == "" {
			return fmt.Errorf("ASR model and file are required")
		}
		c.Set(DomesticASRDataKey, carrier)
	case constant.ChannelTypeMiniMax:
		if info.RelayMode != relayconstant.RelayModeAudioSpeech {
			return fmt.Errorf("MiniMax audio supports speech only")
		}
		copy := *request
		copy.Model = model
		if _, err := dto.BuildMiniMaxSpeech(copy); err != nil {
			return err
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return err
		}
		if storage.Size() > dto.MaxMiniMaxSpeechBodyBytes {
			return fmt.Errorf("speech request exceeds 128 KiB")
		}
		raw, err := storage.Bytes()
		if err != nil {
			return err
		}
		var fields map[string]any
		if err := common.UnmarshalUnique(raw, &fields); err != nil {
			return err
		}
		for name := range fields {
			switch name {
			case "model", "input", "voice", "response_format", "speed", "metadata", "stream", "stream_format":
			default:
				return fmt.Errorf("unsupported speech field %q", name)
			}
		}
		if info.PlatformMode {
			return nil
		}
		price, exists := ratio_setting.GetModelPrice(billingModel, false)
		if !exists || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) || billing_setting.GetBillingMode(billingModel) == billing_setting.BillingModeTieredExpr {
			return fmt.Errorf("MiniMax speech requires explicit configured per-request pricing")
		}
	}
	return nil
}

// ValidateNativeSpeechPrice protects a positive fixed price from rounding to
// zero and excludes token-expression settlement before any wallet reservation.
func ValidateNativeSpeechPrice(c *gin.Context, info *relaycommon.RelayInfo) error {
	if c.GetInt("channel_type") != constant.ChannelTypeMiniMax || info.RelayMode != relayconstant.RelayModeAudioSpeech {
		return nil
	}
	if !info.PriceData.UsePrice || info.TieredBillingSnapshot != nil {
		return fmt.Errorf("native speech requires per-request price")
	}
	if info.PriceData.ModelPrice > 0 && info.PriceData.GroupRatioInfo.GroupRatio > 0 && common.QuotaFromFloat(info.PriceData.ModelPrice*common.QuotaPerUnit*info.PriceData.GroupRatioInfo.GroupRatio) == 0 {
		return fmt.Errorf("native speech price is below quota precision")
	}
	return nil
}
