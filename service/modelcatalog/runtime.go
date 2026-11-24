package modelcatalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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
	Type         int
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
	authorized, err := authorizer.Authorize(ctx, runtime.UserID, runtime.Groups, runtime.Client)
	if err != nil {
		return nil, err
	}

	var disabled map[string]bool
	if availability, ok := sources.(interface {
		DisabledModels(context.Context) (map[string]bool, error)
	}); ok {
		disabled, err = availability.DisabledModels(ctx)
		if err != nil {
			return nil, err
		}
	}
	routeByID := make(map[int]ClientModelRoute, len(routes))
	for _, route := range routes {
		if route.Enabled && RouteMatchesClient(route.Client, authorized.Client) &&
			!disabled[strings.ToLower(strings.TrimSpace(route.PublicId))] {
			if len(disabled) != 0 {
				aliases, _ := decodeStrings(route.Aliases)
				filtered := make([]string, 0, len(aliases))
				for _, alias := range aliases {
					if !disabled[strings.ToLower(strings.TrimSpace(alias))] {
						filtered = append(filtered, alias)
					}
				}
				encoded, _ := common.Marshal(filtered)
				route.Aliases = string(encoded)
			}
			routeByID[route.Id] = route
		}
	}
	targetsByRoute := make(map[int][]ClientModelTarget, len(routeByID))
	for _, raw := range targets {
		if !raw.Enabled {
			continue
		}
		route, ok := routeByID[raw.RouteId]
		if !ok {
			continue
		}
		channel, ok, err := sources.Channel(ctx, raw.ChannelId)
		if err != nil {
			return nil, err
		}
		if !ok || !channel.Enabled {
			continue
		}
		metadata, metadataErr := NormalizeModelCapabilities(route.Metadata)
		if metadataErr != nil {
			common.SysLog(fmt.Sprintf("model catalog skipped route %d target %d: %v", route.Id, raw.Id, metadataErr))
			continue
		}
		if err := ValidateChannelOperations(metadata, channel.Type); err != nil {
			common.SysLog(fmt.Sprintf("model catalog skipped route %d target %d channel %d: %v", route.Id, raw.Id, channel.ID, err))
			continue
		}
		upstream, err := MapUpstreamModel(channel.ModelMapping, strings.TrimSpace(raw.UpstreamModel))
		if err != nil {
			common.SysLog(fmt.Sprintf("model catalog skipped route %d target %d: %v", route.Id, raw.Id, err))
			continue
		}
		if upstream == "" || disabled[strings.ToLower(upstream)] {
			continue
		}
		abilities, err := sources.Abilities(ctx, raw.ChannelId, upstream)
		if err != nil {
			return nil, err
		}
		groups := make([]string, 0, len(abilities))
		groupPolicies := make(map[string]model.ClientModelTargetPolicy, len(abilities))
		for _, ability := range abilities {
			if !ability.Enabled || !strings.EqualFold(strings.TrimSpace(ability.Model), upstream) {
				continue
			}
			group := strings.TrimSpace(ability.Group)
			if group != "" {
				groups = append(groups, group)
				weight := int(ability.Weight)
				if weight <= 0 {
					weight = 1
				}
				key := strings.ToLower(group)
				candidate := model.ClientModelTargetPolicy{Priority: int(ability.Priority), Weight: weight}
				current, exists := groupPolicies[key]
				if !exists || candidate.Priority > current.Priority || candidate.Priority == current.Priority && candidate.Weight > current.Weight {
					groupPolicies[key] = candidate
				}
			}
		}
		if len(groups) == 0 || firstAuthorizedTargetGroup(groups, authorized.Groups) == "" {
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
		if validator, ok := sources.(interface {
			ValidateTargetPricing(int, string, string, []string) error
		}); ok {
			if err := validator.ValidateTargetPricing(channel.Type, upstream, canonical, metadata.Operations); err != nil {
				common.SysLog(fmt.Sprintf("model catalog skipped route %d target %d: %v", route.Id, raw.Id, err))
				continue
			}
		}
		raw.UpstreamModel = upstream
		raw.BillingModel = strings.TrimSpace(canonical)
		raw.Groups = normalizeIDs(groups)
		raw.GroupPolicies = groupPolicies
		if raw.Weight <= 0 {
			raw.Weight = 1
		}
		// Use the strict catalog validator to reject a corrupt target without
		// discarding healthy siblings or admitting an ambiguous fallback.
		if _, err := NewCatalog([]ClientModelRoute{route}, []ClientModelTarget{raw}); err != nil {
			common.SysLog(fmt.Sprintf("model catalog skipped route %d target %d: %v", route.Id, raw.Id, err))
			continue
		}
		targetsByRoute[raw.RouteId] = append(targetsByRoute[raw.RouteId], raw)
	}
	nameCounts := make(map[string]int)
	for id := range targetsByRoute {
		for _, name := range RouteNames(routeByID[id]) {
			nameCounts[name]++
		}
	}
	active := make([]ClientModelRoute, 0, len(targetsByRoute))
	assembled := make([]ClientModelTarget, 0, len(targets))
	for _, original := range routes {
		route := routeByID[original.Id]
		validTargets := targetsByRoute[route.Id]
		if len(validTargets) == 0 {
			continue
		}
		collision := false
		for _, name := range RouteNames(route) {
			if nameCounts[name] > 1 {
				common.SysLog(fmt.Sprintf("model catalog skipped route %d: model name %q is ambiguous", route.Id, name))
				collision = true
				break
			}
		}
		if !collision {
			active = append(active, route)
			assembled = append(assembled, validTargets...)
		}
	}
	catalog, err := NewCatalog(active, assembled)
	if err != nil {
		return nil, err
	}
	return catalog.WithAuthorizer(authorizer), nil
}

// RouteNames returns the case-insensitive public names claimed by a route.
func RouteNames(route ClientModelRoute) []string {
	aliases, _ := decodeStrings(route.Aliases)
	names := normalizeIDs(append(aliases, route.PublicId))
	for i := range names {
		names[i] = strings.ToLower(names[i])
	}
	return names
}

// MapUpstreamModel resolves the channel alias used by the catalog and admin validation.
func MapUpstreamModel(raw, model string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model, nil
	}
	var mapping map[string]string
	if err := common.Unmarshal([]byte(raw), &mapping); err != nil {
		return "", fmt.Errorf("invalid channel model mapping: %w", err)
	}
	// Explicit targets bind a single canonical provider identity. Legacy
	// relays can follow chains, but must not re-map an explicit target.
	if mapped := strings.TrimSpace(mapping[model]); mapped != "" {
		return mapped, nil
	}
	return model, nil
}
