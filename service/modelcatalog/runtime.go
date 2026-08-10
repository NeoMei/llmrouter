package modelcatalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// RuntimeContext is the caller identity used when assembling a client catalog.
// The assembly boundary deliberately carries tenant/group/client information so
// a raw database snapshot can never be mistaken for an authorized catalog.
type RuntimeContext struct {
	UserID int
	Groups []string
	Client string
}

// RuntimeChannel is the non-secret channel state needed by the catalog. Keys
// are intentionally absent; the management API must never expose them.
type RuntimeChannel struct {
	ID           int
	Enabled      bool
	ModelMapping string
}

// RuntimeAbility is the existing channel/ability selection state projected at
// the model-catalog boundary.
type RuntimeAbility struct {
	Group    string
	Model    string
	Enabled  bool
	Priority int64
	Weight   uint
}

// RuntimeSources supplies live channel/ability/pricing state. Keeping this
// interface small makes the assembly deterministic in tests and allows the
// production adapter to load from GORM without leaking database details into
// the immutable Catalog.
type RuntimeSources interface {
	Channel(context.Context, int) (RuntimeChannel, bool, error)
	Abilities(context.Context, int, string) ([]RuntimeAbility, error)
	CanonicalBillingModel(context.Context, string) (string, bool, error)
}

// AssembleRuntimeCatalog filters stale targets, applies channel model mapping,
// resolves canonical billing names, and carries the existing priority/weight
// semantics into the immutable Catalog. An authorizer is mandatory even when
// the caller already has a user/group context; Catalog.List/Resolve will invoke
// it again at request time.
func AssembleRuntimeCatalog(
	ctx context.Context,
	runtime RuntimeContext,
	routes []ClientModelRoute,
	targets []ClientModelTarget,
	sources RuntimeSources,
	authorizer VisibilityAuthorizer,
) (*Catalog, error) {
	if authorizer == nil {
		return nil, ErrAuthorizerRequired
	}
	if sources == nil {
		return nil, fmt.Errorf("model catalog runtime sources are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := authorizer.Authorize(ctx, runtime.UserID, runtime.Groups, runtime.Client); err != nil {
		return nil, err
	}

	routeByID := make(map[int]ClientModelRoute, len(routes))
	for _, route := range routes {
		if route.Enabled {
			routeByID[route.Id] = route
		}
	}
	assembled := make([]ClientModelTarget, 0, len(targets))
	activeRoutes := make(map[int]struct{}, len(routeByID))
	for _, raw := range targets {
		if !raw.Enabled {
			continue
		}
		if _, ok := routeByID[raw.RouteId]; !ok {
			continue
		}
		channel, ok, err := sources.Channel(ctx, raw.ChannelId)
		if err != nil {
			return nil, err
		}
		if !ok || !channel.Enabled {
			continue
		}
		upstream := applyModelMapping(channel.ModelMapping, strings.TrimSpace(raw.UpstreamModel))
		if upstream == "" {
			continue
		}
		abilities, err := sources.Abilities(ctx, raw.ChannelId, upstream)
		if err != nil {
			return nil, err
		}
		groups := make([]string, 0, len(abilities))
		var maxPriority int64
		var maxWeight uint
		for _, ability := range abilities {
			if !ability.Enabled || !strings.EqualFold(strings.TrimSpace(ability.Model), upstream) {
				continue
			}
			group := strings.TrimSpace(ability.Group)
			if group != "" {
				groups = append(groups, group)
			}
			if ability.Priority > maxPriority {
				maxPriority = ability.Priority
			}
			if ability.Weight > maxWeight {
				maxWeight = ability.Weight
			}
		}
		if len(groups) == 0 {
			continue
		}
		billing := strings.TrimSpace(raw.BillingModel)
		if billing == "" {
			billing = upstream
		}
		canonical, priced, err := sources.CanonicalBillingModel(ctx, billing)
		if err != nil {
			return nil, err
		}
		if !priced || strings.TrimSpace(canonical) == "" {
			continue
		}
		raw.UpstreamModel = upstream
		raw.BillingModel = strings.TrimSpace(canonical)
		raw.Groups = normalizeIDs(groups)
		if maxPriority > 0 {
			raw.Priority = int(maxPriority)
		}
		if maxWeight > 0 {
			raw.Weight = int(maxWeight)
		}
		if raw.Weight <= 0 {
			raw.Weight = 1
		}
		assembled = append(assembled, raw)
		activeRoutes[raw.RouteId] = struct{}{}
	}
	active := make([]ClientModelRoute, 0, len(activeRoutes))
	for id := range activeRoutes {
		active = append(active, routeByID[id])
	}
	catalog, err := NewCatalog(active, assembled)
	if err != nil {
		return nil, err
	}
	return catalog.WithAuthorizer(authorizer), nil
}

func applyModelMapping(raw, model string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model
	}
	var mapping map[string]string
	if err := common.Unmarshal([]byte(raw), &mapping); err != nil {
		return model
	}
	if mapped := strings.TrimSpace(mapping[model]); mapped != "" {
		return mapped
	}
	return model
}
