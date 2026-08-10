package modelcatalog

import (
	"context"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func encodeStrings(values []string) string {
	data, _ := common.Marshal(values)
	return string(data)
}

type routeFixture struct {
	PublicID       string
	DisplayName    string
	Client         string
	Aliases        []string
	ChannelID      int
	UpstreamModel  string
	BillingModel   string
	PreferredWires []WireProtocol
	Groups         []string
	Enabled        bool
}

func newTestCatalog(t *testing.T, fixtures ...routeFixture) *Catalog {
	t.Helper()
	routes := make([]ClientModelRoute, 0, len(fixtures))
	targets := make([]ClientModelTarget, 0, len(fixtures))
	for i, fixture := range fixtures {
		enabled := fixture.Enabled
		if !fixture.Enabled {
			enabled = true
		}
		client := fixture.Client
		if client == "" {
			client = "all"
		}
		routes = append(routes, ClientModelRoute{
			Id:          i + 1,
			PublicId:    fixture.PublicID,
			DisplayName: fixture.DisplayName,
			Client:      client,
			Aliases:     encodeStrings(fixture.Aliases),
			Enabled:     enabled,
		})
		targets = append(targets, ClientModelTarget{
			Id:             i + 1,
			RouteId:        i + 1,
			ChannelId:      fixture.ChannelID,
			UpstreamModel:  fixture.UpstreamModel,
			BillingModel:   fixture.BillingModel,
			PreferredWires: encodeWires(fixture.PreferredWires),
			Enabled:        enabled,
			Weight:         1,
			Groups:         fixture.Groups,
		})
	}
	catalog, err := NewCatalog(routes, targets)
	require.NoError(t, err)
	return catalog.WithAuthorizer(testAuthorizer{})
}

func TestCatalogResolvesAliasBeforeBilling(t *testing.T) {
	catalog := newTestCatalog(t, routeFixture{
		PublicID:       "deepseek/deepseek-v4-flash",
		Aliases:        []string{"deepseek-v4-flash", "gpt-5.6-sol"},
		ChannelID:      7,
		UpstreamModel:  "deepseek-v4-flash",
		BillingModel:   "deepseek-v4-flash",
		PreferredWires: []WireProtocol{WireResponses, WireOpenAIChat},
	})
	got, err := catalog.Resolve(context.Background(), 11, []string{"default"}, "gpt-5.6-sol", "codex")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", got.RequestedModel)
	require.Equal(t, "deepseek/deepseek-v4-flash", got.PublicID)
	require.Equal(t, "deepseek-v4-flash", got.UpstreamModel)
	require.Equal(t, "deepseek-v4-flash", got.BillingModel)
	require.Equal(t, 7, got.ChannelID)
}

func TestCatalogRejectsAliasCollision(t *testing.T) {
	_, err := buildIndex([]routeFixture{
		{PublicID: "deepseek/a", Aliases: []string{"shared"}},
		{PublicID: "kimi/b", Aliases: []string{"shared"}},
	})
	require.ErrorContains(t, err, `alias "shared" is assigned to multiple public models`)
}

func TestCatalogRejectsDisabledTarget(t *testing.T) {
	_, err := NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: encodeWires([]WireProtocol{WireResponses}), Enabled: false}},
	)
	require.ErrorContains(t, err, "target 0 is disabled")
}

func TestCatalogRejectsUnknownWireAndMissingChannel(t *testing.T) {
	_, err := NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 0, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["future_wire"]`, Enabled: true}},
	)
	require.ErrorContains(t, err, "nonexistent channel")

	_, err = NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["future_wire"]`, Enabled: true}},
	)
	require.ErrorContains(t, err, "unknown wire protocol")
}

func TestCatalogRejectsInvalidIdentifiersAndMissingBilling(t *testing.T) {
	_, err := NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek\n/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses"]`, Enabled: true}},
	)
	require.ErrorContains(t, err, "control characters")

	_, err = NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", PreferredWires: `["responses"]`, Enabled: true}},
	)
	require.ErrorContains(t, err, "no billing configuration")

	_, err = NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses"]`, Weight: -1, Enabled: true}},
	)
	require.ErrorContains(t, err, "weight below one")

	_, err = NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses"]`, Weight: 0, Enabled: true}},
	)
	require.ErrorContains(t, err, "weight below one")
}

func TestCatalogRejectsEmptyAliasAndInvalidRouteIDs(t *testing.T) {
	_, err := NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Aliases: `["a", ""]`, Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses"]`, Weight: 1, Enabled: true}},
	)
	require.ErrorContains(t, err, "alias is required")

	_, err = NewCatalog(
		[]ClientModelRoute{{Id: 0, PublicId: "deepseek/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 0, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses"]`, Weight: 1, Enabled: true}},
	)
	require.ErrorContains(t, err, "route id")

	_, err = NewCatalog(
		[]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true}, {Id: 1, PublicId: "kimi/a", Enabled: true}},
		[]ClientModelTarget{{RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses"]`, Weight: 1, Enabled: true}},
	)
	require.ErrorContains(t, err, "route id 1 is duplicated")
}

func TestCatalogListHonorsVisibilityAndReturnsDefensiveCopies(t *testing.T) {
	catalog := newTestCatalog(t, routeFixture{
		PublicID: "deepseek/a", Aliases: []string{"a"}, ChannelID: 7,
		UpstreamModel: "a", BillingModel: "a", PreferredWires: []WireProtocol{WireResponses}, Groups: []string{"paid"},
	})
	models, err := catalog.List(context.Background(), 11, []string{"free"}, "codex")
	require.NoError(t, err)
	require.Empty(t, models)
	models, err = catalog.List(context.Background(), 11, nil, "codex")
	require.NoError(t, err)
	require.Empty(t, models)
	models, err = catalog.List(context.Background(), 11, []string{"paid"}, "codex")
	require.NoError(t, err)
	require.Len(t, models, 1)
	models[0].Aliases[0] = "mutated"
	modelsAgain, err := catalog.List(context.Background(), 11, []string{"paid"}, "codex")
	require.NoError(t, err)
	require.Equal(t, "a", modelsAgain[0].Aliases[0])

	resolved, err := catalog.Resolve(context.Background(), 11, []string{"paid"}, "a", "codex")
	require.NoError(t, err)
	require.Equal(t, 1, resolved.Weight)
	resolved.PreferredWires[0] = WireAnthropic
	resolvedAgain, err := catalog.Resolve(context.Background(), 11, []string{"paid"}, "a", "codex")
	require.NoError(t, err)
	require.Equal(t, WireResponses, resolvedAgain.PreferredWires[0])
}

func TestCatalogListExposesValidatedCapabilitiesAndVisibleWires(t *testing.T) {
	routes := []ClientModelRoute{{
		Id: 1, PublicId: "deepseek/a", Client: "codex", Aliases: `[]`, Enabled: true,
		Metadata: `{"context_window":131072,"reasoning_efforts":["low","high"],"supports_reasoning_summary":true,"supports_tools":true}`,
	}}
	targets := []ClientModelTarget{
		{Id: 1, RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses","openai_chat"]`, Weight: 1, Enabled: true, Groups: []string{"paid"}},
		{Id: 2, RouteId: 1, ChannelId: 8, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["anthropic","responses"]`, Weight: 1, Enabled: true, Groups: []string{"enterprise"}},
	}
	catalog, err := NewCatalog(routes, targets)
	require.NoError(t, err)
	catalog = catalog.WithAuthorizer(testAuthorizer{})

	models, err := catalog.List(context.Background(), 11, []string{"paid"}, "codex")
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, []WireProtocol{WireResponses, WireOpenAIChat}, models[0].PreferredWires)
	require.Equal(t, 131072, models[0].ContextWindow)
	require.Equal(t, []string{"low", "high"}, models[0].ReasoningEfforts)
	require.True(t, models[0].SupportsReasoningSummary)
	require.True(t, models[0].SupportsTools)

	models[0].PreferredWires[0] = WireAnthropic
	models[0].ReasoningEfforts[0] = "mutated"
	again, err := catalog.List(context.Background(), 11, []string{"paid"}, "codex")
	require.NoError(t, err)
	require.Equal(t, WireResponses, again[0].PreferredWires[0])
	require.Equal(t, "low", again[0].ReasoningEfforts[0])
}

func TestCatalogRejectsInvalidCapabilityMetadata(t *testing.T) {
	targets := []ClientModelTarget{{
		Id: 1, RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a",
		PreferredWires: `["responses"]`, Weight: 1, Enabled: true,
	}}
	for name, metadata := range map[string]string{
		"malformed JSON":          `{`,
		"negative context window": `{"context_window":-1}`,
		"blank reasoning effort":  `{"reasoning_efforts":["high",""]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewCatalog([]ClientModelRoute{{Id: 1, PublicId: "deepseek/a", Enabled: true, Metadata: metadata}}, targets)
			require.ErrorContains(t, err, "metadata")
		})
	}
}

type testAuthorizer struct{}

func (testAuthorizer) Authorize(_ context.Context, userID int, groups []string, client string) (AuthorizedRuntimeSnapshot, error) {
	if userID != 11 {
		return AuthorizedRuntimeSnapshot{}, fmt.Errorf("user %d is not authorized", userID)
	}
	return NewAuthorizedRuntimeSnapshot(userID, groups, client), nil
}

func TestCatalogRejectsUnauthorizedTenantWithAuthorizer(t *testing.T) {
	catalog := newTestCatalog(t, routeFixture{
		PublicID: "deepseek/a", ChannelID: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: []WireProtocol{WireResponses}, Groups: []string{"paid"},
	})
	catalog = catalog.WithAuthorizer(testAuthorizer{})
	_, err := catalog.Resolve(context.Background(), 12, []string{"paid"}, "deepseek/a", "codex")
	require.ErrorContains(t, err, "not authorized")
	models, err := catalog.List(context.Background(), 12, []string{"paid"}, "codex")
	require.ErrorContains(t, err, "not authorized")
	require.Nil(t, models)
}

type boundClientAuthorizer struct{}

func (boundClientAuthorizer) Authorize(_ context.Context, userID int, groups []string, _ string) (AuthorizedRuntimeSnapshot, error) {
	return NewAuthorizedRuntimeSnapshot(userID, groups, "claude"), nil
}

func TestCatalogDoesNotOverrideAuthorizerClientBinding(t *testing.T) {
	catalog := newTestCatalog(t, routeFixture{
		PublicID: "deepseek/a", Client: "claude", ChannelID: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: []WireProtocol{WireResponses},
	})
	catalog = catalog.WithAuthorizer(boundClientAuthorizer{})
	_, err := catalog.Resolve(context.Background(), 11, []string{"default"}, "deepseek/a", "codex")
	require.ErrorContains(t, err, `authorization snapshot is for client "claude", not "codex"`)
}

func TestCatalogRequiresAuthorizer(t *testing.T) {
	catalog, err := buildIndex([]routeFixture{{PublicID: "deepseek/a"}})
	require.NoError(t, err)
	models, err := catalog.List(context.Background(), 11, []string{"default"}, "codex")
	require.ErrorIs(t, err, ErrAuthorizerRequired)
	require.Nil(t, models)
	resolved, err := catalog.Resolve(context.Background(), 11, []string{"default"}, "deepseek/a", "codex")
	require.ErrorIs(t, err, ErrAuthorizerRequired)
	require.Nil(t, resolved)

	bound := catalog.WithAuthorizer(nil)
	models, err = bound.List(context.Background(), 11, []string{"default"}, "codex")
	require.ErrorIs(t, err, ErrAuthorizerRequired)
	require.Nil(t, models)
	resolved, err = bound.Resolve(context.Background(), 11, []string{"default"}, "deepseek/a", "codex")
	require.ErrorIs(t, err, ErrAuthorizerRequired)
	require.Nil(t, resolved)
}

func TestRuntimeAssemblyAppliesMappingAndFiltersUnavailableTargets(t *testing.T) {
	routes := []ClientModelRoute{{Id: 1, PublicId: "deepseek/deepseek-v4-flash", Client: "codex", Enabled: true}}
	targets := []ClientModelTarget{
		{Id: 1, RouteId: 1, ChannelId: 7, UpstreamModel: "deepseek-v4-flash", BillingModel: "", PreferredWires: `["responses"]`, Weight: 1, Enabled: true},
		{Id: 2, RouteId: 1, ChannelId: 8, UpstreamModel: "stale", BillingModel: "stale", PreferredWires: `["responses"]`, Weight: 1, Enabled: true},
	}
	// The first target is remapped to a priced canonical model; the second
	// target has no enabled channel/ability and must not leak into the snapshot.
	sources := runtimeFixtureSources{
		channels: map[int]RuntimeChannel{
			7: {ID: 7, Enabled: true, ModelMapping: `{"deepseek-v4-flash":"deepseek-v4-flash"}`},
			8: {ID: 8, Enabled: false},
		},
		abilities: map[int][]RuntimeAbility{7: {{Group: "default", Model: "deepseek-v4-flash", Enabled: true, Priority: 9, Weight: 3}}},
		priced:    map[string]string{"deepseek-v4-flash": "deepseek-v4-flash"},
	}
	catalog, err := AssembleRuntimeCatalog(context.Background(), RuntimeContext{UserID: 11, Groups: []string{"default"}, Client: "codex"}, routes, targets, sources, testAuthorizer{})
	require.NoError(t, err)
	resolved, err := catalog.Resolve(context.Background(), 11, []string{"default"}, "deepseek/deepseek-v4-flash", "codex")
	require.NoError(t, err)
	require.Equal(t, "deepseek-v4-flash", resolved.UpstreamModel)
	require.Equal(t, "deepseek-v4-flash", resolved.BillingModel)
	require.Equal(t, 9, resolved.Priority)
	require.Equal(t, 3, resolved.Weight)
}

func TestRuntimeAssemblyRequiresAuthorizer(t *testing.T) {
	sources := runtimeFixtureSources{}
	_, err := AssembleRuntimeCatalog(context.Background(), RuntimeContext{UserID: 11}, nil, nil, sources, nil)
	require.ErrorIs(t, err, ErrAuthorizerRequired)
}

type runtimeFixtureSources struct {
	channels  map[int]RuntimeChannel
	abilities map[int][]RuntimeAbility
	priced    map[string]string
}

func (s runtimeFixtureSources) Channel(_ context.Context, id int) (RuntimeChannel, bool, error) {
	channel, ok := s.channels[id]
	return channel, ok, nil
}

func (s runtimeFixtureSources) Abilities(_ context.Context, channelID int, _ string) ([]RuntimeAbility, error) {
	return s.abilities[channelID], nil
}

func (s runtimeFixtureSources) CanonicalBillingModel(_ context.Context, model string) (string, bool, error) {
	canonical, ok := s.priced[model]
	return canonical, ok, nil
}

func buildIndex(fixtures []routeFixture) (*Catalog, error) {
	routes := make([]ClientModelRoute, 0, len(fixtures))
	targets := make([]ClientModelTarget, 0, len(fixtures))
	for i, fixture := range fixtures {
		routes = append(routes, ClientModelRoute{
			Id:       i + 1,
			PublicId: fixture.PublicID,
			Aliases:  encodeStrings(fixture.Aliases),
			Enabled:  true,
		})
		targets = append(targets, ClientModelTarget{
			RouteId:        i + 1,
			ChannelId:      1,
			UpstreamModel:  "upstream",
			BillingModel:   "billing",
			PreferredWires: encodeWires([]WireProtocol{WireResponses}),
			Enabled:        true,
			Weight:         1,
		})
	}
	return NewCatalog(routes, targets)
}
