package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func knowledgePolicy() KnowledgePolicy {
	return KnowledgePolicy{ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Version: "knowledge_v1", ProviderReference: "https://example.test/rerank-policy", PublicModel: "rank", WireOperation: "rerank.create", ChannelID: 1, ChannelType: constant.ChannelTypeAli, UpstreamModel: "qwen3-rerank", HardInputTokens: 120000, MaxItemTokens: 4000, MaxBatch: 500, RateIDs: map[string]string{"CREDIT/6": "rate"}}
}
func TestKnowledgeRerankStrictAdmission(t *testing.T) {
	p := knowledgePolicy()
	c := Config{MaxInputTokens: 120000, MaxBodyBytes: 1 << 20}
	for _, tc := range []struct {
		native bool
		body   string
	}{
		{false, `{"model":"rank","query":"q","documents":["a","b"],"top_n":1,"return_documents":false}`},
		{true, `{"model":"rank","input":{"query":"q","documents":["a","b"]},"parameters":{"top_n":1,"return_documents":false}}`},
	} {
		r, e := parseKnowledgeRerank(c, p, []byte(tc.body), tc.native)
		require.NoError(t, e)
		assert.Equal(t, 120000, r.Quantity)
		var f frozenKnowledge
		require.NoError(t, common.UnmarshalUnique([]byte(r.MediaSnapshot), &f))
		assert.Contains(t, string(f.Body), `"return_documents":false`)
		assert.Contains(t, string(f.Body), `"documents":["a","b"]`)
	}
	for _, body := range []string{`{"model":"rank","query":"q","documents":[]}`, `{"model":"rank","query":"q","documents":["a"],"top_n":2}`, `{"model":"rank","query":"q","documents":["a"],"return_documents":null}`, `{"model":"rank","query":"q","documents":["a"],"max_chunk_per_doc":2}`, `{"model":"rank","query":"q","query":"x","documents":["a"]}`} {
		_, e := parseKnowledgeRerank(c, p, []byte(body), false)
		require.Error(t, e)
	}
	p.MaxBatch = 1
	_, e := parseKnowledgeRerank(c, p, []byte(`{"model":"rank","query":"q","documents":["a","b"]}`), false)
	require.Error(t, e)
	p = knowledgePolicy()
	p.MaxItemTokens = 1
	_, e = parseKnowledgeRerank(c, p, []byte(`{"model":"rank","query":"long","documents":["a"]}`), false)
	require.Error(t, e)
	p = knowledgePolicy()
	p.HardInputTokens = 4
	_, e = parseKnowledgeRerank(c, p, []byte(`{"model":"rank","query":"qq","documents":["a","b"]}`), false)
	require.Error(t, e)
}
