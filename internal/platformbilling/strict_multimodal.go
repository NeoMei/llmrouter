package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"unicode/utf8"
)

func parseKnowledgeMultimodal(c Config, p KnowledgePolicy, body []byte, native bool) (operationRequest, error) {
	invalid := operationRequest{}
	if !p.valid(c, p.PublicModel, "multimodal.embeddings.create") || !utf8.Valid(body) || int64(len(body)) > c.MaxBodyBytes {
		return invalid, ErrInvalid
	}
	var original map[string]any
	if common.UnmarshalUnique(body, &original) != nil {
		return invalid, ErrInvalid
	}
	request := original
	if native {
		if !protocolKeys(original, "model input parameters") {
			return invalid, ErrInvalid
		}
		input, ok := original["input"].(map[string]any)
		if !ok || !protocolKeys(input, "contents") {
			return invalid, ErrInvalid
		}
		request = map[string]any{"model": original["model"], "input": input["contents"]}
		if raw, exists := original["parameters"]; exists {
			params, ok := raw.(map[string]any)
			if !ok || !protocolKeys(params, "dimension fps enable_fusion") {
				return invalid, ErrInvalid
			}
			for k, v := range params {
				if k == "dimension" {
					k = "dimensions"
				}
				request[k] = v
			}
		}
	}
	if !protocolKeys(request, "model input dimensions fps enable_fusion") || request["model"] != p.PublicModel {
		return invalid, ErrInvalid
	}
	for _, v := range request {
		if v == nil {
			return invalid, ErrInvalid
		}
	}
	items, ok := request["input"].([]any)
	if !ok || len(items) < 1 || len(items) > p.MaxBatch {
		return invalid, ErrInvalid
	}
	texts, images, videos := 0, 0, 0
	for _, v := range items {
		item, ok := v.(map[string]any)
		if !ok || len(item) != 1 || !protocolKeys(item, "text image video") {
			return invalid, ErrInvalid
		}
		for k, v := range item {
			s, ok := v.(string)
			if !ok || s == "" || !utf8.ValidString(s) {
				return invalid, ErrInvalid
			}
			switch k {
			case "text":
				texts += len(s)
			case "image":
				images++
			case "video":
				videos++
			}
		}
	}
	if texts > 32000 || texts > p.HardInputTokens || images > 5 || videos > 1 {
		return invalid, ErrInvalid
	}
	if v, exists := request["dimensions"]; exists {
		n, e := confirmedCount(v)
		if e != nil || n != p.Space.Dimensions {
			return invalid, ErrInvalid
		}
	}
	if v, exists := request["fps"]; exists {
		n, e := confirmedCount(v)
		if e != nil || n != 1 {
			return invalid, ErrInvalid
		}
	}
	if v, exists := request["enable_fusion"]; exists {
		if v != false {
			return invalid, ErrInvalid
		}
	}
	// Explicitly freeze fixed semantic-space settings in the effective request.
	request["dimensions"] = p.Space.Dimensions
	request["fps"] = 1
	canonical, e := common.Marshal(original)
	if e != nil {
		return invalid, ErrInvalid
	}
	effective, e := common.Marshal(request)
	if e != nil {
		return invalid, ErrInvalid
	}
	snapshot, e := common.Marshal(frozenKnowledge{Policy: p, Body: effective})
	if e != nil {
		return invalid, ErrInvalid
	}
	return operationRequest{Model: p.PublicModel, Canonical: canonical, Quantity: p.HardInputTokens, Output: p.VisualTokens, MediaSnapshot: string(snapshot)}, nil
}
func validKnowledgeSpace(s *dto.EmbeddingSpace) bool {
	if s == nil {
		return false
	}
	switch s.Dimensions {
	case 256, 512, 768, 1024, 1536, 2048, 2560:
		return true
	}
	return false
}
