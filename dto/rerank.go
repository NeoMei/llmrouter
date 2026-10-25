package dto

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type RerankRequest struct {
	Documents       []any  `json:"documents"`
	Query           string `json:"query"`
	Model           string `json:"model"`
	TopN            *int   `json:"top_n,omitempty"`
	ReturnDocuments *bool  `json:"return_documents,omitempty"`
	MaxChunkPerDoc  *int   `json:"max_chunk_per_doc,omitempty"`
	OverLapTokens   *int   `json:"overlap_tokens,omitempty"`
}

func (r *RerankRequest) IsStream(c *gin.Context) bool {
	return false
}

func (r *RerankRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var texts = make([]string, 0)

	for _, document := range r.Documents {
		texts = append(texts, fmt.Sprintf("%v", document))
	}

	if r.Query != "" {
		texts = append(texts, r.Query)
	}

	return &types.TokenCountMeta{
		CombineText: strings.Join(texts, "\n"),
	}
}

func (r *RerankRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}

func (r *RerankRequest) GetReturnDocuments() bool {
	if r.ReturnDocuments == nil {
		return false
	}
	return *r.ReturnDocuments
}

type RerankResponseResult struct {
	Document       any     `json:"document,omitempty"`
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type RerankDocument struct {
	Text any `json:"text"`
}

type RerankResponse struct {
	Results []RerankResponseResult `json:"results"`
	Usage   Usage                  `json:"usage"`
}

// ValidateRerankResults checks actual wire fields before scalar DTO decoding can
// turn missing/null indices and scores into apparently valid zero values.
func ValidateRerankResults(results []json.RawMessage, documentCount, resultCount int) error {
	if len(results) != resultCount {
		return fmt.Errorf("rerank response cardinality differs from requested result count")
	}
	seen := make(map[int]bool, resultCount)
	for _, raw := range results {
		var item struct {
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		}
		if err := common.Unmarshal(raw, &item); err != nil {
			return err
		}
		if item.Index == nil || *item.Index < 0 || *item.Index >= documentCount || seen[*item.Index] {
			return fmt.Errorf("rerank indices must be present, unique and within the original documents")
		}
		if item.Score == nil || math.IsNaN(*item.Score) || math.IsInf(*item.Score, 0) {
			return fmt.Errorf("rerank relevance_score must be a finite number")
		}
		seen[*item.Index] = true
	}
	return nil
}
