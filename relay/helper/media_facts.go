package helper

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/mediafacts"
	"math"
)

type MediaFact = mediafacts.MediaFact

const MaxInlineMediaBytes = mediafacts.MaxInlineMediaBytes

func DecodeMediaFact(modality, carrier string) (MediaFact, error) {
	return mediafacts.DecodeMediaFact(modality, carrier)
}

// InspectRequestMedia only visits message content; tool schemas/arguments and
// arbitrary metadata stay opaque. Both Chat and Responses use this before money.
func InspectRequestMedia(request dto.Request) ([]MediaFact, error) {
	var messages []map[string]any
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		raw, err := common.Marshal(req.Messages)
		if err != nil {
			return nil, err
		}
		if err = common.Unmarshal(raw, &messages); err != nil {
			return nil, err
		}
	case *dto.OpenAIResponsesRequest:
		if len(req.Input) == 0 || common.GetJsonType(req.Input) != "array" {
			return nil, nil
		}
		if err := common.Unmarshal(req.Input, &messages); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	facts := []MediaFact{}
	for _, message := range messages {
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, value := range parts {
			part, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid message content part")
			}
			kind, _ := part["type"].(string)
			modality, carrier := "", ""
			var payload map[string]any
			switch kind {
			case "image_url", "input_image":
				modality = "image"
				switch v := part["image_url"].(type) {
				case string:
					carrier = v
				case map[string]any:
					carrier, _ = v["url"].(string)
					if detail, exists := v["detail"]; exists {
						if _, ok := detail.(string); !ok {
							return nil, fmt.Errorf("invalid image detail")
						}
					}
				}
				if carrier == "" {
					if fileID, ok := part["file_id"].(string); ok && fileID != "" {
						facts = append(facts, MediaFact{Modality: modality})
						continue
					}
				}
			case "input_audio":
				modality = "audio"
				payload, _ = part["input_audio"].(map[string]any)
				if payload == nil {
					payload = part
				}
				carrier, _ = payload["data"].(string)
				remote, _ := payload["audio_url"].(string)
				if carrier != "" && remote != "" {
					return nil, fmt.Errorf("audio data and audio_url are mutually exclusive")
				}
				if carrier == "" {
					carrier = remote
				}
				if format, exists := payload["format"]; exists {
					if _, ok := format.(string); !ok {
						return nil, fmt.Errorf("invalid audio format")
					}
				}
			case "video_url", "input_video":
				modality = "video"
				switch v := part["video_url"].(type) {
				case string:
					carrier = v
				case map[string]any:
					payload = v
					carrier, _ = v["url"].(string)
				}
				if payload != nil {
					if fps, exists := payload["fps"]; exists {
						n, ok := fps.(float64)
						if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
							return nil, fmt.Errorf("invalid video fps")
						}
					}
				}
			default:
				continue
			}
			if fps, exists := part["fps"]; exists {
				n, ok := fps.(float64)
				if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
					return nil, fmt.Errorf("invalid video fps")
				}
			}
			if carrier == "" {
				return nil, fmt.Errorf("invalid %s carrier", modality)
			}
			fact, err := DecodeMediaFact(modality, carrier)
			if err != nil {
				return nil, err
			}
			if payload != nil && fact.Format != "" {
				if format, _ := payload["format"].(string); format != "" && format != fact.Format {
					return nil, fmt.Errorf("audio format differs from decoded bytes")
				}
			}
			facts = append(facts, fact)
		}
	}
	return facts, nil
}
