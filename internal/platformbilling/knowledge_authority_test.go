package platformbilling

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKnowledgeAuthorityProducerAndCatalog(t *testing.T) {
	for _, tc := range []struct {
		wire, bill, path string
		format           types.RelayFormat
	}{{"rerank.create", "rerank", "/v1/rerank", types.RelayFormatRerank}, {"multimodal.embeddings.create", "multimodal_embedding", "/v1/multimodal/embeddings", types.RelayFormatMultimodalEmbedding}} {
		assert.Equal(t, tc.bill, wireBillingOperation(tc.wire))
		assert.Equal(t, tc.wire, legacyWireOperation(tc.bill))
		f, ok := TokenProducer(tc.wire, constant.ChannelTypeAli)
		assert.True(t, ok)
		assert.Equal(t, tc.format, f)
		_, ok = TokenProducer(tc.wire, constant.ChannelTypeCohere)
		assert.False(t, ok)
		a := &Adapter{config: Config{}}
		catalog, e := a.ProjectCatalog("superwagie", []CatalogSourceModel{{PublicID: "test", ModelCapabilities: dto.ModelCapabilities{Operations: []string{tc.wire}}}})
		require.NoError(t, e)
		require.Len(t, catalog.Models[0].Operations, 1)
		op := catalog.Models[0].Operations[0]
		assert.Equal(t, tc.path, op.Endpoint)
		assert.Equal(t, tc.bill, op.BillingOperation)
		assert.False(t, op.Available)
	}
}

func TestKnowledgeCatalogCertifiedBoundsAndPins(t *testing.T) {
	p := knowledgePolicy()
	p.HardInputTokens = 1000
	a := &Adapter{config: Config{Enabled: true, MaxInputTokens: 1000, MaxBodyBytes: 4096, KnowledgePolicies: map[string]map[string]KnowledgePolicy{"rerank.create": {"rank": p}}, OperationLimits: map[string]map[string]OperationLimits{"rerank": {"rank": {Policy: "provider_reported_v1", MaxBodyBytes: 1 << 20}}}}}
	out, e := a.ProjectCatalog("superwagie", []CatalogSourceModel{{PublicID: "rank", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"rerank.create"}}}})
	require.NoError(t, e)
	op := out.Models[0].Operations[0]
	assert.Equal(t, 1000, op.Limits.MaxQuantity)
	assert.EqualValues(t, 4096, op.Limits.MaxBodyBytes)
	assert.Equal(t, "rate", op.RateIDs["CREDIT/6"])
}
