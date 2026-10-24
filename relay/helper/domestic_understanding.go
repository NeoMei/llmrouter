package helper

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
)

// ValidateDomesticUnderstanding applies only concrete known model restrictions.
// Other legacy Ali vision models remain available; no family-wide support is
// inferred from this tested model, or from operator capability declarations.
func ValidateDomesticUnderstanding(request dto.Request, channelType int, model string) error {
	if channelType != constant.ChannelTypeAli {
		return nil
	}
	facts, err := InspectRequestMedia(request)
	if err != nil {
		return err
	}

	if model == "qwen-turbo" && len(facts) > 0 {
		return fmt.Errorf("qwen-turbo is a text model and does not accept media")
	}
	if model != "qwen3.8-omni-flash" && model != "qwen3.8-max" {
		return nil
	}
	if model == "qwen3.8-omni-flash" {
		for _, fact := range facts {
			if fact.EncodedBytes >= 10*1024*1024 && (fact.Modality == "audio" || fact.Modality == "video") {
				return fmt.Errorf("Omni inline encoded audio/video must be smaller than 10 MiB")
			}
			if fact.Modality == "image" && fact.Format != "" && (fact.Width <= 10 || fact.Height <= 10 || int64(fact.Width) > 200*int64(fact.Height) || int64(fact.Height) > 200*int64(fact.Width)) {
				return fmt.Errorf("Omni image dimensions or aspect ratio are unsupported")
			}
			if fact.Modality == "audio" && fact.Seconds > 3*60*60 {
				return fmt.Errorf("Omni audio exceeds supported duration")
			}
		}
	}
	if model == "qwen3.8-omni-flash" && request.IsStream(nil) {
		return fmt.Errorf("selected Omni understanding currently supports nonstreaming requests")
	}
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		for _, message := range req.Messages {
			raw, err := common.Marshal(message.Content)
			if err != nil {
				return err
			}
			if common.GetJsonType(raw) != "array" {
				continue
			}
			var parts []map[string]any
			if err := common.Unmarshal(raw, &parts); err != nil {
				return err
			}
			for _, part := range parts {
				kind, _ := part["type"].(string)
				switch kind {
				case "text", "image_url", "input_audio", "video_url":
				default:
					return fmt.Errorf("selected understanding path does not support content type %q", kind)
				}
				if kind == "video_url" {
					video, _ := part["video_url"].(map[string]any)
					if _, nested := video["fps"]; nested {
						return fmt.Errorf("selected understanding model does not support nested video fps")
					}
					if fps, exists := part["fps"]; exists {
						n, ok := fps.(float64)
						if model != "qwen3.8-max" || !ok || n < 0.1 || n > 10 {
							return fmt.Errorf("selected video model does not support this fps")
						}
					}
				}
				if kind == "input_audio" {
					if model == "qwen3.8-max" {
						return fmt.Errorf("selected vision path does not support audio input")
					}
					audio, validAudio := part["input_audio"].(map[string]any)
					if !validAudio {
						return fmt.Errorf("Chat requires nested input_audio carrier")
					}
					if format, _ := audio["format"].(string); format != "" && format != "wav" && format != "mp3" {
						return fmt.Errorf("selected understanding path supports WAV/MP3 carriers")
					}
				}
			}
		}
		var modalities []string
		if len(req.Modalities) > 0 {
			if err := common.Unmarshal(req.Modalities, &modalities); err != nil {
				return err
			}
		}
		for _, modality := range modalities {
			if modality != "text" {
				return fmt.Errorf("qwen3.8-omni-flash understanding supports text output")
			}
		}
		if model == "qwen3.8-omni-flash" && req.ResponseFormat != nil && req.ResponseFormat.Type == "json_schema" {
			return fmt.Errorf("selected Omni model does not support JSON Schema output")
		}
	case *dto.OpenAIResponsesRequest:
		if model == "qwen3.8-max" {
			return fmt.Errorf("selected vision path currently supports Chat only")
		}
		if req.Background != nil && *req.Background {
			return fmt.Errorf("selected Omni understanding currently supports synchronous Responses only")
		}
		var tools []struct {
			Type string `json:"type"`
		}
		if len(req.Tools) > 0 {
			if err := common.Unmarshal(req.Tools, &tools); err != nil {
				return err
			}
			for _, tool := range tools {
				if tool.Type != "function" && tool.Type != "web_search" {
					return fmt.Errorf("selected Omni path does not support hosted tool %q", tool.Type)
				}
			}
		}
		var input []map[string]any
		if common.GetJsonType(req.Input) != "array" {
			return nil
		}
		if err := common.Unmarshal(req.Input, &input); err != nil {
			return err
		}
		for _, message := range input {
			parts, _ := message["content"].([]any)
			for _, value := range parts {
				part, _ := value.(map[string]any)
				kind, _ := part["type"].(string)
				switch kind {
				case "input_text", "output_text", "input_image", "input_audio", "input_video":
				default:
					return fmt.Errorf("selected Omni Responses does not support content type %q", kind)
				}
				if kind != "input_audio" && kind != "input_video" {
					continue
				}
				if message["role"] != "user" {
					return fmt.Errorf("Omni Responses audio/video inputs require user role")
				}
				if kind == "input_audio" {
					if multichannel, exists := part["use_multichannel"]; exists {
						enabled, valid := multichannel.(bool)
						if !valid || enabled {
							return fmt.Errorf("selected understanding path supports only default single-channel audio")
						}
					}
					if format, _ := part["format"].(string); format != "" && format != "wav" && format != "mp3" {
						return fmt.Errorf("selected understanding path supports WAV/MP3 carriers")
					}
					if _, nested := part["input_audio"]; nested {
						return fmt.Errorf("Omni Responses requires audio_url or data at content level")
					}
				}
				if kind == "input_video" {
					if _, fps := part["fps"]; fps {
						return fmt.Errorf("Omni Responses does not support fps control")
					}
					if _, ok := part["video_url"].(string); !ok {
						return fmt.Errorf("Omni Responses requires string video_url")
					}
				}
			}
		}
	default:
		return fmt.Errorf("selected understanding requires typed Chat or Responses request")
	}
	return nil
}
