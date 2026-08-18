package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var ErrAuthorizerRequired = errors.New("model catalog authorizer is required")

// Re-export the durable records from model so catalog callers only need one
// package when loading and resolving public routes.
type ClientModelRoute = model.ClientModelRoute
type ClientModelTarget = model.ClientModelTarget

// WireProtocol identifies the wire shape a client request may use.
type WireProtocol string

const (
	WireResponses  WireProtocol = "responses"
	WireOpenAIChat WireProtocol = "openai_chat"
	WireAnthropic  WireProtocol = "anthropic"
)

// ClientModel is the immutable public identity exposed to a client.
type ClientModel struct {
	Id                       int            `json:"-"`
	PublicID                 string         `json:"id"`
	DisplayName              string         `json:"display_name"`
	Client                   string         `json:"client,omitempty"`
	Aliases                  []string       `json:"aliases,omitempty"`
	PreferredWires           []WireProtocol `json:"preferred_wires"`
	ContextWindow            int            `json:"context_window,omitempty"`
	ReasoningEfforts         []string       `json:"reasoning_efforts,omitempty"`
	SupportsReasoningSummary bool           `json:"supports_reasoning_summary"`
	SupportsTools            bool           `json:"supports_tools"`
	Metadata                 string         `json:"-"`
}

type routeMetadata struct {
	ContextWindow            int      `json:"context_window"`
	ContextFitRequired       bool     `json:"context_fit_required"`
	ReasoningEfforts         []string `json:"reasoning_efforts"`
	SupportsReasoningSummary bool     `json:"supports_reasoning_summary"`
	SupportsTools            bool     `json:"supports_tools"`
}

// ResolvedModel is the result used by routing and billing. RequestedModel is
// retained for telemetry, while BillingModel is always the canonical upstream
// price key; callers must use this value before charging a request.
type ResolvedModel struct {
	RequestedModel       string
	PublicID             string
	UpstreamModel        string
	BillingModel         string
	ChannelID            int
	RouteID              int
	TargetID             int
	Priority             int
	Weight               int
	Client               string
	PreferredWires       []WireProtocol
	ContextWindow        int
	RequiresKnownContext bool
	SelectedGroup        string
}

// AuthorizedRuntimeSnapshot is the immutable authorization result consumed by
// catalog operations. Callers should obtain it from a VisibilityAuthorizer;
// the convenience methods below defensively clone every slice before use.
type AuthorizedRuntimeSnapshot struct {
	UserID int
	Groups []string
	Client string
}

func NewAuthorizedRuntimeSnapshot(userID int, groups []string, client string) AuthorizedRuntimeSnapshot {
	return AuthorizedRuntimeSnapshot{UserID: userID, Groups: append([]string(nil), normalizeIDs(groups)...), Client: strings.ToLower(strings.TrimSpace(client))}
}

// VisibilityAuthorizer binds a request to a tenant and its already-authorized
// groups. Catalog construction deliberately does not perform live DB lookups.
type VisibilityAuthorizer interface {
	Authorize(context.Context, int, []string, string) (AuthorizedRuntimeSnapshot, error)
}

type routeEntry struct {
	model              ClientModel
	targets            []model.ClientModelTarget
	enabled            bool
	contextFitRequired bool
}

// Catalog is an immutable in-memory snapshot. Build a new snapshot after a
// route mutation and atomically replace it in the caller; methods never expose
// internal slices or maps.
type Catalog struct {
	entries    []routeEntry
	byKey      map[string]string
	byPublic   map[string]int
	authorizer VisibilityAuthorizer
}

// NewCatalog validates durable route and target records and builds a snapshot.
func NewCatalog(routes []model.ClientModelRoute, targets []model.ClientModelTarget) (*Catalog, error) {
	byRoute := make(map[int]int, len(routes))
	entries := make([]routeEntry, len(routes))
	for i, route := range routes {
		if route.Id <= 0 {
			return nil, fmt.Errorf("route id %d is invalid; route id must be positive", route.Id)
		}
		if _, exists := byRoute[route.Id]; exists {
			return nil, fmt.Errorf("route id %d is duplicated", route.Id)
		}
		publicID := strings.TrimSpace(route.PublicId)
		if err := validateID(publicID, "public model id"); err != nil {
			return nil, err
		}
		client := strings.ToLower(strings.TrimSpace(route.Client))
		if client == "" {
			client = "all"
		}
		aliases, err := decodeStrings(route.Aliases)
		if err != nil {
			return nil, fmt.Errorf("route %q aliases: %w", publicID, err)
		}
		aliases, err = normalizeAliases(aliases)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", publicID, err)
		}
		display := strings.TrimSpace(route.DisplayName)
		if display == "" {
			display = publicID
		}
		capabilities, err := decodeRouteMetadata(route.Metadata)
		if err != nil {
			return nil, fmt.Errorf("route %q metadata: %w", publicID, err)
		}
		entries[i] = routeEntry{enabled: route.Enabled, contextFitRequired: capabilities.ContextFitRequired, model: ClientModel{
			Id: route.Id, PublicID: publicID, DisplayName: display, Client: client,
			Aliases: append([]string(nil), aliases...), Metadata: route.Metadata,
			ContextWindow: capabilities.ContextWindow, ReasoningEfforts: append([]string(nil), capabilities.ReasoningEfforts...),
			SupportsReasoningSummary: capabilities.SupportsReasoningSummary, SupportsTools: capabilities.SupportsTools,
		}}
		byRoute[route.Id] = i
	}

	for _, target := range targets {
		i, ok := byRoute[target.RouteId]
		if !ok {
			return nil, fmt.Errorf("target %d references nonexistent route %d", target.Id, target.RouteId)
		}
		if !target.Enabled {
			return nil, fmt.Errorf("target %d is disabled", target.Id)
		}
		if target.ChannelId <= 0 {
			return nil, fmt.Errorf("target %d references nonexistent channel %d", target.Id, target.ChannelId)
		}
		if err := validateID(strings.TrimSpace(target.UpstreamModel), "upstream model"); err != nil {
			return nil, fmt.Errorf("target %d: %w", target.Id, err)
		}
		if err := validateID(strings.TrimSpace(target.BillingModel), "billing model"); err != nil {
			return nil, fmt.Errorf("target %d has no billing configuration: %w", target.Id, err)
		}
		wires, err := decodeWires(target.PreferredWires)
		if err != nil {
			return nil, fmt.Errorf("target %d: %w", target.Id, err)
		}
		if target.Weight <= 0 {
			return nil, fmt.Errorf("target %d has weight below one", target.Id)
		}
		target.UpstreamModel = strings.TrimSpace(target.UpstreamModel)
		target.BillingModel = strings.TrimSpace(target.BillingModel)
		target.Groups = normalizeIDs(target.Groups)
		if len(target.GroupPolicies) > 0 {
			policies := make(map[string]model.ClientModelTargetPolicy, len(target.GroupPolicies))
			for group, policy := range target.GroupPolicies {
				policies[strings.ToLower(strings.TrimSpace(group))] = policy
			}
			target.GroupPolicies = policies
		}
		target.PreferredWires = encodeWires(wires)
		entries[i].targets = append(entries[i].targets, target)
	}

	for i := range entries {
		if !entries[i].modelEnabled() {
			continue
		}
		if len(entries[i].targets) == 0 {
			return nil, fmt.Errorf("public model %q has no enabled targets", entries[i].model.PublicID)
		}
		sort.SliceStable(entries[i].targets, func(a, b int) bool {
			if entries[i].targets[a].Priority != entries[i].targets[b].Priority {
				return entries[i].targets[a].Priority > entries[i].targets[b].Priority
			}
			return entries[i].targets[a].Id < entries[i].targets[b].Id
		})
	}

	byKey := make(map[string]string, len(entries)*2)
	byPublic := make(map[string]int, len(entries))
	for i, entry := range entries {
		if !entry.modelEnabled() {
			continue
		}
		if err := addLookup(byKey, strings.ToLower(entry.model.PublicID), entry.model.PublicID, false); err != nil {
			return nil, err
		}
		byPublic[entry.model.PublicID] = i
		for _, alias := range entry.model.Aliases {
			if err := addLookup(byKey, strings.ToLower(alias), entry.model.PublicID, true); err != nil {
				return nil, err
			}
		}
	}
	return &Catalog{entries: entries, byKey: byKey, byPublic: byPublic}, nil
}

// NewCatalogFromRecords is a named alias for callers loading GORM records.
func NewCatalogFromRecords(routes []model.ClientModelRoute, targets []model.ClientModelTarget) (*Catalog, error) {
	return NewCatalog(routes, targets)
}

// WithAuthorizer returns a catalog view that requires every operation to pass
// through the supplied tenant/group authorizer. The route index remains shared
// and immutable.
func (c *Catalog) WithAuthorizer(authorizer VisibilityAuthorizer) *Catalog {
	if c == nil {
		return nil
	}
	copy := *c
	copy.authorizer = authorizer
	return &copy
}

func (e routeEntry) modelEnabled() bool { return e.enabled }

func addLookup(index map[string]string, key, publicID string, alias bool) error {
	if previous, ok := index[key]; ok {
		if alias {
			return fmt.Errorf("alias %q is assigned to multiple public models (%s and %s)", key, previous, publicID)
		}
		return fmt.Errorf("public model id %q is assigned more than once", key)
	}
	index[key] = publicID
	return nil
}

// List resolves an authorization snapshot before returning visible models. In
// production the catalog must be wrapped with WithAuthorizer; the fallback is
// only for already-authorized internal callers and never persists user data.
func (c *Catalog) List(ctx context.Context, userID int, groups []string, client string) ([]ClientModel, error) {
	snapshot, err := c.authorize(ctx, userID, groups, client)
	if err != nil {
		return nil, err
	}
	return c.listAuthorized(snapshot), nil
}

func (c *Catalog) listAuthorized(snapshot AuthorizedRuntimeSnapshot) []ClientModel {
	client := snapshot.Client
	visibleGroups := snapshot.Groups
	result := make([]ClientModel, 0, len(c.entries))
	for _, entry := range c.entries {
		if client != "" && entry.model.Client != "all" && entry.model.Client != client {
			continue
		}
		if !entryVisible(entry.targets, visibleGroups) {
			continue
		}
		clientModel := cloneClientModel(entry.model)
		seenWires := make(map[WireProtocol]struct{}, 3)
		for _, target := range entry.targets {
			if !entryTargetVisible(target, visibleGroups) {
				continue
			}
			wires, _ := decodeWires(target.PreferredWires)
			for _, wire := range wires {
				if _, exists := seenWires[wire]; exists {
					continue
				}
				seenWires[wire] = struct{}{}
				clientModel.PreferredWires = append(clientModel.PreferredWires, wire)
			}
		}
		result = append(result, clientModel)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].PublicID < result[j].PublicID })
	return result
}

func (c *Catalog) Resolve(ctx context.Context, userID int, groups []string, requestedModel, client string) (*ResolvedModel, error) {
	candidates, err := c.ResolveCandidates(ctx, userID, groups, requestedModel, client)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("model %q has no authorized targets", requestedModel)
	}
	resolved := candidates[0]
	return &resolved, nil
}

// ResolveCandidates returns every authorized target in the stable priority/ID
// order established when the immutable catalog snapshot is built.
func (c *Catalog) ResolveCandidates(ctx context.Context, userID int, groups []string, requestedModel, client string) ([]ResolvedModel, error) {
	snapshot, err := c.authorize(ctx, userID, groups, client)
	if err != nil {
		return nil, err
	}
	return c.resolveCandidatesAuthorized(snapshot, requestedModel)
}

func (c *Catalog) resolveCandidatesAuthorized(snapshot AuthorizedRuntimeSnapshot, requestedModel string) ([]ResolvedModel, error) {
	requested := strings.TrimSpace(requestedModel)
	if requested == "" {
		return nil, fmt.Errorf("model is required")
	}
	publicID, ok := c.byKey[strings.ToLower(requested)]
	if !ok {
		return nil, fmt.Errorf("model %q is not available", requestedModel)
	}
	i := c.byPublic[publicID]
	entry := c.entries[i]
	client := snapshot.Client
	if client != "" && entry.model.Client != "all" && entry.model.Client != client {
		return nil, fmt.Errorf("model %q is not available for client %q", requestedModel, client)
	}
	// Targets are ordered by priority/ID at snapshot build time. This core does
	// not perform weighted selection; it preserves each validated weight for a
	// later selector/runtime layer.
	resolved := make([]ResolvedModel, 0, len(entry.targets))
	for _, target := range entry.targets {
		if !entryTargetVisible(target, snapshot.Groups) {
			continue
		}
		wires, err := decodeWires(target.PreferredWires)
		if err != nil {
			return nil, err
		}
		contextWindow := entry.model.ContextWindow
		if target.ContextWindow > 0 {
			contextWindow = target.ContextWindow
		}
		selectedGroup := firstAuthorizedTargetGroup(target.Groups, snapshot.Groups)
		priority, weight := target.Priority, target.Weight
		if policy, ok := target.GroupPolicies[strings.ToLower(strings.TrimSpace(selectedGroup))]; ok {
			priority, weight = policy.Priority, policy.Weight
		}
		resolved = append(resolved, ResolvedModel{
			RequestedModel: requestedModel, PublicID: entry.model.PublicID,
			UpstreamModel: target.UpstreamModel, BillingModel: target.BillingModel,
			ChannelID: target.ChannelId, RouteID: entry.model.Id, TargetID: target.Id, Priority: priority, Weight: weight,
			Client:               entry.model.Client,
			PreferredWires:       append([]WireProtocol(nil), wires...),
			ContextWindow:        contextWindow,
			RequiresKnownContext: target.ContextFitRequired || entry.contextFitRequired,
			SelectedGroup:        selectedGroup,
		})
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("model %q is not visible for the requested groups", requestedModel)
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].Priority != resolved[j].Priority {
			return resolved[i].Priority > resolved[j].Priority
		}
		return resolved[i].TargetID < resolved[j].TargetID
	})
	return resolved, nil
}

func firstAuthorizedTargetGroup(targetGroups, authorizedGroups []string) string {
	if len(authorizedGroups) == 0 {
		return ""
	}
	if len(targetGroups) == 0 {
		return authorizedGroups[0]
	}
	allowed := make(map[string]struct{}, len(targetGroups))
	for _, group := range targetGroups {
		allowed[strings.ToLower(strings.TrimSpace(group))] = struct{}{}
	}
	for _, group := range authorizedGroups {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(group))]; ok {
			return group
		}
	}
	return ""
}

func (c *Catalog) authorize(ctx context.Context, userID int, groups []string, client string) (AuthorizedRuntimeSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return AuthorizedRuntimeSnapshot{}, err
	}
	var snapshot AuthorizedRuntimeSnapshot
	var err error
	if c.authorizer != nil {
		snapshot, err = c.authorizer.Authorize(ctx, userID, groups, client)
		if err != nil {
			return AuthorizedRuntimeSnapshot{}, err
		}
	} else {
		return AuthorizedRuntimeSnapshot{}, ErrAuthorizerRequired
	}
	if snapshot.UserID != userID {
		return AuthorizedRuntimeSnapshot{}, fmt.Errorf("authorization snapshot belongs to user %d, not %d", snapshot.UserID, userID)
	}
	requestedClient := strings.ToLower(strings.TrimSpace(client))
	boundClient := strings.ToLower(strings.TrimSpace(snapshot.Client))
	if boundClient != "" && requestedClient != "" && boundClient != requestedClient {
		return AuthorizedRuntimeSnapshot{}, fmt.Errorf("authorization snapshot is for client %q, not %q", boundClient, requestedClient)
	}
	snapshot.Groups = append([]string(nil), normalizeIDs(snapshot.Groups)...)
	if boundClient != "" {
		snapshot.Client = boundClient
	} else {
		snapshot.Client = requestedClient
	}
	return snapshot, nil
}

func entryVisible(targets []model.ClientModelTarget, groups []string) bool {
	for _, target := range targets {
		if entryTargetVisible(target, groups) {
			return true
		}
	}
	return false
}

func entryTargetVisible(target model.ClientModelTarget, groups []string) bool {
	if len(target.Groups) == 0 {
		return true
	}
	if len(groups) == 0 {
		return false
	}
	allowed := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		allowed[group] = struct{}{}
	}
	for _, group := range target.Groups {
		if _, ok := allowed[strings.ToLower(group)]; ok {
			return true
		}
	}
	return false
}

func cloneClientModel(m ClientModel) ClientModel {
	m.Aliases = append([]string(nil), m.Aliases...)
	m.PreferredWires = append([]WireProtocol(nil), m.PreferredWires...)
	m.ReasoningEfforts = append([]string(nil), m.ReasoningEfforts...)
	return m
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func validateID(value, label string) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control characters", label)
		}
	}
	return nil
}

func normalizeIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeAliases(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("alias is required")
		}
		if err := validateID(value, "alias"); err != nil {
			return nil, err
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("alias %q is duplicated", value)
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func decodeStrings(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var values []string
	if err := common.Unmarshal([]byte(raw), &values); err == nil {
		return values, nil
	}
	values = strings.Split(raw, ",")
	return values, nil
}

func decodeRouteMetadata(raw string) (routeMetadata, error) {
	if strings.TrimSpace(raw) == "" {
		return routeMetadata{}, nil
	}
	var metadata routeMetadata
	if err := common.Unmarshal([]byte(raw), &metadata); err != nil {
		return routeMetadata{}, err
	}
	if metadata.ContextWindow < 0 {
		return routeMetadata{}, fmt.Errorf("context_window must not be negative")
	}
	seen := make(map[string]struct{}, len(metadata.ReasoningEfforts))
	normalized := make([]string, 0, len(metadata.ReasoningEfforts))
	for _, effort := range metadata.ReasoningEfforts {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort == "" {
			return routeMetadata{}, fmt.Errorf("reasoning_efforts must not contain blank values")
		}
		if _, exists := seen[effort]; exists {
			continue
		}
		seen[effort] = struct{}{}
		normalized = append(normalized, effort)
	}
	metadata.ReasoningEfforts = normalized
	return metadata, nil
}

func decodeWires(raw string) ([]WireProtocol, error) {
	values, err := decodeStrings(raw)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("preferred wires are required")
	}
	result := make([]WireProtocol, 0, len(values))
	seen := make(map[WireProtocol]struct{}, len(values))
	for _, value := range values {
		wire := WireProtocol(strings.ToLower(strings.TrimSpace(value)))
		switch wire {
		case WireResponses, WireOpenAIChat, WireAnthropic:
		default:
			return nil, fmt.Errorf("unknown wire protocol %q", value)
		}
		if _, ok := seen[wire]; ok {
			continue
		}
		seen[wire] = struct{}{}
		result = append(result, wire)
	}
	return result, nil
}

func encodeWires(values []WireProtocol) string {
	data, _ := common.Marshal(values)
	return string(data)
}
