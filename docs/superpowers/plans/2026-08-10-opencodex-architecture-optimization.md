# OpenCodex-Informed Client Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Codex CLI and Claude Code first-class clients of Codex Gateway by adding stable public model identities, capability-driven native protocol routing, complete Responses tool/state continuity, safe client configuration, and explainable failover.

**Architecture:** Keep the existing Go multi-tenant gateway, channel selection, billing, token authorization, and provider adaptors. Add a client-compatibility layer before billing that resolves a public model ID into an upstream model and preferred wire protocol, then reuse the existing capability registry and relay conversion packages; stateful Responses continuity is isolated behind a bounded store, and CLI configuration is made reversible without importing OpenCodex's local-only security assumptions.

**Tech Stack:** Go 1.25.1, Gin, GORM, Redis, existing relay/capability and relayconvert packages, TypeScript 5.7, Node 18+, existing CLI Node test runner, Docker Compose, Codex CLI, Claude Code.

## Global Constraints

- Preserve existing channel selection, grouping, quotas, billing, API-token authorization, and provider keys.
- Resolve public aliases before price lookup and pre-consumption; store both requested and resolved model names in audit metadata.
- Prefer native `responses` for a model only when the channel declaration or a successful capability probe proves it; do not infer protocol support from an OpenAI-compatible base URL alone.
- Preserve native DeepSeek Responses routing and the existing custom-tool SSE compatibility work.
- Never route management endpoints with a normal relay token, and never copy OpenCodex's loopback-only trust model into the public multi-tenant server.
- Client configuration changes must be atomic, permission-safe, reversible, and must refuse to overwrite a file owned by another user.
- Stateful replay data must be tenant-scoped, token-scoped, bounded by TTL and byte size, redacted from logs, and disabled when Redis is unavailable in multi-instance production.
- `tool_search` support must preserve exact namespaced wire names and reject unknown or unauthorized deferred tools.
- Existing unrelated worktree changes and `.codebase-memory/` artifacts are preserved.
- No provider OAuth/account-pool implementation is included in this plan; current API-key channels remain the supported credential model.

---

## Audit Baseline and File Map

The OpenCodex v2.11.1 reference is pinned at commit `121f1ad929dc6da3356c06f5192f2f97f7a5dde5` under `/Users/neomei/项目/opencodex 移植/opencodex-latest`. Its persistent graph contains 50,496 nodes and 122,289 edges. The current gateway graph contains 58,108 nodes and 128,515 edges.

Observed gaps that this plan closes:

| Area | Current gateway | OpenCodex mechanism worth borrowing | Decision |
|---|---|---|---|
| Client model identity | CLI writes `deepseek-v4-flash`; `/v1/models` exposes enabled channel names only | Reversible aliases plus provider/model routes and a converged catalog | Implement a server-owned public catalog and client-safe aliases |
| Wire protocol | Mostly selected by channel type/adaptor | Per-model `modelAdapters` with validated defaults and overrides | Add per-model ordered wire preferences and observed capability state |
| Responses tools | Strong function/custom conversion; `additional_tools` handling is DeepSeek-specific | Central parser handles normal, custom, namespace, additional, and tool-search tools | Move normalization into a provider-neutral compatibility kernel |
| Stateful Responses | Native forwarding accepts `previous_response_id`; chat conversion rejects it | Bounded replay store, compaction continuity, reasoning replay cache | Add tenant-scoped state store and replay expansion |
| Client setup | One backup file and shell-profile writes | Ownership checks, atomic convergence, native restore, diagnostics | Add managed blocks, receipts, restore, and doctor commands |
| Failover visibility | Channel affinity/cooldown and key pools exist | Virtual combos, policy fallback, attempt sequence, route-decision trace | Reuse current routing and add public virtual routes plus bounded traces |
| Security | Production management bearer exists; CORS allows all origins with credentials | Separate management/data-plane admission and strict origin validation | Keep server auth, fix CORS and add explicit trusted-origin parsing |

Focused implementation units:

- `model/client_model_route.go`: persistent public-model route and target records.
- `service/modelcatalog/catalog.go`: catalog listing, alias resolution, metadata, and billing identity.
- `service/modelcatalog/catalog_test.go`: deterministic alias, visibility, and resolution tests.
- `controller/model.go`: protocol-specific model-list serialization and empty-list safety.
- `controller/client_model_route.go`: management CRUD and validation for public routes.
- `router/api-router.go`: management route registration.
- `relay/protocol/wire.go`: ordered per-model wire resolution.
- `relay/protocol/probe.go`: bounded native-protocol probes and observed-capability TTL.
- `relay/compat/responses_tools.go`: provider-neutral tool normalization.
- `service/responsesstate/store.go`: state-store interface and replay record limits.
- `service/responsesstate/redis.go`: multi-instance implementation.
- `service/responsesstate/memory.go`: development/single-instance implementation.
- `relay/responses_handler.go`: replay expansion and completed-response persistence.
- `relay/common/relay_info.go`: requested/resolved model, wire, route, and attempt metadata.
- `service/log_info_generate.go`: redacted route-decision audit fields.
- `cli/src/client-state.ts`: atomic writes, ownership checks, receipts, and restore.
- `cli/src/codex.ts`: catalog-driven Codex configuration.
- `cli/src/claude.ts`: Claude aliases and managed environment block.
- `cli/src/doctor.ts`: end-to-end client diagnostics.
- `middleware/cors.go`: trusted-origin enforcement.
- `scripts/client-e2e.sh`: real Codex/Claude protocol and tool-loop acceptance matrix.

Stable interfaces introduced by this plan:

```go
package modelcatalog

type WireProtocol string

const (
    WireResponses WireProtocol = "responses"
    WireOpenAIChat WireProtocol = "openai_chat"
    WireAnthropic WireProtocol = "anthropic"
)

type ClientModel struct {
    PublicID                 string         `json:"id"`
    DisplayName              string         `json:"display_name"`
    Aliases                  []string       `json:"aliases,omitempty"`
    PreferredWires           []WireProtocol `json:"preferred_wires"`
    ContextWindow            int            `json:"context_window,omitempty"`
    ReasoningEfforts         []string       `json:"reasoning_efforts,omitempty"`
    SupportsReasoningSummary bool           `json:"supports_reasoning_summary"`
    SupportsTools            bool           `json:"supports_tools"`
)

type ResolvedModel struct {
    RequestedModel string
    PublicID       string
    UpstreamModel  string
    BillingModel   string
    ChannelID      int
    PreferredWires []WireProtocol
    RouteID        int
}

type Catalog interface {
    List(ctx context.Context, userID int, groups []string, client string) ([]ClientModel, error)
    Resolve(ctx context.Context, userID int, groups []string, requested, client string) (ResolvedModel, error)
}
```

```go
package responsesstate

type Record struct {
    TenantID   int
    TokenID    int
    ResponseID string
    Model      string
    Input      json.RawMessage
    Output     json.RawMessage
    ExpiresAt  time.Time
}

type Store interface {
    Load(ctx context.Context, tenantID, tokenID int, responseID string) (Record, error)
    Save(ctx context.Context, record Record) error
    Delete(ctx context.Context, tenantID, tokenID int, responseID string) error
    Health(ctx context.Context) error
}
```

```ts
export type ClientReceipt = {
  client: 'codex' | 'claude'
  managedPaths: string[]
  backups: Array<{ original: string; backup: string; sha256: string }>
  managedEnvKeys: string[]
  createdAt: string
}
```

## Batch 1: Public Model Identity and Native Wire Resolution

### Task 1: Add persistent public model routes and deterministic alias resolution

**Files:**
- Create: `model/client_model_route.go`
- Create: `service/modelcatalog/catalog.go`
- Test: `service/modelcatalog/catalog_test.go`
- Modify: `model/main.go`

**Interfaces:**
- Consumes: enabled channels, channel group visibility, `Channel.GetModelMapping()`, and existing pricing helpers.
- Produces: `modelcatalog.Catalog`, `ClientModel`, and `ResolvedModel` exactly as declared above.

- [x] **Step 1: Write failing resolution tests**

```go
func TestCatalogResolvesAliasBeforeBilling(t *testing.T) {
    catalog := newTestCatalog(t, routeFixture{
        PublicID: "deepseek/deepseek-v4-flash",
        Aliases: []string{"deepseek-v4-flash", "gpt-5.6-sol"},
        ChannelID: 7,
        UpstreamModel: "deepseek-v4-flash",
        BillingModel: "deepseek-v4-flash",
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
```

- [x] **Step 2: Run the focused test and confirm the missing package failure**

Run: `go test ./service/modelcatalog -run 'TestCatalog(ResolvesAliasBeforeBilling|RejectsAliasCollision)' -count=1`

Expected: FAIL because `service/modelcatalog` and the public-route model do not exist.

- [x] **Step 3: Implement the persistent records and immutable in-memory index**

```go
type ClientModelRoute struct {
    Id          int    `json:"id" gorm:"primaryKey"`
    PublicId    string `json:"public_id" gorm:"size:191;uniqueIndex;not null"`
    DisplayName string `json:"display_name" gorm:"size:191;not null"`
    Client      string `json:"client" gorm:"size:32;index;not null;default:all"`
    Aliases     string `json:"aliases" gorm:"type:text"`
    Metadata    string `json:"metadata" gorm:"type:text"`
    Enabled     bool   `json:"enabled" gorm:"index;not null;default:true"`
    CreatedTime int64  `json:"created_time"`
    UpdatedTime int64  `json:"updated_time"`
}

type ClientModelTarget struct {
    Id             int    `json:"id" gorm:"primaryKey"`
    RouteId        int    `json:"route_id" gorm:"index;not null"`
    ChannelId      int    `json:"channel_id" gorm:"index;not null"`
    UpstreamModel  string `json:"upstream_model" gorm:"size:191;not null"`
    BillingModel   string `json:"billing_model" gorm:"size:191;not null"`
    PreferredWires string `json:"preferred_wires" gorm:"type:text;not null"`
    Priority       int    `json:"priority" gorm:"not null;default:0"`
    Weight         int    `json:"weight" gorm:"not null;default:1"`
    Enabled        bool   `json:"enabled" gorm:"index;not null;default:true"`
}
```

Build one lowercase lookup index for public IDs and aliases. Validation rejects empty IDs, control characters, duplicate aliases, nonexistent channels, disabled targets, unknown wires, weights below one, and a `BillingModel` without billing configuration. Return defensive copies from `List` and `Resolve`.

- [x] **Step 4: Register both records in the existing GORM migration list and run tests**

Run: `go test ./service/modelcatalog ./model -count=1`

Expected: PASS, including collision, visibility, disabled-target, invalid-wire, and defensive-copy cases.

- [x] **Step 5: Commit the model-catalog core**

```bash
git add model/client_model_route.go model/main.go service/modelcatalog
git commit -m "feat: add public client model catalog"
```

### Task 2: Make model listing client-aware and safe for empty catalogs

**Files:**
- Modify: `controller/model.go:208`
- Create: `controller/client_model_route.go`
- Test: `controller/model_list_test.go`
- Test: `controller/client_model_route_test.go`
- Modify: `router/api-router.go`
- Create: `web/src/features/client-model-routes/api.ts`
- Create: `web/src/features/client-model-routes/client-model-routes-page.tsx`
- Test: `web/src/features/client-model-routes/client-model-routes-page.test.tsx`
- Modify: `web/src/routes.tsx`

**Interfaces:**
- Consumes: `modelcatalog.Catalog.List` and `modelcatalog.Catalog.Resolve`.
- Produces: `/v1/models`, Anthropic model listing, and management CRUD under `/api/client_model_routes`.

- [x] **Step 1: Add failing empty-list and alias-listing tests**

```go
func TestListModelsAnthropicEmptyCatalogDoesNotPanic(t *testing.T) {
    recorder, ctx := newModelListRequest(t, constant.ChannelTypeAnthropic, emptyCatalog())
    require.NotPanics(t, func() { ListModels(ctx, constant.ChannelTypeAnthropic) })
    require.Equal(t, http.StatusOK, recorder.Code)
    require.JSONEq(t, `{"data":[],"first_id":null,"has_more":false,"last_id":null}`, recorder.Body.String())
}

func TestListModelsIncludesPublicIDAndCodexAlias(t *testing.T) {
    recorder, ctx := newModelListRequest(t, constant.ChannelTypeOpenAI, deepSeekCatalog())
    ListModels(ctx, constant.ChannelTypeOpenAI)
    require.Contains(t, recorder.Body.String(), `"id":"deepseek/deepseek-v4-flash"`)
    require.Contains(t, recorder.Body.String(), `"id":"gpt-5.6-sol"`)
}
```

- [x] **Step 2: Prove the current Anthropic path can index an empty slice**

Run: `go test ./controller -run 'TestListModels(AnthropicEmptyCatalogDoesNotPanic|IncludesPublicIDAndCodexAlias)' -count=1`

Expected: FAIL; the current implementation reads elements zero and `len-1` without an empty guard and has no public catalog.

- [x] **Step 3: Serialize client catalog entries and add validated management CRUD**

```go
func anthropicModelPage(models []dto.AnthropicModel) gin.H {
    page := gin.H{"data": models, "first_id": nil, "has_more": false, "last_id": nil}
    if len(models) != 0 {
        page["first_id"] = models[0].ID
        page["last_id"] = models[len(models)-1].ID
    }
    return page
}
```

The CRUD handler must validate the whole route and all targets in a transaction, reject alias collisions with HTTP 409, invalidate the immutable catalog snapshot only after commit, and require the existing admin authorization middleware.

The admin page lists public IDs, aliases, client scope, target channel/model, ordered wire preferences, priority, weight, and enabled state. Its form fetches channel and provider-model choices from existing management APIs, prevents duplicate aliases before submission, and renders server validation errors without exposing channel keys.

- [x] **Step 4: Run controller, router, and catalog tests**

Run: `go test ./controller ./router ./service/modelcatalog -count=1 && (cd web && npm test -- client-model-routes-page.test.tsx && npm run build:check)`

Expected: PASS; empty OpenAI, Anthropic, and Gemini catalogs all return 200 with empty arrays.

- [x] **Step 5: Commit the client-aware model APIs**

```bash
git add controller/model.go controller/client_model_route.go controller/*model*_test.go router/api-router.go web/src/features/client-model-routes web/src/routes.tsx
git commit -m "feat: expose client-aware model routes"
```

### Task 3: Resolve requested models and native wire protocols before billing

**Files:**
- Create: `relay/protocol/wire.go`
- Create: `relay/protocol/probe.go`
- Test: `relay/protocol/wire_test.go`
- Test: `relay/protocol/probe_test.go`
- Modify: `relay/common/relay_info.go`
- Modify: `relay/relay_adaptor.go`
- Modify: `service/pre_consume_token_quota.go`
- Test: `service/pre_consume_token_quota_test.go`

**Interfaces:**
- Consumes: `modelcatalog.ResolvedModel`, capability registry declarations, and observed probe records.
- Produces: `protocol.Resolve(resolved, channel, operation) (Decision, error)` and billing against `ResolvedModel.BillingModel`.

- [x] **Step 1: Add failing resolution and billing-order tests**

```go
func TestResolvePrefersProvenNativeResponses(t *testing.T) {
    decision, err := Resolve(ResolveInput{
        Requested: []modelcatalog.WireProtocol{modelcatalog.WireResponses, modelcatalog.WireOpenAIChat},
        Declared: map[modelcatalog.WireProtocol]bool{modelcatalog.WireResponses: true, modelcatalog.WireOpenAIChat: true},
        Observed: map[modelcatalog.WireProtocol]Observation{modelcatalog.WireResponses: {Healthy: true}},
    })
    require.NoError(t, err)
    require.Equal(t, modelcatalog.WireResponses, decision.Wire)
}

func TestPreConsumeUsesResolvedBillingModel(t *testing.T) {
    info := relaycommon.RelayInfo{OriginModelName: "gpt-5.6-sol", ResolvedModelName: "deepseek-v4-flash", BillingModelName: "deepseek-v4-flash"}
    got := billingModelFor(&info)
    require.Equal(t, "deepseek-v4-flash", got)
}
```

- [x] **Step 2: Run focused tests and verify the new fields and resolver are absent**

Run: `go test ./relay/protocol ./service -run 'Test(ResolvePrefersProvenNativeResponses|PreConsumeUsesResolvedBillingModel)' -count=1`

Expected: FAIL at compile time because the resolver and resolved/billing fields do not exist.

- [x] **Step 3: Implement explicit-first wire resolution and bounded probes**

```go
type Decision struct {
    Wire       modelcatalog.WireProtocol
    Source     string
    ObservedAt time.Time
}

func Resolve(in ResolveInput) (Decision, error) {
    for _, wire := range in.Requested {
        if !in.Declared[wire] {
            continue
        }
        observation, observed := in.Observed[wire]
        if !observed || observation.Healthy {
            source := "declared"
            if observed { source = "probe" }
            return Decision{Wire: wire, Source: source, ObservedAt: observation.ObservedAt}, nil
        }
    }
    return Decision{}, errors.New("no declared healthy wire protocol")
}
```

Probe `/v1/responses`, `/v1/chat/completions`, or `/v1/messages` with a non-streaming one-token request, a five-second timeout, no retries, and no prompt/response body logging. Cache success for 24 hours and failure for 10 minutes; explicit management configuration outranks observed defaults.

- [x] **Step 4: Resolve before pre-consumption and run relay/service tests**

Set `OriginModelName`, `PublicModelName`, `ResolvedModelName`, `BillingModelName`, `ModelRouteId`, and `SelectedWire` on `RelayInfo` before quota calculation. Replace price lookup inputs that use the requested alias with `BillingModelName`, but keep the alias in user-visible request logs.

Run: `go test ./relay/... ./service/... -count=1`

Expected: PASS; aliases no longer cause missing-price failures and native Responses is selected only from a declaration or healthy observation.

- [x] **Step 5: Commit native protocol resolution**

```bash
git add relay/protocol relay/common/relay_info.go relay/relay_adaptor.go service/pre_consume_token_quota.go service/pre_consume_token_quota_test.go
git commit -m "feat: resolve model wire before billing"
```

## Batch 2: Complete Responses Compatibility

### Task 4: Centralize Responses tool normalization across providers

**Files:**
- Create: `relay/compat/responses_tools.go`
- Test: `relay/compat/responses_tools_test.go`
- Modify: `relay/channel/deepseek/adaptor.go:186`
- Modify: `service/relayconvert/internal/oai_responses/req_helpers.go:99`
- Test: `service/relayconvert/internal/oai_responses/to_oai_chat_req_test.go`

**Interfaces:**
- Produces: `compat.NormalizeResponsesTools(input, tools json.RawMessage) (NormalizedTools, error)`.
- Consumed by: native DeepSeek, Responses-to-chat conversion, and future native Responses adaptors.

- [x] **Step 1: Add a provider-neutral tool matrix test**

```go
func TestNormalizeResponsesTools(t *testing.T) {
    input := json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec","format":{"type":"grammar"}}]}]}]`)
    got, err := NormalizeResponsesTools(input, nil)
    require.NoError(t, err)
    require.JSONEq(t, `[]`, string(got.Input))
    require.Contains(t, string(got.Tools), `"name":"functions.exec"`)
    require.Contains(t, got.CustomWireNames, "functions.exec")
}

func TestNormalizeResponsesToolsPreservesToolSearch(t *testing.T) {
    tools := json.RawMessage(`[{"type":"tool_search","name":"load_tools"}]`)
    got, err := NormalizeResponsesTools(nil, tools)
    require.NoError(t, err)
    require.True(t, got.RequiresToolSearch)
}
```

- [x] **Step 2: Run tests and confirm normalization is currently DeepSeek-local**

Run: `go test ./relay/compat ./relay/channel/deepseek ./service/relayconvert/internal/oai_responses -count=1`

Expected: FAIL because `relay/compat` does not exist and `tool_search` has no representation.

- [x] **Step 3: Implement one strict normalizer**

```go
type NormalizedTools struct {
    Input             json.RawMessage
    Tools             json.RawMessage
    CustomWireNames   map[string]struct{}
    RequiresToolSearch bool
}
```

Accept `tools` and every `input[].type == "additional_tools"` group. Flatten namespace members to `namespace.name`, retain the original namespace in internal metadata, convert custom/freeform tools only when the selected wire lacks native custom tools, preserve grammar format in metadata, deduplicate exact wire names, and return `invalid_request_error` for collisions or malformed groups.

For a wire without native deferred tools, expose `tool_search` as an ordinary function declaration and record its synthetic name in `NormalizedTools`. Convert the upstream function call back to a Responses `tool_search_call`; on the next request, accept only `tool_search_output.tools` whose names were present in the client's corresponding output, merge those loaded declarations into the active tool set, and convert later calls using their exact flattened names. A missing call ID, duplicate loaded name, or loaded tool not tied to a preceding search output returns `invalid_request_error`.

- [x] **Step 4: Replace DeepSeek-local promotion and run the regression matrix**

`promoteResponsesAdditionalTools` becomes a thin call into `compat.NormalizeResponsesTools`; chat conversion consumes the same normalized result. Keep existing DeepSeek SSE rewriting tests unchanged.

Run: `go test ./relay/compat ./relay/channel/deepseek ./service/relayconvert/internal/oai_responses ./relay -count=1`

Expected: PASS for function calls, custom calls, namespaces, `additional_tools`, deferred `tool_search` call/output, dynamically loaded tools, stream/non-stream, malformed input, unauthorized loaded tools, and duplicate names.

- [x] **Step 5: Commit the compatibility kernel**

```bash
git add relay/compat relay/channel/deepseek service/relayconvert/internal/oai_responses
git commit -m "refactor: centralize responses tool normalization"
```

### Task 5: Add bounded previous-response and reasoning continuity

**Files:**
- Create: `service/responsesstate/store.go`
- Create: `service/responsesstate/memory.go`
- Create: `service/responsesstate/redis.go`
- Test: `service/responsesstate/store_test.go`
- Modify: `relay/responses_handler.go`
- Test: `relay/responses_handler_test.go`
- Modify: `pkg/readiness/readiness.go`

**Interfaces:**
- Produces: `responsesstate.Store` exactly as declared above and `Expand(ctx, request) (json.RawMessage, error)`.
- Consumes: tenant/user ID, token ID, response ID, normalized input/output items, and the selected store.

- [x] **Step 1: Add tenant-isolation, expiry, and replay tests**

```go
func TestStoreScopesResponseByTenantAndToken(t *testing.T) {
    store := NewMemory(1*time.Hour, 1<<20)
    record := Record{TenantID: 1, TokenID: 2, ResponseID: "resp_1", Input: json.RawMessage(`[]`), Output: json.RawMessage(`[]`)}
    require.NoError(t, store.Save(context.Background(), record))
    _, err := store.Load(context.Background(), 1, 3, "resp_1")
    require.ErrorIs(t, err, ErrNotFound)
}

func TestExpandPreviousResponsePrependsStoredTurn(t *testing.T) {
    store := seededStore(t, `[{"type":"message","role":"user","content":"first"}]`, `[{"type":"message","role":"assistant","content":"answer"}]`)
    expanded, err := Expand(context.Background(), store, 1, 2, json.RawMessage(`{"previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"second"}]}`))
    require.NoError(t, err)
    require.Contains(t, string(expanded), `"content":"first"`)
    require.Contains(t, string(expanded), `"content":"second"`)
}
```

- [x] **Step 2: Run focused tests and verify chat conversion currently rejects stateful fields**

Run: `go test ./service/responsesstate ./relay -run 'Test(StoreScopesResponseByTenantAndToken|ExpandPreviousResponsePrependsStoredTurn)' -count=1`

Expected: FAIL because no state store or replay expansion exists.

- [x] **Step 3: Implement bounded memory and Redis stores**

Use a one-hour default TTL, a 2 MiB maximum serialized record, a 32 MiB single-process memory ceiling, Redis keys `responses-state:<tenant>:<token>:<sha256(response-id)>`, and AES-GCM encryption with `GATEWAY_AES_KEY`. Reject cycles, more than 32 replay hops, expired records, cross-model replay, and cross-tenant/token access.

- [x] **Step 4: Integrate replay, completed-response persistence, compaction, and reasoning ownership**

Expand `previous_response_id` only when the selected upstream wire cannot handle it natively. Save normalized input plus output after a completed response; associate reasoning items with the assistant/tool-call turn that produced them; never write hidden reasoning or state payloads to logs. Make readiness fail in multi-instance production when stateful compatibility is enabled without Redis.

Run: `go test ./service/responsesstate ./relay ./pkg/readiness -count=1 && go test ./service/relayconvert/internal/oai_responses -count=1`

Expected: PASS for replay, expiry, cycles, size limits, tenant isolation, compaction items, function/custom tool outputs, and reasoning replay.

- [x] **Step 5: Commit stateful Responses continuity**

```bash
git add service/responsesstate relay/responses_handler.go relay/responses_handler_test.go pkg/readiness
git commit -m "feat: add bounded responses continuity"
```

## Batch 3: Safe Client Integration, Explainability, and Certification

### Task 6: Make Codex and Claude configuration convergent and reversible

**Files:**
- Create: `cli/src/client-state.ts`
- Test: `cli/src/client-state.test.ts`
- Modify: `cli/src/codex.ts:30`
- Modify: `cli/src/claude.ts:4`
- Modify: `cli/src/init.ts`
- Test: `cli/src/config.test.ts`

**Interfaces:**
- Produces: `configureClient`, `restoreClient`, and `ClientReceipt` exactly as declared above.
- Consumes: gateway URL, API token, selected public model, home directory, and shell profile.

- [x] **Step 1: Add ownership, idempotence, and restore tests**

```ts
test('configure then restore reproduces the original bytes', async () => {
  const home = makeHome()
  const original = 'model = "native"\n'
  writeFileSync(join(home, '.codex/config.toml'), original, { mode: 0o600 })
  const receipt = await configureCodex({ home, serverUrl: 'https://gateway.test', apiToken: 'secret', model: 'deepseek/deepseek-v4-flash' })
  await restoreClient(receipt)
  assert.equal(readFileSync(join(home, '.codex/config.toml'), 'utf8'), original)
})

test('configure is idempotent and never duplicates managed env keys', async () => {
  const home = makeHome()
  await configureClaude({ home, serverUrl: 'https://gateway.test', apiToken: 'secret' })
  await configureClaude({ home, serverUrl: 'https://gateway.test', apiToken: 'secret' })
  const profile = readFileSync(join(home, '.zshrc'), 'utf8')
  assert.equal(profile.match(/CODEX-GATEWAY:BEGIN/g)?.length, 1)
})
```

- [x] **Step 2: Run CLI tests and confirm the current one-shot writers fail convergence**

Run: `(cd cli && npm test)`

Expected: FAIL because receipts, ownership refusal, managed blocks, exact restore, and model selection do not exist.

- [x] **Step 3: Implement atomic client state transitions**

Write to a `0600` sibling temporary file, `fsync`, rename atomically, then persist a `0600` receipt under `~/.codex-gateway/receipts/`. Backups are content-addressed by SHA-256. Refuse symlinks and files whose UID differs from `process.getuid()`. Manage shell variables only inside:

```text
# CODEX-GATEWAY:BEGIN
export ANTHROPIC_BASE_URL='https://gateway.example'
export ANTHROPIC_API_KEY='...'
# CODEX-GATEWAY:END
```

Codex configuration uses the selected public model and `wire_api = "responses"`; Claude aliases are fetched from the authenticated model catalog and encoded to avoid `/` and `--` ambiguity. Restore verifies the post-configuration hash before replacing any file, otherwise it stops with a conflict message.

- [x] **Step 4: Run CLI tests and build**

Run: `(cd cli && npm test && npm run build)`

Expected: PASS for fresh configuration, repeat configuration, existing config, wrong owner, symlink refusal, interrupted temp write, changed-after-config conflict, and exact restore.

- [x] **Step 5: Commit safe client integration**

```bash
git add cli/src/client-state.ts cli/src/client-state.test.ts cli/src/codex.ts cli/src/claude.ts cli/src/init.ts cli/src/config.test.ts
git commit -m "feat: make client configuration reversible"
```

### Task 7: Add route-decision traces, virtual failover routes, and doctor diagnostics

**Files:**
- Create: `service/modelcatalog/route_policy.go`
- Test: `service/modelcatalog/route_policy_test.go`
- Modify: `relay/common/relay_info.go`
- Modify: `service/log_info_generate.go:208`
- Test: `service/log_info_generate_test.go`
- Create: `controller/route_decision.go`
- Test: `controller/route_decision_test.go`
- Modify: `router/api-router.go`
- Create: `cli/src/doctor.ts`
- Test: `cli/src/doctor.test.ts`

**Interfaces:**
- Produces: `RouteDecisionTrace`, `Attempt`, `/api/log/:request_id/route-decision`, and `codex-gateway doctor`.
- Consumes: public route targets, existing channel affinity/cooldown state, key-pool state, upstream status, and model/wire resolution.

- [x] **Step 1: Add deterministic fallback and redaction tests**

```go
func TestPolicyFallsBackOnlyOnRetryableFailure(t *testing.T) {
    policy := NewPolicy([]Target{{ChannelID: 1, Priority: 0, Weight: 1}, {ChannelID: 2, Priority: 1, Weight: 1}})
    require.Equal(t, 2, policy.NextAfter(1, AttemptResult{Status: 429}).ChannelID)
    require.Nil(t, policy.NextAfter(1, AttemptResult{Status: 400}))
}

func TestRouteDecisionContainsNoSecrets(t *testing.T) {
    trace := RouteDecisionTrace{RequestedModel: "alias", PublicModel: "deepseek/model", Attempts: []Attempt{{ChannelID: 7, Status: 429, Reason: "rate_limited"}}}
    encoded, err := json.Marshal(trace)
    require.NoError(t, err)
    require.NotContains(t, string(encoded), "sk-")
    require.NotContains(t, string(encoded), "Authorization")
}
```

- [x] **Step 2: Run policy and log tests**

Run: `go test ./service/modelcatalog ./service ./controller -run 'Test(PolicyFallsBackOnlyOnRetryableFailure|RouteDecisionContainsNoSecrets)' -count=1`

Expected: FAIL because route policies and route-decision traces do not exist.

- [x] **Step 3: Implement bounded attempts using existing cooldown and affinity state**

```go
type RouteDecisionTrace struct {
    RequestedModel string    `json:"requested_model"`
    PublicModel    string    `json:"public_model"`
    ResolvedModel  string    `json:"resolved_model"`
    SelectedWire   string    `json:"selected_wire"`
    RouteID        int       `json:"route_id"`
    Attempts       []Attempt `json:"attempts"`
}

type Attempt struct {
    ChannelID int    `json:"channel_id"`
    Wire      string `json:"wire"`
    Status    int    `json:"status"`
    Reason    string `json:"reason"`
    DurationMs int64 `json:"duration_ms"`
}
```

Retry at most two alternate targets and only on network failure, 408, 409 conflict explicitly marked retryable, 429, or 5xx. Do not retry client cancellation, validation errors, authentication errors, non-idempotent operations, or after a streamed response emits bytes. Keep affinity when healthy; clear it only under existing affinity failure rules. Persist only the bounded trace fields above.

- [x] **Step 4: Implement doctor probes and run all focused tests**

`doctor` checks configuration ownership/permissions, `/healthz`, authenticated `/v1/models`, chosen model visibility, a minimal native Responses request, one function/custom tool round trip, Anthropic Messages, streaming termination, and restore receipt integrity. It prints PASS/FAIL per check without printing tokens, prompts, or response content and exits nonzero on a critical failure.

Run: `go test ./service/modelcatalog ./service ./controller -count=1 && (cd cli && npm test && npm run build)`

Expected: PASS, including no-retry-after-stream-start and trace redaction.

- [x] **Step 5: Commit routing explainability and diagnostics**

```bash
git add service/modelcatalog/route_policy.go service/modelcatalog/route_policy_test.go relay/common/relay_info.go service/log_info_generate.go service/log_info_generate_test.go controller/route_decision.go controller/route_decision_test.go router/api-router.go cli/src/doctor.ts cli/src/doctor.test.ts
git commit -m "feat: explain client model routing"
```

### Task 8: Close security gaps and certify real Codex/Claude behavior

**Files:**
- Modify: `middleware/cors.go:9`
- Test: `middleware/cors_test.go`
- Create: `scripts/client-e2e.sh`
- Create: `docs/operations/client-compatibility.md`
- Modify: `scripts/release-preflight.sh`

**Interfaces:**
- Consumes: `TRUSTED_ORIGINS`, staged gateway URL, a scoped test token, configured provider routes, Codex CLI, and Claude Code.
- Produces: strict CORS behavior, a repeatable acceptance matrix, and an operator runbook.

- [x] **Step 1: Add strict CORS tests**

```go
func TestCORSAllowsConfiguredOriginOnly(t *testing.T) {
    t.Setenv("TRUSTED_ORIGINS", "https://admin.example.com")
    allowed := runCORSRequest(t, "https://admin.example.com")
    require.Equal(t, "https://admin.example.com", allowed.Header().Get("Access-Control-Allow-Origin"))
    denied := runCORSRequest(t, "https://evil.example")
    require.Empty(t, denied.Header().Get("Access-Control-Allow-Origin"))
}

func TestProductionRejectsWildcardCredentialedCORS(t *testing.T) {
    t.Setenv("RUN_MODE", "production")
    t.Setenv("TRUSTED_ORIGINS", "*")
    require.Error(t, ValidateCORSConfig())
}
```

- [x] **Step 2: Run middleware tests and demonstrate the current permissive behavior**

Run: `go test ./middleware -run 'Test(CORSAllowsConfiguredOriginOnly|ProductionRejectsWildcardCredentialedCORS)' -count=1`

Expected: FAIL because the current middleware sets `AllowAllOrigins = true`, `AllowCredentials = true`, and `AllowHeaders = []string{"*"}`.

- [x] **Step 3: Implement exact trusted-origin parsing**

```go
func ValidateCORSConfig() error {
    origins := trustedOrigins(os.Getenv("TRUSTED_ORIGINS"))
    if strings.EqualFold(os.Getenv("RUN_MODE"), "production") && len(origins) == 0 {
        return errors.New("TRUSTED_ORIGINS must contain at least one https origin in production")
    }
    for _, origin := range origins {
        u, err := url.Parse(origin)
        if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
            return fmt.Errorf("invalid trusted origin %q", origin)
        }
    }
    return nil
}
```

Use exact origins, credentials only for exact matches, methods `GET,POST,PUT,PATCH,DELETE,OPTIONS`, and headers `Authorization,Content-Type,Anthropic-Version,Anthropic-Beta,OpenAI-Beta,X-Request-Id`. Local development may allow exact `http://127.0.0.1:<port>` and `http://localhost:<port>` entries supplied through the same variable.

- [ ] **Step 4: Add and run the real client acceptance matrix**

`scripts/client-e2e.sh` must execute these exact gates against the isolated deployment:

```text
Codex: model list -> text -> streaming -> function tool -> custom/freeform tool -> additional_tools -> previous_response_id -> compact
Claude Code: model alias -> text -> streaming -> tool use/result -> long conversation -> cancellation
Providers: DeepSeek native Responses; Kimi declared protocol; Zhipu declared protocol; Alibaba declared protocol
Routing: alias resolution -> billing identity -> one retryable failover -> no retry after stream bytes
Security: relay token rejected on management route; management token rejected as provider key; untrusted Origin rejected
```

Run: `bash scripts/verify-backend.sh && bash scripts/verify-cli.sh && bash scripts/client-e2e.sh`

Expected: all local checks pass; the live matrix exits zero with one evidence JSON file per client/provider/wire combination and no secret-bearing output.

- [x] **Step 5: Document operations and commit certification gates**

Document public model creation, alias collision behavior, wire preference, probes, state-store sizing, client configure/restore, doctor output, route-decision inspection, and rollback switches. Add release preflight checks for `TRUSTED_ORIGINS`, Redis when stateful compatibility is enabled, and at least one healthy visible client model.

```bash
git add middleware/cors.go middleware/cors_test.go scripts/client-e2e.sh scripts/release-preflight.sh docs/operations/client-compatibility.md
git commit -m "test: certify codex and claude compatibility"
```

## Completion Gate

The optimization is complete only when all of the following are true:

- `go test ./...`, `go vet ./...`, `go test -race ./...`, and `go build -trimpath ./...` exit zero.
- `(cd cli && npm test && npm run build)` exits zero.
- `/v1/models` exposes configured public IDs and client aliases without empty-list panics.
- Price lookup, pre-consumption, settlement, and logs use the resolved billing model while preserving the requested alias for audit.
- Native DeepSeek Responses passes text, streaming, tools, `additional_tools`, state continuity, and compact tests.
- Codex CLI and Claude Code both pass real tool-loop acceptance without CC Switch, OpenCodex, or another local proxy.
- Client configure/restore reproduces original bytes and refuses ownership/symlink conflicts.
- Route-decision evidence explains alias, upstream model, selected wire, channel attempts, and fallback reason without secrets.
- Production startup rejects wildcard credentialed CORS and missing trusted origins.
- The isolated deployment passes the client/provider/wire matrix before any production traffic change.

## 2026-08-11 Remaining Blocker Note

- Task 8, Step 4 (real client acceptance matrix) is **blocked by external prerequisites**: live provider credentials for DeepSeek/Kimi/Zhipu/Alibaba, a scoped relay token, and real Codex CLI / Claude Code installations. These must not be simulated or fabricated. The rest of the architecture-optimization tasks are complete and all local gates pass.

## 2026-08-13 Completion Audit Note

Audited the plan against the live codebase. Two steps that were checked as
complete had not actually been wired into the runtime; both are now implemented
and covered by regression tests:

- **Task 5 (bounded Responses continuity)**: the `responsesstate` store and
  `Expand` helper existed with tests but were never called from the relay path.
  Added `responsesstate.Default()` (Redis-backed when available, bounded memory
  otherwise), expanded `previous_response_id` chains in `ResponsesHelper` when
  the selected wire is chat conversion, and persisted completed turns from both
  non-streaming and streaming OpenAI Responses handlers. Added
  `relay/responses_handler_test.go` and `relay/channel/openai/responses_state_test.go`.
- **Install directory serving**: gin's `RedirectTrailingSlash`, triggered by the
  `/:mode/mj/...` wildcard relay routes, 301-redirected `/install/` to `/install`
  before the static middleware could serve the onboarding index. Registered
  `/install` and `/install/*filepath` explicitly in `SetWebRouter` and
  normalized trailing slashes in `embedFileSystem.Open`. Added
  `router/install_route_test.go`.

All local gates pass: `verify-backend.sh`, `verify-web.sh`, `verify-cli.sh`,
`onboarding_e2e.sh`, `build-onboarding-artifacts.sh --check`, and Playwright
desktop/mobile e2e for install, docs, and client-model-routes.
