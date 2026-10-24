package helper

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultimodalEmbeddingTypedInput(t *testing.T) {
	for _, native := range []bool{false, true} {
		raw := `{"model":"public","input":[{"text":"hello"}],"dimensions":1024,"fps":1,"enable_fusion":false}`
		if native {
			raw = `{"model":"public","input":{"contents":[{"text":"hello"}]},"parameters":{"dimension":1024,"fps":1,"enable_fusion":false}}`
		}
		r, err := ParseMultimodalEmbeddingRequest([]byte(raw), native)
		require.NoError(t, err)
		require.NotNil(t, r.FPS)
		assert.Equal(t, 1.0, *r.FPS)
		require.NotNil(t, r.EnableFusion)
		assert.False(t, *r.EnableFusion)
		assert.Len(t, r.Input, 1)
	}
	for _, raw := range []string{
		`{"model":"p","input":[{"audio":"x"}]}`,
		`{"model":"p","input":[{"text":"x"}],"fps":0}`,
		`{"model":"p","input":[{"text":"x"}],"enable_fusion":true}`,
		`{"model":"p","input":[{"text":"x","image":"x"}]}`,
		`{"model":"p","input":[{"text":"x"}],"model":"other"}`,
		`{"model":"p","input":[{"text":"x"}],"instructions":"x"}`,
		`{"model":"p","input":[{"text":null}]}`,
	} {
		_, err := ParseMultimodalEmbeddingRequest([]byte(raw), false)
		assert.Error(t, err, raw)
	}
	s := dto.EmbeddingSpace{Provider: "aliyun", Model: "qwen3-vl-embedding", Version: "operator-v1", Dimensions: 1024, InputSemantics: dto.QwenMultimodalInputSemantics}
	require.NoError(t, ValidateMultimodalEmbeddingSpace(&dto.MultimodalEmbeddingRequest{Input: []dto.MultimodalEmbeddingContent{{Text: "hello"}}}, &s))
	s.InputSemantics = "text"
	assert.Error(t, ValidateMultimodalEmbeddingSpace(&dto.MultimodalEmbeddingRequest{}, &s))
}

func TestMultimodalNativeStrictFields(t *testing.T) {
	for _, raw := range []string{
		`{"model":"p","input":{"contents":[{"text":"x"}],"instruct":"x"}}`,
		`{"model":"p","input":{"contents":[{"text":"x"}]},"parameters":{"fps":0}}`,
		`{"model":"p","input":{"contents":[{"text":"x"}]},"parameters":{"enable_fusion":true}}`,
		`{"model":"p","input":{"contents":[{"text":"x"}]},"parameters":{"dimension":null}}`,
		`{"model":"p","input":{"contents":[{"text":"x","text":"y"}]}}`,
		`{"model":"p","input":{"contents":[{"multi_images":["x","y"]}]}}`,
		`{"model":"p","input":{"contents":[{"video":"x","fps":0}]}}`,
	} {
		_, err := ParseMultimodalEmbeddingRequest([]byte(raw), true)
		assert.Error(t, err, raw)
	}
}
