package modelcatalog

import (
	"context"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestManagedTechnicalCatalogDoesNotRequireNativeSalePrice(t *testing.T) {
	ctx := context.Background()
	runtime := RuntimeContext{UserID: 11, Groups: []string{"platform"}, Client: "superwagie"}
	routes := []ClientModelRoute{{Id: 1, PublicId: "chat", DisplayName: "Chat", Client: "all", Enabled: true, Metadata: `{"operations":["chat.completions.create"],"supports_tools":true}`}}
	targets := []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "chat", PreferredWires: `["openai_chat"]`, Enabled: true}}
	src := runtimeFixtureSources{channels: map[int]RuntimeChannel{1: {ID: 1, Type: constant.ChannelTypeOpenAI, Enabled: true}}, abilities: map[int][]RuntimeAbility{1: {{Group: "platform", Model: "chat", Enabled: true}}}}
	native, err := AssembleRuntimeCatalog(ctx, runtime, routes, targets, src, testAuthorizer{})
	require.NoError(t, err)
	empty, err := native.List(ctx, 11, runtime.Groups, runtime.Client)
	require.NoError(t, err)
	assert.Empty(t, empty)
	managed, err := AssembleManagedRuntimeCatalog(ctx, runtime, routes, targets, src, testAuthorizer{})
	require.NoError(t, err)
	models, err := managed.List(ctx, 11, runtime.Groups, runtime.Client)
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.True(t, models[0].SupportsTools)
	src.abilities[1][0].Enabled = false
	managed, err = AssembleManagedRuntimeCatalog(ctx, runtime, routes, targets, src, testAuthorizer{})
	require.NoError(t, err)
	models, err = managed.List(ctx, 11, runtime.Groups, runtime.Client)
	require.NoError(t, err)
	assert.Empty(t, models)
}
