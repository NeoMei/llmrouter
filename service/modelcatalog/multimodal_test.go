package modelcatalog

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogMultimodalMetadataContract(t *testing.T) {
	for _, metadata := range []string{
		`{"input_modalities":["smell"]}`,
		`{"operations":["invented.ocr"]}`,
		`{"media_limits":{"max_images":-1}}`,
		`{"media_limits":{"max_video_seconds":0}}`,
		`{"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":0,"input_semantics":"text"}}`,
	} {
		t.Run(metadata, func(t *testing.T) {
			_, err := NewCatalog([]ClientModelRoute{{Id: 1, PublicId: "public", Enabled: true, Metadata: metadata}}, []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "text-embedding-v4", BillingModel: "text-embedding-v4", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}})
			require.Error(t, err)
		})
	}
	catalog, err := NewCatalog([]ClientModelRoute{{Id: 1, PublicId: "public", Enabled: true, Metadata: `{"input_modalities":[" TEXT ","text"],"operations":["embeddings.create"],"streaming":false}`}}, []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "text-embedding-v4", BillingModel: "text-embedding-v4", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}})
	require.NoError(t, err)
	models, err := catalog.WithAuthorizer(testAuthorizer{}).List(context.Background(), 11, []string{"default"}, "")
	require.NoError(t, err)
	body, err := common.Marshal(models)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"input_modalities":["text"]`)
	assert.Contains(t, string(body), `"streaming":false`)
}

func TestCatalogEmbeddingSpaceRejectsCandidateDrift(t *testing.T) {
	const space = `{"provider":"aliyun","model":"text-embedding-v4","version":"v4-2026","dimensions":1024,"input_semantics":"text/query-document-v1"}`
	for _, tc := range []struct {
		name, upstream, metadata string
		invalid                  bool
	}{
		{"inherit", "text-embedding-v4", "", false},
		{"explicit", "text-embedding-v4", `{"embedding_space":` + space + `}`, false},
		{"other model", "qwen3-vl-embedding", "", true},
		{"same dimensions other version", "text-embedding-v4", `{"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v5","dimensions":1024,"input_semantics":"text/query-document-v1"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCatalog([]ClientModelRoute{{Id: 1, PublicId: "fixed-space", Enabled: true, Metadata: `{"embedding_space":` + space + `}`}}, []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: tc.upstream, BillingModel: "text-embedding-v4", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true, Metadata: tc.metadata}})
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRuntimeEmbeddingSpaceChecksMappedModelAndAdapterOperation(t *testing.T) {
	routes := []ClientModelRoute{{Id: 1, PublicId: "fixed", Enabled: true, Metadata: `{"operations":["embeddings.create"],"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text/query-document-v1"}}`}}
	targets := []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 7, UpstreamModel: "configured-name", BillingModel: "text-embedding-v4", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}}
	for _, tc := range []struct {
		name, mapped string
		channelType  int
		invalid      bool
	}{
		{"same actual model", "text-embedding-v4", 1, false},
		{"mapping drift", "other-vector", 1, true},
		{"unsupported operation", "text-embedding-v4", 14, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := runtimeFixtureSources{channels: map[int]RuntimeChannel{7: {ID: 7, Type: tc.channelType, Enabled: true, ModelMapping: `{"configured-name":"` + tc.mapped + `"}`}}, abilities: map[int][]RuntimeAbility{7: {{Group: "default", Model: tc.mapped, Enabled: true, Weight: 1}}}, priced: map[string]string{"text-embedding-v4": "text-embedding-v4"}}
			catalog, err := AssembleRuntimeCatalog(context.Background(), RuntimeContext{UserID: 11, Groups: []string{"default"}}, routes, targets, sources, testAuthorizer{})
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			candidates, err := catalog.ResolveCandidates(context.Background(), 11, []string{"default"}, "fixed", "")
			require.NoError(t, err)
			require.Len(t, candidates, 1)
			assert.Equal(t, 1024, candidates[0].Capabilities.EmbeddingSpace.Dimensions)
			candidates[0].Capabilities.EmbeddingSpace.Version = "caller-mutated"
			next, err := catalog.Resolve(context.Background(), 11, []string{"default"}, "fixed", "")
			require.NoError(t, err)
			assert.Equal(t, "v4", next.Capabilities.EmbeddingSpace.Version)
		})
	}
}

func TestCatalogRejectsIgnoredDeclaredLimitsAndTargetCapabilities(t *testing.T) {
	for _, raw := range []string{`{"media_limits":{"max_vidoe_seconds":10}}`, `{"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text","preprocesssing":"ignored"}}`} {
		_, err := NormalizeModelCapabilities(raw)
		require.Error(t, err)
	}
	for _, raw := range []string{`{"input_modalities":["image"]}`, `{"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text"}}`} {
		_, err := NewCatalog([]ClientModelRoute{{Id: 1, PublicId: "legacy", Enabled: true}}, []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "text-embedding-v4", BillingModel: "text-embedding-v4", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1, Metadata: raw}})
		require.Error(t, err)
	}
}
