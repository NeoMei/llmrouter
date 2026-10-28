package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

func parseKnowledgeEmbedding(c Config, p KnowledgePolicy, body []byte) (operationRequest, error) {
	if !p.valid(c, p.PublicModel, "embeddings.create") {
		return operationRequest{}, ErrInvalid
	}
	bounded := c
	bounded.MaxInputTokens = p.HardInputTokens
	req, e := parseOperation(bounded, "embedding", "application/json", body)
	if e != nil || req.Model != p.PublicModel {
		return operationRequest{}, ErrInvalid
	}
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil {
		return operationRequest{}, ErrInvalid
	}
	count, e := (&dto.EmbeddingRequest{Input: request["input"]}).InputCount()
	if e != nil || count > p.MaxBatch {
		return operationRequest{}, ErrInvalid
	}
	switch input := request["input"].(type) {
	case string:
		if len(input) > p.MaxItemTokens/2 {
			return operationRequest{}, ErrInvalid
		}
	case []any:
		for _, v := range input {
			switch item := v.(type) {
			case string:
				if len(item) > p.MaxItemTokens/2 {
					return operationRequest{}, ErrInvalid
				}
			case []any:
				if len(item) > p.MaxItemTokens {
					return operationRequest{}, ErrInvalid
				}
			default:
				if len(input) > p.MaxItemTokens {
					return operationRequest{}, ErrInvalid
				}
			}
		}
	}
	if v, ok := request["dimensions"]; ok {
		n, e := confirmedCount(v)
		if e != nil || n != p.Space.Dimensions {
			return operationRequest{}, ErrInvalid
		}
	}
	request["dimensions"] = p.Space.Dimensions
	effective, e := common.Marshal(request)
	if e != nil {
		return operationRequest{}, e
	}
	snapshot, e := common.Marshal(frozenKnowledge{Policy: p, Body: effective})
	if e != nil {
		return operationRequest{}, e
	}
	req.MediaSnapshot = string(snapshot)
	return req, nil
}
