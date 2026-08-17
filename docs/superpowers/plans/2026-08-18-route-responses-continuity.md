# Route Execution and Responses Continuity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make resolved route targets authoritative, reject or skip candidates that cannot fit the request, and replace unscoped Responses replay with bounded versioned continuity that persists only valid completed turns.

**Architecture:** The model catalog returns an ordered immutable candidate list carrying route metadata. The controller owns attempt advancement, pins each attempt to the candidate's channel/model/billing identity, and re-prepares pricing before send. Responses state uses a v2 scope record and parent links instead of recursively materialized input; handlers normalize `store:false`, fail closed on scope errors, and persist only eligible terminal responses.

**Tech Stack:** Go 1.22+, Gin, GORM v2, Redis, testify, existing model catalog, relay decision trace, and Responses DTOs.

## Global Constraints

- Existing public Chat Completions, Responses, Responses Compact, Anthropic Messages, Gemini, and MCP contracts must remain stable unless the approved design explicitly tightens unsafe behavior.
- Existing Gateway channel type and model-routing data must remain compatible.
- Every behavior change must be developed with a failing regression test first.
- Upstream commits are reference material, not cherry-pick requirements; implementations must follow the current repository's package boundaries.
- Unsafe or ambiguous state must fail closed with a stable client-visible error and must not mutate billing, continuity state, or channel status.
- Legacy Responses records without the full v2 scope must be rejected; no compatibility replay is permitted.
- Omitted Responses `store` must become the explicit JSON value `false`; explicit `store:true` remains unchanged.
- The unrelated untracked file `scripts/prepare-sidecar-env.py` must not be modified or committed.
- All JSON encoding and decoding must use the wrappers in `common/json.go`.
- Database behavior must remain compatible with SQLite, MySQL 5.7.8+, and PostgreSQL 9.6+.

---

### Task 1: Return ordered route candidates with context-fit metadata

**Files:**
- Modify: `model/client_model_route.go`
- Modify: `service/modelcatalog/catalog.go`
- Modify: `service/modelcatalog/catalog_test.go`

**Interfaces:**
- Consumes: existing authorization snapshots, target priority ordering, route metadata JSON, and wire decoding.
- Produces: `ResolveCandidates(ctx context.Context, userID int, groups []string, requestedModel, client string) ([]ResolvedModel, error)`; `Resolve` remains a first-candidate compatibility wrapper.

- [ ] **Step 1: Write failing candidate and context metadata tests**

Add tests proving authorized targets are returned in priority/ID order, every candidate carries its target-specific context capacity (falling back to route metadata for legacy rows), and returned slices are independent copies.

```go
func TestResolveCandidatesReturnsOrderedAuthorizedTargets(t *testing.T) {
	catalog, err := NewCatalog(
		[]ClientModelRoute{{
			Id: 1, PublicId: "coder", Client: "all", Enabled: true,
			Metadata: `{"context_window":32000,"context_fit_required":true}`,
		}},
		[]ClientModelTarget{
			{Id: 20, RouteId: 1, ChannelId: 12, UpstreamModel: "model-b", BillingModel: "price-b", PreferredWires: `["responses"]`, Priority: 5, Weight: 1, Enabled: true, ContextWindow: 200000, ContextFitRequired: true},
			{Id: 10, RouteId: 1, ChannelId: 11, UpstreamModel: "model-a", BillingModel: "price-a", PreferredWires: `["responses"]`, Priority: 10, Weight: 1, Enabled: true, ContextWindow: 8000, ContextFitRequired: true},
		})
	require.NoError(t, err)
	catalog = catalog.WithAuthorizer(testAuthorizer{})

	got, err := catalog.ResolveCandidates(context.Background(), 11, []string{"default"}, "coder", "codex")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []int{10, 20}, []int{got[0].TargetID, got[1].TargetID})
	assert.Equal(t, 8000, got[0].ContextWindow)
	assert.Equal(t, 200000, got[1].ContextWindow)
	assert.True(t, got[0].RequiresKnownContext)
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./service/modelcatalog -run 'TestResolveCandidates' -count=1`

Expected: build failure because `ResolveCandidates`, `ResolvedModel.ContextWindow`, and `ResolvedModel.RequiresKnownContext` do not exist.

- [ ] **Step 3: Extend the immutable catalog result**

```go
type routeMetadata struct {
	ContextWindow            int      `json:"context_window"`
	ContextFitRequired       bool     `json:"context_fit_required"`
	ReasoningEfforts         []string `json:"reasoning_efforts"`
	SupportsReasoningSummary bool     `json:"supports_reasoning_summary"`
	SupportsTools            bool     `json:"supports_tools"`
}

// Add these fields to the existing ClientModelTarget declaration without
// GORM default tags; existing rows scan to their Go zero values and retain
// legacy behavior.
ContextWindow      int  `json:"context_window"`
ContextFitRequired bool `json:"context_fit_required"`

type ResolvedModel struct {
	RequestedModel      string
	PublicID            string
	UpstreamModel       string
	BillingModel        string
	ChannelID           int
	RouteID             int
	TargetID            int
	Priority            int
	Weight              int
	Client              string
	PreferredWires      []WireProtocol
	ContextWindow       int
	RequiresKnownContext bool
}
```

Implement `ResolveCandidates` by running authorization once and appending every visible target in the catalog's existing stable order. A positive target `ContextWindow` overrides route metadata; otherwise the route value is used. `RequiresKnownContext` is true when either target or route requires it. Each result gets a copied wire slice. Implement `Resolve` as:

```go
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
```

- [ ] **Step 4: Verify GREEN**

Run: `go test -race ./service/modelcatalog -count=1`

Expected: PASS with no race report; legacy `Resolve` tests remain unchanged.

- [ ] **Step 5: Commit Task 1**

```bash
git add model/client_model_route.go service/modelcatalog/catalog.go service/modelcatalog/catalog_test.go
git commit -m "feat(routing): expose ordered route candidates"
```

### Task 2: Enforce context fit and authoritative target binding

**Files:**
- Modify: `relay/common/relay_info.go`
- Modify: `controller/relay.go`
- Modify: `controller/relay_route_test.go`
- Modify: `service/modelcatalog/route_policy.go`
- Modify: `service/modelcatalog/route_policy_test.go`
- Modify: `types/error.go`

**Interfaces:**
- Consumes: `ResolveCandidates` from Task 1, `model.GetChannelById(id, true)`, existing `ModelPriceHelper`, `PreConsumeBilling`, and `BillingSettler.Reserve`.
- Produces: `relaycommon.RouteCandidate`, `selectRouteCandidateForAttempt`, error code `context_capacity_exceeded`, and trace attempts containing route/target identity.

- [ ] **Step 1: Write failing admission and pinning tests**

Cover four contracts: an undersized candidate is skipped before billing/send; a later eligible target is selected; no eligible target returns `context_capacity_exceeded`; and `getChannel` loads the explicit candidate channel instead of calling the random distributor.

```go
func TestFilterRouteCandidatesSkipsKnownUndersizedTargets(t *testing.T) {
	candidates := []modelcatalog.ResolvedModel{
		{TargetID: 1, ChannelID: 11, ContextWindow: 8000, RequiresKnownContext: true},
		{TargetID: 2, ChannelID: 12, ContextWindow: 128000, RequiresKnownContext: true},
	}
	got := filterRouteCandidatesByContext(candidates, 12000)
	require.Len(t, got, 1)
	assert.Equal(t, 2, got[0].TargetID)
}

func TestFilterRouteCandidatesPreservesLegacyUnknownCapacity(t *testing.T) {
	candidates := []modelcatalog.ResolvedModel{{TargetID: 1, ChannelID: 11, ContextWindow: 0, RequiresKnownContext: false}}
	got := filterRouteCandidatesByContext(candidates, 12000)
	require.Len(t, got, 1)
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./controller ./service/modelcatalog -run '(RouteCandidate|ContextCapacity|ExplicitTarget|OrderedFallback)' -count=1`

Expected: build failure for the new candidate/filter/trace fields.

- [ ] **Step 3: Add neutral route-attempt state to RelayInfo**

```go
type RouteCandidate struct {
	RouteID              int
	TargetID             int
	ChannelID            int
	UpstreamModel        string
	BillingModel         string
	SelectedWire         string
	ContextWindow        int
	RequiresKnownContext bool
}
```

Add these `RelayInfo` fields without importing `service/modelcatalog` into `relay/common`:

```go
ModelTargetId       int
ResolvedChannelId  int
RouteContextWindow int
RouteCandidates    []RouteCandidate
RouteCandidateIndex int
```

Extend `modelcatalog.Attempt` with `RouteID`, `TargetID`, and `RejectedReason`. Preserve existing JSON fields.

- [ ] **Step 4: Resolve, filter, and bind candidates before pricing**

Change `resolveClientModelRoute` to accept `estimatedInputTokens int` and return `*types.NewAPIError`. A catalog miss preserves legacy behavior; a matched route whose candidates are all ineligible returns HTTP 400 with `types.ErrorCodeContextCapacityExceeded` and `types.ErrOptionWithSkipRetry()`.

```go
func filterRouteCandidatesByContext(candidates []modelcatalog.ResolvedModel, estimated int) []modelcatalog.ResolvedModel {
	eligible := make([]modelcatalog.ResolvedModel, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ContextWindow > 0 && estimated > candidate.ContextWindow {
			continue
		}
		if candidate.ContextWindow == 0 && candidate.RequiresKnownContext {
			continue
		}
		eligible = append(eligible, candidate)
	}
	return eligible
}
```

Convert each eligible catalog result to `relaycommon.RouteCandidate`. Set `RouteCandidateIndex = 0`, then copy the first candidate's route, target, channel, upstream model, billing model, context, and selected wire to the authoritative RelayInfo fields.

- [ ] **Step 5: Pin each attempt and refresh its billing identity**

At the start of each retry-loop iteration, bind the current explicit candidate. `getChannel` must take this branch before the random distributor:

```go
if len(info.RouteCandidates) > 0 {
	candidate := info.RouteCandidates[info.RouteCandidateIndex]
	channel, err := model.GetChannelById(candidate.ChannelID, true)
	if err != nil || channel == nil {
		return nil, types.NewError(fmt.Errorf("resolved route target %d channel %d is unavailable", candidate.TargetID, candidate.ChannelID), types.ErrorCodeGetChannelFailed)
	}
	if setupErr := middleware.SetupContextForSelectedChannel(c, channel, candidate.UpstreamModel); setupErr != nil {
		return nil, setupErr
	}
	return channel, nil
}
```

Before the first attempt and whenever the candidate index advances, set `ResolvedModelName`, `BillingModelName`, `ModelRouteId`, `ModelTargetId`, `ResolvedChannelId`, and `SelectedWire`, then call `helper.ModelPriceHelper`. For a paid candidate with no billing session call `service.PreConsumeBilling`; for an existing session call `Reserve(priceData.QuotaToPreConsume)` and update `FinalPreConsumedQuota`. A cheaper or free final candidate is settled by the existing session against the final `PriceData`; do not pre-refund between attempts.

Advance `RouteCandidateIndex` only after `shouldRetry` accepts the failure and another explicit candidate exists. Do not fall through to the random distributor after the explicit candidate list is exhausted.

- [ ] **Step 6: Verify GREEN and no-send/no-charge behavior**

Run: `go test -race ./controller ./service/modelcatalog -run '(Route|Candidate|ContextCapacity|Fallback|BillingIdentity)' -count=1`

Expected: PASS; rejected candidates generate trace entries with `RejectedReason="context_capacity_exceeded"`, zero upstream calls, and no quota mutation.

- [ ] **Step 7: Commit Task 2**

```bash
git add relay/common/relay_info.go controller/relay.go controller/relay_route_test.go service/modelcatalog/route_policy.go service/modelcatalog/route_policy_test.go types/error.go
git commit -m "fix(routing): bind explicit targets and enforce context fit"
```

### Task 3: Introduce v2 scoped Responses records and reject legacy state

**Files:**
- Modify: `service/responsesstate/store.go`
- Modify: `service/responsesstate/memory.go`
- Modify: `service/responsesstate/redis.go`
- Modify: `service/responsesstate/store_test.go`
- Modify: `service/responsesstate/default_test.go`

**Interfaces:**
- Consumes: existing memory/Redis TTL and size limits.
- Produces: `CurrentVersion = 2`, `Scope`, `Record.ParentResponseID`, `Store.Load(ctx, scope, responseID)`, `ErrLegacyScope`, and `ErrScopeMismatch`.

- [ ] **Step 1: Write failing scope-isolation tests**

Test tenant/user, token, route, target/channel, model, and explicit client-task mismatches independently. Seed the old unversioned key and assert `ErrLegacyScope`, not `ErrNotFound`. Assert that new v2 keys do not contain raw response IDs.

```go
func TestStoreRejectsLegacyUnscopedRecord(t *testing.T) {
	store := NewMemory(time.Hour, 1<<20)
	legacy := Record{TenantID: 1, TokenID: 2, ResponseID: "resp_old", Input: json.RawMessage(`[]`), Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(time.Hour)}
	store.records[legacyKey(1, 2, "resp_old")] = legacy
	_, err := store.Load(context.Background(), Scope{TenantID: 1, UserID: 1, TokenID: 2, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}, "resp_old")
	require.ErrorIs(t, err, ErrLegacyScope)
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./service/responsesstate -run '(LegacyScope|ScopeMismatch|V2Key|TaskIsolation)' -count=1`

Expected: build failure because the v2 scope API and errors do not exist.

- [ ] **Step 3: Define the versioned record and store contract**

```go
const CurrentVersion = 2

var (
	ErrNotFound      = errors.New("responses state record not found")
	ErrReplayRejected = errors.New("responses replay rejected")
	ErrLegacyScope   = errors.New("responses state uses a legacy scope")
	ErrScopeMismatch = errors.New("responses state scope mismatch")
)

type Scope struct {
	TenantID    int    `json:"tenant_id"`
	UserID      int    `json:"user_id"`
	TokenID     int    `json:"token_id"`
	RouteID     int    `json:"route_id"`
	TargetID    int    `json:"target_id"`
	ChannelID   int    `json:"channel_id"`
	ClientTaskID string `json:"client_task_id"`
	Model       string `json:"model"`
}

type Record struct {
	Version          int             `json:"version"`
	Scope            Scope           `json:"scope"`
	ResponseID       string          `json:"response_id"`
	ParentResponseID string          `json:"parent_response_id,omitempty"`
	Input            json.RawMessage `json:"input"`
	Output           json.RawMessage `json:"output"`
	ExpiresAt        time.Time       `json:"expires_at"`
}

type Store interface {
	Load(ctx context.Context, expected Scope, responseID string) (Record, error)
	Save(ctx context.Context, record Record) error
}
```

`Key` must start with `responses-state:v2:` and hash the response ID. Retain a private `legacyKey(tenantID, tokenID, responseID)` solely to detect and reject old records. `ValidateRecord` requires version 2 and every scope identity except `ClientTaskID`, which is filled before save.

- [ ] **Step 4: Enforce scope in memory and Redis implementations**

Load the v2 key, validate the record, and compare all non-empty expected scope fields. When `expected.ClientTaskID` is empty, adopt the stored server-issued task ID; when it is non-empty, require equality. If the v2 key is absent, probe the legacy key and return `ErrLegacyScope` when it exists. Redis must use `common.Unmarshal`/`common.Marshal`, not direct `encoding/json` functions.

```go
func scopeMatches(expected, actual Scope) bool {
	return expected.TenantID == actual.TenantID &&
		expected.UserID == actual.UserID &&
		expected.TokenID == actual.TokenID &&
		expected.RouteID == actual.RouteID &&
		expected.TargetID == actual.TargetID &&
		expected.ChannelID == actual.ChannelID &&
		expected.Model == actual.Model &&
		(expected.ClientTaskID == "" || expected.ClientTaskID == actual.ClientTaskID)
}
```

- [ ] **Step 5: Verify GREEN**

Run: `go test -race ./service/responsesstate -run '(Store|Scope|Legacy|Redis|Memory)' -count=1`

Expected: PASS with no race report.

- [ ] **Step 6: Commit Task 3**

```bash
git add service/responsesstate/store.go service/responsesstate/memory.go service/responsesstate/redis.go service/responsesstate/store_test.go service/responsesstate/default_test.go
git commit -m "fix(responses): version and scope continuity records"
```

### Task 4: Make replay expansion linear and bounded

**Files:**
- Modify: `service/responsesstate/store.go`
- Modify: `service/responsesstate/store_test.go`

**Interfaces:**
- Consumes: v2 `Store` and `Record.ParentResponseID` from Task 3.
- Produces: `Expansion`, `Expand(ctx, store, expectedScope, request) (Expansion, error)`, and `SaveTurn(ctx, store, record) error`.

- [ ] **Step 1: Write failing linear-history tests**

Create a three-turn chain where each record stores only its own input/output and parent ID. Assert expanded item count is exactly the sum of the three turns plus current input, and that saving a child never embeds an ancestor's materialized items. Preserve cycle, `MaxReplayHops`, TTL, and byte ceiling tests.

```go
type Expansion struct {
	Request          json.RawMessage
	Scope            Scope
	ParentResponseID string
	CurrentInput     json.RawMessage
}

func TestExpandUsesParentLinksWithoutResavingMaterializedHistory(t *testing.T) {
	// Seed resp_1 <- resp_2 with one input and one output per record.
	// Expanding resp_2 plus the current input must yield five items, while
	// Expansion.CurrentInput remains the single current item.
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./service/responsesstate -run '(ParentLinks|Linear|ReplayHops|Cycles)' -count=1`

Expected: build failure for `Expansion` and the scoped `Expand` signature.

- [ ] **Step 3: Replace pseudo-parent items with explicit parent links**

Walk `Record.ParentResponseID` for at most `MaxReplayHops`, collect each record once, then append in root-to-leaf order. Keep a `seen` map for cycles. Never inspect stored input for a synthetic `previous_response_id` item. Preserve the original current input separately in `Expansion.CurrentInput` and delete `previous_response_id` only from `Expansion.Request`.

Make `SaveTurn` return an error and accept a complete v2 `Record`; callers decide whether persistence is best-effort after eligibility checks.

```go
func SaveTurn(ctx context.Context, store Store, record Record) error {
	if store == nil {
		return errors.New("responses state store is nil")
	}
	if record.Scope.ClientTaskID == "" {
		record.Scope.ClientTaskID = record.ResponseID
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = time.Now().Add(DefaultRecordTTL)
	}
	return store.Save(ctx, record)
}
```

- [ ] **Step 4: Verify GREEN and bounded complexity**

Run: `go test -race ./service/responsesstate -run '(Expand|Parent|Linear|Replay|Record)' -count=1`

Expected: PASS; a chain of N retained turns loads N records and serialized child size does not grow with ancestor materialization.

- [ ] **Step 5: Commit Task 4**

```bash
git add service/responsesstate/store.go service/responsesstate/store_test.go
git commit -m "fix(responses): make continuity expansion linear"
```

### Task 5: Fail closed in handlers and persist only eligible completions

**Files:**
- Modify: `constant/context_key.go`
- Modify: `relay/responses_handler.go`
- Modify: `relay/responses_handler_test.go`
- Modify: `relay/channel/openai/relay_responses.go`
- Modify: `relay/channel/openai/responses_state_test.go`
- Modify: `service/responsesstate/store.go`
- Modify: `types/error.go`

**Interfaces:**
- Consumes: scoped linear expansion from Tasks 3–4 and authoritative route identity from Task 2.
- Produces: `ContinuityContext`, `ParseClientTaskID`, `EligibleCompletedResponse`, and error code `responses_state_scope_invalid`.

- [ ] **Step 1: Write failing handler-policy tests**

Cover omitted `store`→`false`, explicit `true` preservation, legacy/mismatch errors returned to the client, bounded `client_metadata.task_id`, server-issued task ID inheritance, refusal exclusion, incomplete exclusion, error exclusion, cancelled stream exclusion, and completed valid persistence.

```go
func TestResponsesHelperDefaultsOmittedStoreToFalse(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{Model: "model-a", Input: json.RawMessage(`[]`)}
	// Run the request normalization seam and assert the converted request
	// contains the exact JSON boolean false, not an omitted field.
	normalizeResponsesStore(req)
	assert.JSONEq(t, `false`, string(req.Store))
}

func TestEligibleCompletedResponseRejectsRefusal(t *testing.T) {
	resp := &dto.OpenAIResponsesResponse{
		ID: "resp_1", Status: json.RawMessage(`"completed"`),
		Output: []dto.ResponsesOutput{{Type: "message", Content: []dto.ResponsesOutputContent{{Type: "refusal", Refusal: "no"}}}},
	}
	assert.False(t, responsesstate.EligibleCompletedResponse(resp))
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./relay ./relay/channel/openai ./service/responsesstate -run '(StoreDefault|StateScope|ClientTask|Eligible|Refusal|Incomplete|Persist)' -count=1`

Expected: build failures for the normalization, continuity context, and eligibility helpers.

- [ ] **Step 3: Normalize store and derive only bounded task identity**

```go
func normalizeResponsesStore(request *dto.OpenAIResponsesRequest) {
	if request != nil && len(request.Store) == 0 {
		request.Store = json.RawMessage("false")
	}
}
```

`ParseClientTaskID` reads only `client_metadata.task_id`, trims it, limits it to 128 bytes, and accepts `[A-Za-z0-9._:-]+`. Invalid non-empty values return `types.ErrorCodeInvalidRequest`. If a first turn has no explicit task ID, persistence sets it to the response ID. If a continuation has no explicit task ID, expansion adopts the stored scope's server-issued task ID.

- [ ] **Step 4: Replace silent expansion fallback with a stable error**

Build the expected scope from `RelayInfo.UserId`, `TokenId`, `ModelRouteId`, `ModelTargetId`, `ResolvedChannelId`, the parsed task ID, and `ResolvedModelNameOrOrigin`. Map `ErrLegacyScope` and `ErrScopeMismatch` to HTTP 409, `types.ErrorCodeResponsesStateScopeInvalid`, skip retry, and return before upstream/billing mutation. Keep unavailable state storage as a server error rather than silently dropping the client's continuity request.

Store this value in Gin context:

```go
type ContinuityContext struct {
	Scope            responsesstate.Scope
	ParentResponseID string
	CurrentInput     json.RawMessage
}
```

- [ ] **Step 5: Gate persistence on a valid terminal response**

`EligibleCompletedResponse` requires a non-empty response ID, status exactly `"completed"`, nil `Error`, nil `IncompleteDetails`, and no output/content type `refusal`. Stream code calls persistence only after a valid `response.completed` terminal event; cancellation, malformed terminal events, and upstream errors leave no record.

Persist the original `CurrentInput`, not expanded materialization, and set `ParentResponseID` from the request context. Log a save failure as best-effort only after eligibility passes.

- [ ] **Step 6: Verify GREEN**

Run: `go test -race ./service/responsesstate ./relay ./relay/channel/openai -run '(Responses|Store|Scope|Task|Eligible|Persist|Refusal|Incomplete|Cancelled)' -count=1`

Expected: PASS; unsafe state never reaches upstream and ineligible outputs never enter memory/Redis.

- [ ] **Step 7: Commit Task 5**

```bash
git add constant/context_key.go dto/openai_request.go relay/responses_handler.go relay/responses_handler_test.go relay/channel/openai/relay_responses.go relay/channel/openai/responses_state_test.go service/responsesstate/store.go types/error.go
git commit -m "fix(responses): fail closed on scoped continuity"
```

### Task 6: Subproject verification gate

**Files:**
- No planned file changes; this task is a verification-only gate.

**Interfaces:**
- Consumes: all Task 1–5 outputs.
- Produces: a separately reviewable route/continuity subproject with fresh evidence.

- [ ] **Step 1: Run focused race suites**

Run: `go test -race ./service/modelcatalog ./service/responsesstate ./controller ./relay ./relay/channel/openai -count=1`

Expected: PASS with no race report.

- [ ] **Step 2: Run offline route and protocol contracts**

Run: `go test ./service/modelcatalog ./controller ./relay/... -count=1`

Expected: PASS, including explicit target, fallback, Responses, compact, and conversion suites.

- [ ] **Step 3: Run full backend and hygiene gates**

Run: `go test ./... && go vet ./...`

Expected: exit 0.

Run: `gofmt -w service/modelcatalog/catalog.go service/modelcatalog/catalog_test.go service/modelcatalog/route_policy.go service/modelcatalog/route_policy_test.go relay/common/relay_info.go controller/relay.go controller/relay_route_test.go service/responsesstate/store.go service/responsesstate/memory.go service/responsesstate/redis.go service/responsesstate/store_test.go service/responsesstate/default_test.go constant/context_key.go dto/openai_request.go relay/responses_handler.go relay/responses_handler_test.go relay/channel/openai/relay_responses.go relay/channel/openai/responses_state_test.go types/error.go && git diff --check`

Expected: no formatting or whitespace diagnostics.

- [ ] **Step 4: Verify scope and checkpoint**

Run: `git diff --name-only HEAD~5..HEAD && git diff -- scripts/prepare-sidecar-env.py`

Expected: only route, relay, Responses-state, DTO/context, error, and test files appear; the sidecar helper has no diff.

If the gate required a code repair, stage the exact repaired paths shown by `git status --short` and commit with `git commit -m "test(core): close route continuity verification gaps"`. If no repair was needed, do not create a commit.
