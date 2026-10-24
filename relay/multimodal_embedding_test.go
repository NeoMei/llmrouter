package relay

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultimodalVectorCompleteContract(t *testing.T) {
	space := dto.EmbeddingSpace{Provider: "aliyun", Model: "qwen3-vl-embedding", Version: "v1", Dimensions: 256, InputSemantics: dto.QwenMultimodalInputSemantics}
	vector := make([]float64, 256)
	vector[0] = 0.25
	raw, e := common.Marshal(map[string]any{"output": map[string]any{"embeddings": []any{map[string]any{"index": 0, "embedding": vector, "type": "vl"}}}, "usage": map[string]any{"input_tokens": 0, "image_tokens": 0, "total_tokens": 0}})
	require.NoError(t, e)
	for _, tc := range []struct{ name, from, to string }{
		{"missing_type", `,"type":"vl"`, ``}, {"null_index", `"index":0`, `"index":null`}, {"null_vector", `"embedding":[0.25`, `"embedding":[null`},
		{"duplicate_usage", `"image_tokens":0`, `"image_tokens":0,"image_tokens":1`}, {"missing_total", `,"total_tokens":0`, ``}, {"inclusive_tongyi", `"image_tokens":0`, `"input_tokens_details":{"text_tokens":0,"image_tokens":0}`},
		{"fractional_usage", `"input_tokens":0`, `"input_tokens":0.5`}, {"huge_total", `"total_tokens":0`, `"total_tokens":9223372036854775808`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := strings.Replace(string(raw), tc.from, tc.to, 1)
			require.NotEqual(t, string(raw), bad)
			out, usage, e := normalizeMultimodalEmbedding([]byte(bad), space, 1, 1000, true, "public")
			assert.Error(t, e)
			assert.Nil(t, out)
			assert.Nil(t, usage)
		})
	}
	out, usage, e := normalizeMultimodalEmbedding(raw, space, 1, 0, true, "public")
	require.NoError(t, e)
	assert.NotEmpty(t, out)
	assert.Zero(t, usage.TotalTokens)
}
