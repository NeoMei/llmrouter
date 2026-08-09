package capability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicRouteManifestHasNoDuplicateAndCoversOperations(t *testing.T) {
	routes := PublicRouteManifest()
	require.NotEmpty(t, routes)

	seenRoutes := make(map[string]struct{}, len(routes))
	seenOperations := make(map[Operation]struct{})
	for _, route := range routes {
		key := route.Method + " " + route.Pattern
		_, duplicate := seenRoutes[key]
		assert.Falsef(t, duplicate, "duplicate route %s", key)
		seenRoutes[key] = struct{}{}
		seenOperations[route.Operation] = struct{}{}
		assert.Equal(t, ProtocolForOperation(route.Operation), route.Protocol)
	}
	for _, operation := range AllOperations() {
		_, ok := seenOperations[operation]
		assert.Truef(t, ok, "operation %s has no public route", operation)
	}
}
