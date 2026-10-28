package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	ci "github.com/QuantumNous/new-api/internal/platformbilling/continuityindex"
	"gorm.io/gorm"
)

// NativeHistoryConfig contains deployment-owned secrets and certifications. It
// is never populated from request metadata or the public model catalog.
type NativeHistoryConfig struct {
	ActiveKeyVersion string                         `json:"active_key_version"`
	Keys             map[string][]byte              `json:"keys"`
	TTLSeconds       int64                          `json:"ttl_seconds"`
	Capacity         int64                          `json:"capacity"`
	Policies         map[string]NativeHistoryPolicy `json:"policies"`
}
type NativeHistoryPolicy struct {
	Version           string                `json:"version"`
	ChannelID         int64                 `json:"channel_id"`
	Namespace         string                `json:"namespace"`
	Organization      string                `json:"organization"`
	UpstreamModel     string                `json:"upstream_model"`
	DefaultStored     bool                  `json:"default_stored"`
	CompatibleSources []NativeHistorySource `json:"compatible_sources"`
}
type NativeHistorySource struct {
	PublicModel   string `json:"public_model"`
	UpstreamModel string `json:"upstream_model"`
}
type NativeHistoryResolver func(context.Context, string, string, string, ci.Target) (ci.Target, string, error)
type nativeContinuitySnapshot struct {
	Version  string
	Root     string
	Target   ci.Target
	Policy   NativeHistoryPolicy
	Bindings []ci.Binding
}
type nativeSendIdentity struct {
	Target                          ci.Target
	UpstreamModel, BodyDigest, Wire string
}
type nativeCompletionSeal struct {
	Digest, PolicyVersion string
	Storage               ci.StoragePolicy
	CreatedAt, ExpiresAt  time.Time
}

var ErrNativeHistory = ci.ErrUnavailable

func (a *Adapter) SetNativeHistoryResolver(resolve NativeHistoryResolver) {
	a.historyResolver = resolve
}
func nativeHistoryWire(wire string) bool {
	return wire == "responses.create" || wire == "responses.compact"
}
func nativeDigest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

// NativeHistoryNamespace binds exact API authority, base path and API project.
// The organization is a separate typed field in the index namespace commitment.
func NativeHistoryNamespace(endpoint, project string) (string, error) {
	u, e := url.Parse(endpoint)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrNativeHistory
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/compact")
	if !strings.HasSuffix(path, "/responses") {
		return "", ErrNativeHistory
	}
	u.Path = strings.TrimSuffix(u.Path, "/compact")
	u.Path = strings.TrimSuffix(u.Path, "/responses")
	u.RawPath = ""
	raw, e := common.Marshal(struct{ API, Project string }{u.String(), project})
	if e != nil {
		return "", ErrNativeHistory
	}
	return string(raw), nil
}
func (a *Adapter) initNativeHistory(ctx context.Context) error {
	c := a.config.NativeHistory
	if c == nil {
		return nil
	}
	// Snapshot deployment configuration so a caller cannot mutate policy or keys
	// while active admissions and final-send checks are in flight.
	raw, e := common.Marshal(c)
	if e != nil {
		return ErrInvalid
	}
	var frozen NativeHistoryConfig
	if common.UnmarshalUnique(raw, &frozen) != nil {
		return ErrInvalid
	}
	a.config.NativeHistory = &frozen
	c = &frozen
	if c.TTLSeconds < 1 || c.TTLSeconds > 7*24*3600 || len(c.Policies) == 0 {
		return ErrInvalid
	}
	for model, p := range c.Policies {
		if !validNativeID(model) || !stableID.MatchString(p.Version) || p.ChannelID < 1 || p.Namespace == "" || len(p.Namespace) > 2048 || len(p.Organization) > 255 || !validNativeID(p.UpstreamModel) || len(p.CompatibleSources) > 128 {
			return ErrInvalid
		}
	}
	index, e := ci.New(a.db, ci.Config{ActiveKeyVersion: c.ActiveKeyVersion, Keys: c.Keys, TTL: time.Duration(c.TTLSeconds) * time.Second, Capacity: c.Capacity, ValidateCreator: a.validateNativeCreator})
	if e != nil {
		return e
	}
	if e = index.Migrate(ctx); e != nil {
		return e
	}
	a.history = index
	return nil
}
func nativeOwner(row Call) ci.Owner {
	mode := ci.TaskCredential
	if row.CredentialKind == "api_key" {
		mode = ci.DurableKey
	}
	return ci.Owner{ActorID: row.ActorID, AccountID: row.AccountID, ProductID: row.OriginProductID, InstanceID: row.InstanceID, CredentialID: row.CredentialID, RootTaskID: row.TaskID, Mode: mode}
}
func (a *Adapter) validateNativeCreator(ctx context.Context, o ci.Owner, b ci.Binding) (ci.Target, error) {
	var row Call
	var attempt Attempt
	var sent nativeSendIdentity
	var seal nativeCompletionSeal
	var frozen nativeContinuitySnapshot
	if a.db.WithContext(ctx).First(&row, "call_id = ?", b.Provenance.CallID).Error != nil || a.db.WithContext(ctx).First(&attempt, "id = ? AND call_id = ?", b.Provenance.AttemptID, row.CallID).Error != nil {
		return ci.Target{}, ErrNativeHistory
	}
	actual := nativeOwner(row)
	if actual.ActorID != o.ActorID || actual.AccountID != o.AccountID || actual.ProductID != o.ProductID || actual.InstanceID != o.InstanceID || actual.Mode != o.Mode || (o.Mode == ci.DurableKey && actual.CredentialID != o.CredentialID) || actual.RootTaskID != b.RootTaskID || row.WireOperation != "responses.create" || row.Model != b.Provenance.PublicModel || (attempt.Status != "DISPATCHED" && attempt.Status != "USAGE_CONFIRMED" && attempt.Status != "UNKNOWN") {
		return ci.Target{}, ErrNativeHistory
	}
	if common.UnmarshalUnique([]byte(attempt.UpstreamIdentity), &sent) != nil || common.UnmarshalUnique([]byte(row.NativeObservation), &seal) != nil || sent.Wire != "responses.create" || sent.UpstreamModel != b.Provenance.UpstreamModel || seal.Digest != b.Provenance.ObservationDigest || seal.PolicyVersion != b.Provenance.PolicyVersion || seal.Storage != b.Storage || !seal.CreatedAt.Equal(b.CreatedAt) || !seal.ExpiresAt.Equal(b.ExpiresAt) || sent.Target.ChannelID != b.ChannelID || sent.Target.PoolID != b.PoolID || sent.Target.MemberID != b.MemberID || attempt.ChannelID != int(b.ChannelID) {
		return ci.Target{}, ErrNativeHistory
	}
	if common.UnmarshalUnique([]byte(row.ContinuitySnapshot), &frozen) != nil || frozen.Root != row.TaskID || frozen.Target != sent.Target || frozen.Policy.Version != seal.PolicyVersion || frozen.Policy.UpstreamModel != sent.UpstreamModel || len(sent.BodyDigest) != 64 {
		return ci.Target{}, ErrNativeHistory
	}
	return sent.Target, nil
}
func nativeSelectors(body []byte) ([]ci.Selector, error) {
	var v map[string]any
	if common.UnmarshalUnique(body, &v) != nil {
		return nil, ErrInvalid
	}
	selectors := []ci.Selector{}
	if p := v["previous_response_id"]; p != nil {
		id, ok := p.(string)
		if !ok || !validNativeID(id) {
			return nil, ErrNativeHistory
		}
		selectors = append(selectors, ci.Selector{Kind: ci.Response, ID: id})
	}
	items, _ := v["input"].([]any)
	for _, v := range items {
		item, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == "item_reference" || (len(item) == 1 && item["id"] != nil) {
			id, ok := item["id"].(string)
			if !ok || !validNativeID(id) {
				return nil, ErrNativeHistory
			}
			selectors = append(selectors, ci.Selector{Kind: ci.OutputItem, ID: id})
		}
	}
	if len(selectors) > 64 {
		return nil, ErrNativeHistory
	}
	return selectors, nil
}
func validNativeID(id string) bool {
	if id == "" || len(id) > 255 || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (a *Adapter) admitNativeContinuity(ctx context.Context, grant Claims, task, model, wire string, body []byte) (string, string, error) {
	selectors, e := nativeSelectors(body)
	if e != nil {
		return "", "", e
	}
	if a.history == nil {
		if len(selectors) > 0 {
			return "", "", ErrNativeHistory
		}
		return task, "", nil
	}
	p, ok := a.config.NativeHistory.Policies[model]
	if !ok && len(selectors) == 0 {
		return task, "", nil
	}
	if !ok || a.historyResolver == nil {
		return "", "", ErrNativeHistory
	}
	row := Call{ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, InstanceID: a.config.InstanceID, CredentialID: grant.ID, CredentialKind: grant.CredentialKind, TaskID: task}
	snapshot := nativeContinuitySnapshot{Version: "native-history-v1", Root: task, Policy: p, Target: ci.Target{ChannelID: p.ChannelID, Namespace: p.Namespace, APIOrganization: p.Organization}}
	if len(selectors) > 0 {
		resolution, e := a.history.Resolve(ctx, nativeOwner(row), selectors)
		if e != nil {
			return "", "", ErrNativeHistory
		}
		snapshot.Target = resolution.Target
		snapshot.Bindings = resolution.Bindings
		snapshot.Root = resolution.RootTaskID
		for _, b := range resolution.Bindings {
			compatible := b.Provenance.PublicModel == model && b.Provenance.UpstreamModel == p.UpstreamModel
			for _, source := range p.CompatibleSources {
				compatible = compatible || (source.PublicModel == b.Provenance.PublicModel && source.UpstreamModel == b.Provenance.UpstreamModel)
			}
			if !compatible {
				return "", "", ErrNativeHistory
			}
		}
	}
	if snapshot.Target.ChannelID != p.ChannelID || snapshot.Target.Namespace != p.Namespace || snapshot.Target.APIOrganization != p.Organization {
		return "", "", ErrNativeHistory
	}
	current, upstream, e := a.historyResolver(ctx, grant.OriginProductID, model, wire, snapshot.Target)
	if e != nil || upstream != p.UpstreamModel || current.ChannelID != snapshot.Target.ChannelID || current.Namespace != snapshot.Target.Namespace || current.APIOrganization != snapshot.Target.APIOrganization || (snapshot.Target.MemberID != "" && current != snapshot.Target) {
		return "", "", ErrNativeHistory
	}
	snapshot.Target = current
	data, e := common.Marshal(snapshot)
	if e != nil || len(data) > 128<<10 {
		return "", "", ErrNativeHistory
	}
	return snapshot.Root, string(data), nil
}

// NativeHistoryReplay runs before parsing mutable certificates or resolving an
// expired selector. It authenticates the original immutable receipt scope.
func (a *Adapter) NativeHistoryReplay(ctx context.Context, credential, task, id string, wire WireOperation, body []byte) (bool, error) {
	var old Call
	e := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return true, e
	}

	// Receipt supports refreshed task credentials and purpose=receipt key checks.
	if strings.HasPrefix(credential, "uh_key_") {
		task = old.TaskID
	}
	if _, e = a.Receipt(ctx, credential, task, id); e != nil {
		return true, e
	}
	var v map[string]any
	if common.UnmarshalUnique(body, &v) != nil {
		return true, ErrConflict
	}
	canonical, e := common.Marshal(v)
	expectedWire := old.WireOperation
	if expectedWire == "" {
		expectedWire = legacyWireOperation(old.Operation)
	}
	if e != nil || expectedWire != wire.ID || old.WirePathModel != wire.PathModel || old.WireMode != wire.Mode {
		return true, ErrConflict
	}
	if old.ContinuitySnapshot != "" {
		if nativeDigest(canonical) != old.NativeRequestDigest {
			return true, ErrConflict
		}
	} else {
		data, _ := common.Marshal(struct {
			Version, Operation, Task, Actor, Account, Origin, Asset, Funding string
			Profile                                                          int
			Body                                                             string
		}{"platform-metered-v2", old.Operation, old.TaskID, old.ActorID, old.AccountID, old.OriginProductID, old.Asset, old.FundingMode, old.BillingProfileVersion, string(canonical)})
		if old.MediaSnapshot != "" {
			data, _ = common.Marshal(struct{ Request, Media string }{string(data), old.MediaSnapshot})
		}
		if old.DigestVersion == "platform-wire-v3" {
			data, _ = common.Marshal(struct {
				Version string
				Wire    WireOperation
				Scope   string
			}{"platform-wire-v3", wire, string(data)})
		} else if old.DigestVersion != "" {
			return true, ErrConflict
		}
		if nativeDigest(data) != old.Digest {
			return true, ErrConflict
		}
	}
	return true, ErrDuplicate
}
func (s *Session) NativeHistoryEnabled() bool {
	return nativeHistoryWire(s.call.WireOperation) && s.call.ContinuitySnapshot != ""
}
func (s *Session) NativeHistoryTarget() (ci.Target, bool) {
	var f nativeContinuitySnapshot
	if !s.NativeHistoryEnabled() || common.UnmarshalUnique([]byte(s.call.ContinuitySnapshot), &f) != nil {
		return ci.Target{}, false
	}
	return f.Target, true
}
func (s *Session) NativeHistoryUpstream() string {
	var f nativeContinuitySnapshot
	if common.UnmarshalUnique([]byte(s.call.ContinuitySnapshot), &f) != nil {
		return ""
	}
	return f.Policy.UpstreamModel
}

// NativeHistoryBodyPermit is scoped to this exact admitted canonical request.
// Only native stored selectors and self-contained encrypted history are exempt;
// file/video/task IDs remain forbidden by normal media admission.
func (s *Session) NativeHistoryBodyPermit(body []byte, outbound bool) bool {
	if !s.NativeHistoryEnabled() {
		return false
	}
	expected := s.canonical
	if outbound {
		format, ok := TokenProducer(s.call.WireOperation, constant.ChannelTypeOpenAI)
		if !ok {
			return false
		}
		var e error
		expected, e = s.GenerationBody(constant.ChannelTypeOpenAI, format, s.NativeHistoryUpstream())
		if e != nil {
			return false
		}
	}
	var a, b any
	if common.UnmarshalUnique(expected, &a) != nil || common.UnmarshalUnique(body, &b) != nil {
		return false
	}
	x, _ := common.Marshal(a)
	y, _ := common.Marshal(b)
	return string(x) == string(y)
}

// DispatchNativeHistory is invoked at the last HTTP boundary, after resource
// checks, so a known pre-send rejection remains RESERVED and releasable.
func (s *Session) DispatchNativeHistory(ctx context.Context, target ci.Target, upstream string, request *http.Request, body []byte) error {
	var f nativeContinuitySnapshot
	if !s.NativeHistoryEnabled() || common.UnmarshalUnique([]byte(s.call.ContinuitySnapshot), &f) != nil || target != f.Target || upstream != f.Policy.UpstreamModel || request.Method != http.MethodPost || !s.NativeHistoryBodyPermit(body, true) {
		return ErrNativeHistory
	}
	if (s.call.WireOperation == "responses.create" && !strings.HasSuffix(request.URL.Path, "/responses")) || (s.call.WireOperation == "responses.compact" && !strings.HasSuffix(request.URL.Path, "/responses/compact")) {
		return ErrNativeHistory
	}
	ns, e := NativeHistoryNamespace(request.URL.String(), request.Header.Get("OpenAI-Project"))
	if e != nil || ns != target.Namespace || request.Header.Get("OpenAI-Organization") != target.APIOrganization {
		return ErrNativeHistory
	}
	current, mapped, e := s.adapter.historyResolver(ctx, s.call.OriginProductID, s.call.Model, s.call.WireOperation, target)
	if e != nil || current != target || mapped != upstream {
		return ErrNativeHistory
	}
	sent := nativeSendIdentity{Target: target, UpstreamModel: upstream, Wire: s.call.WireOperation, BodyDigest: nativeDigest(body)}
	raw, e := common.Marshal(sent)
	if e != nil {
		return e
	}
	s.nativeSend = string(raw)
	return s.Dispatch(ctx, int(target.ChannelID))
}

// RegisterNativeCompletion is independent of metering. Only complete raw create
// artifacts observed after a durable actual send can acquire ownership.
func (s *Session) RegisterNativeCompletion(ctx context.Context, raw []byte) error {
	if !s.NativeHistoryEnabled() || s.call.WireOperation != "responses.create" {
		return nil
	}
	if len(raw) == 0 || len(raw) > 16<<20 || s.attempt.ID == 0 || s.nativeSend == "" {
		return ErrNativeHistory
	}
	var response, request map[string]any
	var sent nativeSendIdentity
	var f nativeContinuitySnapshot
	if common.UnmarshalUnique(raw, &response) != nil || common.UnmarshalUnique(s.canonical, &request) != nil || common.UnmarshalUnique([]byte(s.nativeSend), &sent) != nil || common.UnmarshalUnique([]byte(s.call.ContinuitySnapshot), &f) != nil {
		return ErrNativeHistory
	}
	if response["object"] != "response" || response["status"] != "completed" || response["error"] != nil || response["incomplete_details"] != nil {
		return ErrNativeHistory
	}
	storage := ci.CertifiedDefaultStored
	if request["store"] == false {
		return nil
	}
	if request["store"] == true {
		storage = ci.ExplicitStored
	} else if !f.Policy.DefaultStored {
		return nil
	}
	if value := response["store"]; value != nil {
		if _, ok := value.(bool); !ok {
			return ErrNativeHistory
		}
	}
	if response["store"] == false {
		return nil
	}
	id, ok := response["id"].(string)
	if !ok || !validNativeID(id) {
		return ErrNativeHistory
	}
	output, ok := response["output"].([]any)
	if !ok || len(output) > 128 || !responsesInput(output) {
		return ErrNativeHistory
	}
	inputIDs := map[string]bool{}
	inputs, _ := request["input"].([]any)
	for _, v := range inputs {
		if item, ok := v.(map[string]any); ok {
			if id, ok := item["id"].(string); ok {
				inputIDs[id] = true
			}
		}
	}
	if inputIDs[id] || request["previous_response_id"] == id {
		return ErrNativeHistory
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, v := range output {
		item, ok := v.(map[string]any)
		if !ok {
			return ErrNativeHistory
		}
		switch item["type"] {
		case "message":
			if item["role"] != "assistant" {
				return ErrNativeHistory
			}
		case "function_call", "custom_tool_call", "reasoning":
		default:
			return ErrNativeHistory
		}
		if status, ok := item["status"]; ok && status != "completed" {
			return ErrNativeHistory
		}
		itemID, ok := item["id"].(string)
		if !ok || !validNativeID(itemID) || seen[itemID] || inputIDs[itemID] || itemID == id {
			return ErrNativeHistory
		}
		seen[itemID] = true
		ids = append(ids, itemID)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	seal := nativeCompletionSeal{Digest: nativeDigest(raw), PolicyVersion: f.Policy.Version, Storage: storage, CreatedAt: now, ExpiresAt: now.Add(time.Duration(s.adapter.config.NativeHistory.TTLSeconds) * time.Second)}
	// Freeze once; duplicate callback must use the original timestamps and digest.
	var existing Call
	if s.adapter.db.WithContext(ctx).First(&existing, "call_id = ?", s.call.CallID).Error != nil {
		return ErrNativeHistory
	}
	if existing.NativeObservation != "" {
		var old nativeCompletionSeal
		if common.UnmarshalUnique([]byte(existing.NativeObservation), &old) != nil || old.Digest != seal.Digest || old.Storage != seal.Storage || old.PolicyVersion != seal.PolicyVersion {
			return ErrNativeHistory
		}
		seal = old
	}
	encoded, e := common.Marshal(seal)
	if e != nil {
		return ErrNativeHistory
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND native_observation = ?", s.call.CallID, "UNKNOWN", existing.NativeObservation).Update("native_observation", string(encoded))
	if saved.Error != nil || saved.RowsAffected != 1 {
		return ErrNativeHistory
	}
	completion := ci.TrustedCompletion{Owner: nativeOwner(s.call), Target: sent.Target, Provenance: ci.Provenance{CallID: s.call.CallID, AttemptID: uint64(s.attempt.ID), PublicModel: s.call.Model, UpstreamModel: sent.UpstreamModel, Wire: s.call.WireOperation, ObservationDigest: seal.Digest, PolicyVersion: seal.PolicyVersion}, Completed: true, Storage: storage, ResponseID: id, OutputItemIDs: ids, CreatedAt: seal.CreatedAt, ExpiresAt: seal.ExpiresAt}
	if s.adapter.history.Register(ctx, completion) != nil {
		return ErrNativeHistory
	}
	return nil
}

// NativeHistoryInputBound is the deployment certificate's entire accepted context,
// including hidden provider history. Catalog admission cannot substitute a local
// tokenizer estimate for it.
func (a *Adapter) NativeHistoryInputBound(model, wire string) int {
	if wire == "responses.compact" {
		return a.config.CompactPolicies[model].HardInputTokens
	}
	if p, ok := a.config.NativeInputPolicies[wire][model]; ok {
		return p.HardInputTokens
	}
	return a.config.MaxInputTokens
}
