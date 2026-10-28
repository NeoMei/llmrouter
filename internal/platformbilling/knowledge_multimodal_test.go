package platformbilling

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKnowledgeMultimodalIndependentReservation(t *testing.T) {
	price := metering.Price{Version: "metered_v2", Operation: "multimodal_embedding", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "text", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "3"}}, {Key: "visual", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "3"}}}}
	value, e := meteredReserve(price, operationRequest{Quantity: 2, Output: 3})
	require.NoError(t, e)
	assert.Equal(t, "3", value)
}
func TestKnowledgeMultimodalStrictAdmission(t *testing.T) {
	p := knowledgePolicy()
	p.PublicModel = "mm"
	p.WireOperation = "multimodal.embeddings.create"
	p.UpstreamModel = "qwen3-vl-embedding"
	p.HardInputTokens = 100
	p.VisualTokens = 200
	p.MaxBatch = 5
	p.Space = &dto.EmbeddingSpace{Provider: "aliyun", Model: p.UpstreamModel, Version: "v1", Dimensions: 256, InputSemantics: dto.QwenMultimodalInputSemantics}
	c := Config{MaxInputTokens: 1000, MaxBodyBytes: 1 << 20}
	for _, tc := range []struct {
		native bool
		body   string
	}{{false, `{"model":"mm","input":[{"text":"a"}],"dimensions":256,"fps":1,"enable_fusion":false}`}, {true, `{"model":"mm","input":{"contents":[{"text":"a"}]},"parameters":{"dimension":256,"fps":1,"enable_fusion":false}}`}} {
		r, e := parseKnowledgeMultimodal(c, p, []byte(tc.body), tc.native)
		require.NoError(t, e)
		assert.Equal(t, 100, r.Quantity)
		assert.Equal(t, 200, r.Output)
	}
	for _, body := range []string{`{"model":"mm","input":[{"text":"a","image":"https://x"}]}`, `{"model":"mm","input":[{"text":"a"}],"fps":null}`, `{"model":"mm","input":[{"text":"a"}],"enable_fusion":true}`, `{"model":"mm","input":[{"text":"a"}],"dimensions":512}`, `{"model":"mm","input":[{"text":"a"}],"callback_url":"x"}`} {
		_, e := parseKnowledgeMultimodal(c, p, []byte(body), false)
		require.Error(t, e)
	}
}
