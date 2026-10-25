package dto

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type EmbeddingOptions struct {
	Seed             int      `json:"seed,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopK             int      `json:"top_k,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	NumPredict       int      `json:"num_predict,omitempty"`
	NumCtx           int      `json:"num_ctx,omitempty"`
}

type EmbeddingRequest struct {
	Model            string   `json:"model"`
	Input            any      `json:"input"`
	EncodingFormat   string   `json:"encoding_format,omitempty"`
	Dimensions       *int     `json:"dimensions,omitempty"`
	User             string   `json:"user,omitempty"`
	Seed             *float64 `json:"seed,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
}

func (r *EmbeddingRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var texts = make([]string, 0)

	inputs := r.ParseInput()
	for _, input := range inputs {
		texts = append(texts, input)
	}

	return &types.TokenCountMeta{
		CombineText: strings.Join(texts, "\n"),
	}
}

func (r *EmbeddingRequest) IsStream(c *gin.Context) bool {
	return false
}

func (r *EmbeddingRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}

func (r *EmbeddingRequest) ParseInput() []string {
	if r.Input == nil {
		return make([]string, 0)
	}
	var input []string
	switch r.Input.(type) {
	case string:
		input = []string{r.Input.(string)}
	case []any:
		input = make([]string, 0, len(r.Input.([]any)))
		for _, item := range r.Input.([]any) {
			if str, ok := item.(string); ok {
				input = append(input, str)
			}
		}
	}
	return input
}

type EmbeddingResponseItem struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type EmbeddingResponse struct {
	Object string                  `json:"object"`
	Data   []EmbeddingResponseItem `json:"data"`
	Model  string                  `json:"model"`
	Usage  `json:"usage"`
}

// InputCount validates the OpenAI embedding input union without silently
// dropping malformed batch members. Token arrays retain their legacy wire form.
func (r *EmbeddingRequest) InputCount() (int, error) {
	switch input := r.Input.(type) {
	case string:
		if input == "" {
			return 0, fmt.Errorf("input is empty")
		}
		return 1, nil
	case []string:
		if len(input) == 0 {
			return 0, fmt.Errorf("input is empty")
		}
		for _, s := range input {
			if s == "" {
				return 0, fmt.Errorf("input is empty")
			}
		}
		return len(input), nil
	case []int:
		if len(input) == 0 {
			return 0, fmt.Errorf("input is empty")
		}
		for _, n := range input {
			if n < 0 {
				return 0, fmt.Errorf("invalid token input")
			}
		}
		return 1, nil
	case [][]int:
		if len(input) == 0 {
			return 0, fmt.Errorf("input is empty")
		}
		for _, tokens := range input {
			if _, err := (&EmbeddingRequest{Input: tokens}).InputCount(); err != nil {
				return 0, err
			}
		}
		return len(input), nil
	case []any:
		if len(input) == 0 {
			return 0, fmt.Errorf("input is empty")
		}
		if _, ok := input[0].(string); ok {
			for _, v := range input {
				s, ok := v.(string)
				if !ok || s == "" {
					return 0, fmt.Errorf("invalid text embedding batch")
				}
			}
			return len(input), nil
		}
		if _, ok := input[0].([]any); ok {
			for _, v := range input {
				tokens, ok := v.([]any)
				if !ok || len(tokens) == 0 {
					return 0, fmt.Errorf("invalid token embedding batch")
				}
				for _, token := range tokens {
					if !validEmbeddingToken(token) {
						return 0, fmt.Errorf("invalid token input")
					}
				}
			}
			return len(input), nil
		}
		for _, v := range input {
			if !validEmbeddingToken(v) {
				return 0, fmt.Errorf("invalid token input")
			}
		}
		return 1, nil
	default:
		return 0, fmt.Errorf("unsupported embedding input shape")
	}
}

func validEmbeddingToken(v any) bool {
	switch n := v.(type) {
	case float64:
		return n >= 0 && n <= math.MaxInt32 && math.Trunc(n) == n
	case int:
		return n >= 0 && int64(n) <= math.MaxInt32
	default:
		return false
	}
}
