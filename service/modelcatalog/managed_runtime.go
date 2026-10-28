package modelcatalog

import (
	"context"
	"strings"
)

// Managed routing uses UniHub sale tariffs. The native monetary price lookup
// remains unchanged for native callers; this source only preserves the route's
// billing identity while all adapter/ability/visibility validation still runs.
type managedRuntimeSources struct{ RuntimeSources }

func (s managedRuntimeSources) CanonicalBillingModel(_ context.Context, name string) (string, bool, error) {
	name = strings.TrimSpace(name)
	return name, name != "", nil
}
func AssembleManagedRuntimeCatalog(ctx context.Context, runtime RuntimeContext, routes []ClientModelRoute, targets []ClientModelTarget, sources RuntimeSources, auth VisibilityAuthorizer) (*Catalog, error) {
	if sources == nil {
		return AssembleRuntimeCatalog(ctx, runtime, routes, targets, nil, auth)
	}
	return AssembleRuntimeCatalog(ctx, runtime, routes, targets, managedRuntimeSources{sources}, auth)
}
