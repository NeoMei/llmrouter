package contracts_test

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/stretchr/testify/require"
)

type providerFixtureManifest struct {
	RequiredScenarios []string `json:"required_scenarios"`
	Operations        []string `json:"operations"`
}

func TestEverySupportedCapabilityHasProviderFixture(t *testing.T) {
	payload, err := os.ReadFile("provider_fixtures.json")
	require.NoError(t, err)
	var fixtures providerFixtureManifest
	require.NoError(t, json.Unmarshal(payload, &fixtures))
	require.ElementsMatch(t, []string{"success", "error", "timeout", "malformed", "retry", "billing"}, fixtures.RequiredScenarios)

	supported := map[string]struct{}{}
	for key, decision := range capability.DefaultRegistry().Snapshot() {
		if decision.Supported {
			supported[string(key.Operation)] = struct{}{}
		}
	}
	want := make([]string, 0, len(supported))
	for operation := range supported {
		want = append(want, operation)
	}
	sort.Strings(want)
	sort.Strings(fixtures.Operations)
	require.Equal(t, want, fixtures.Operations)
}

func TestAllPublicResourceRoutesHaveE2ECase(t *testing.T) {
	payload, err := os.ReadFile("resource_routes.json")
	require.NoError(t, err)
	var fixtures []capability.RouteManifestEntry
	require.NoError(t, json.Unmarshal(payload, &fixtures))

	resourceOperations := map[capability.Operation]bool{
		capability.OperationModelDelete: true, capability.OperationImageVariation: true,
	}

	want := make([]capability.RouteManifestEntry, 0, len(fixtures))
	for _, route := range capability.PublicRouteManifest() {
		if resourceOperations[route.Operation] {
			want = append(want, route)
		}
	}
	require.ElementsMatch(t, want, fixtures)
}
