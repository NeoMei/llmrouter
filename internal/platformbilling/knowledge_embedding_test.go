package platformbilling

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKnowledgeEmbeddingAdmissionShapes(t *testing.T) {
	c := Config{MaxInputTokens: 64}
	for _, input := range []string{`"hello"`, `["hello","world"]`, `[0,1,2]`, `[[0],[1,2]]`} {
		t.Run(input, func(t *testing.T) {
			r, e := parseOperation(c, "embedding", "application/json", []byte(`{"model":"test","input":`+input+`}`))
			require.NoError(t, e)
			assert.Equal(t, 64, r.Quantity)
		})
	}
	for _, suffix := range []string{`,"encoding_format dimensions":123`, `,"encoding_format":"base64"`, `,"dimensions":null`, `,"dimensions":0`, `,"user":123`, `,"input_type":"query"`} {
		_, e := parseOperation(c, "embedding", "application/json", []byte(`{"model":"test","input":"hello"`+suffix+`}`))
		require.Error(t, e)
	}
}

func TestKnowledgeEmbeddingCertifiedBounds(t *testing.T) {
	p := knowledgePolicy()
	p.PublicModel = "embed"
	p.WireOperation = "embeddings.create"
	p.ChannelType = constant.ChannelTypeOpenAI
	p.UpstreamModel = "upstream-embed"
	p.HardInputTokens = 8
	p.MaxItemTokens = 4
	p.MaxBatch = 2
	p.Space = &dto.EmbeddingSpace{Provider: "openai", Model: p.UpstreamModel, Version: "v1", Dimensions: 3, InputSemantics: "text"}
	c := Config{MaxInputTokens: 64, MaxBodyBytes: 65536}
	for _, input := range []string{`"a"`, `["a","b"]`, `[0,1,2,3]`, `[[0,1],[2,3]]`} {
		r, e := parseKnowledgeEmbedding(c, p, []byte(`{"model":"embed","input":`+input+`}`))
		require.NoError(t, e)
		assert.Equal(t, 8, r.Quantity)
		assert.Contains(t, r.MediaSnapshot, `"dimensions":3`)
	}
	for _, input := range []string{`"abc"`, `[0,1,2,3,4]`, `["a","b","c"]`, `[[0,1,2,3,4]]`} {
		_, e := parseKnowledgeEmbedding(c, p, []byte(`{"model":"embed","input":`+input+`}`))
		require.Error(t, e)
	}
	_, e := parseKnowledgeEmbedding(c, p, []byte(`{"model":"embed","input":"a","dimensions":2}`))
	require.Error(t, e)
	p.Space = nil
	_, e = parseKnowledgeEmbedding(c, p, []byte(`{"model":"embed","input":"a"}`))
	require.Error(t, e)
}
