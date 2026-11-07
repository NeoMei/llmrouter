package router

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogEmbeddingAttributionPreservesRoutingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, id, display, provider, upstream, want string
	}{
		{"legacy alias", "unihub-text-embedding-v4", "Text Embedding V4", "aliyun", "text-embedding-v4", "Alibaba / text-embedding-v4"},
		{"metadata rather than ID prefix", "workspace-embedding", "Embedding", "aliyun", "text-embedding-v4", "Alibaba / text-embedding-v4"},
		{"misleading vendor name", "unihub-text-embedding-v4", "UniHub text-embedding-v4", "aliyun", "text-embedding-v4", "Alibaba / text-embedding-v4"},
		{"canonical display", "text-embedding-v4", "Alibaba text-embedding-v4 (1024)", "aliyun", "text-embedding-v4", "Alibaba text-embedding-v4 (1024)"},
		{"unknown provider", "unihub-text-embedding-v4", "Text Embedding V4", "unknown", "text-embedding-v4", "Text Embedding V4"},
		{"missing metadata", "unihub-text-embedding-v4", "Text Embedding V4", "", "text-embedding-v4", "Text Embedding V4"},
		{"unrelated model", "other-embedding", "Custom Embedding V3", "aliyun", "text-embedding-v3", "Custom Embedding V3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, db := providerCatalogFixture(t)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			prices, err := common.Marshal(map[string]float64{tc.upstream: 0.001})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(prices)))
			channel := model.Channel{Id: 2, Type: constant.ChannelTypeAli, Key: "fixture-key", Status: common.ChannelStatusEnabled, Models: tc.upstream, Group: "native,legacy"}
			require.NoError(t, db.Create(&channel).Error)
			for _, group := range []string{"native", "legacy"} {
				require.NoError(t, db.Create(&model.Ability{Group: group, Model: tc.upstream, ChannelId: 2, Enabled: true}).Error)
			}
			caps := dto.ModelCapabilities{Operations: []string{"embeddings.create"}, InputModalities: []string{"text"}, OutputModalities: []string{"embedding"}}
			if tc.provider != "" {
				caps.EmbeddingSpace = &dto.EmbeddingSpace{Provider: tc.provider, Model: tc.upstream, Version: "v4", Dimensions: 1024, InputSemantics: "text"}
			}
			metadata, err := common.Marshal(caps)
			require.NoError(t, err)
			route := model.ClientModelRoute{Id: 10, PublicId: tc.id, DisplayName: tc.display, Client: "all", Enabled: true, Aliases: "[]", Metadata: string(metadata)}
			require.NoError(t, db.Create(&route).Error)
			target := model.ClientModelTarget{Id: 10, RouteId: 10, ChannelId: 2, UpstreamModel: tc.upstream, BillingModel: tc.upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}
			require.NoError(t, db.Create(&target).Error)
			require.NoError(t, db.First(&route, 10).Error)
			if tc.id == "unihub-text-embedding-v4" {
				require.NotEmpty(t, route.KnowledgeGeneration)
			}

			technical, err := readTechnicalCatalog(context.Background(), adapter, "superwagie")
			require.NoError(t, err)
			require.Len(t, technical.Models, 1)
			assert.Equal(t, tc.id, technical.Models[0].ID)
			assert.Equal(t, tc.want, technical.Models[0].Name)
			assert.Equal(t, caps.EmbeddingSpace, technical.Models[0].Capabilities.EmbeddingSpace)

			native, err := readProviderCatalog(context.Background(), adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
			require.NoError(t, err)
			var projected *ProviderCatalogModel
			for i := range native.Models {
				if native.Models[i].ID == tc.id {
					projected = &native.Models[i]
				}
			}
			require.NotNil(t, projected, "the existing public ID remains discoverable")
			assert.Equal(t, tc.want, projected.Name)
			assert.Equal(t, tc.id, projected.CapabilityMetadata.ID)
			assert.Equal(t, tc.want, projected.CapabilityMetadata.Name)
			assert.Equal(t, caps.EmbeddingSpace, projected.CapabilityMetadata.Capabilities.EmbeddingSpace)

			var persistedRoute model.ClientModelRoute
			var persistedTarget model.ClientModelTarget
			var persistedChannel model.Channel
			require.NoError(t, db.First(&persistedRoute, 10).Error)
			require.NoError(t, db.First(&persistedTarget, 10).Error)
			require.NoError(t, db.First(&persistedChannel, 2).Error)
			assert.Equal(t, route, persistedRoute, "catalog naming must not rotate the durable knowledge generation or rewrite metadata")
			assert.Equal(t, target, persistedTarget)
			assert.Equal(t, channel, persistedChannel)
		})
	}
}
