# Transport, Billing, and Database-Log Correctness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every retryable upstream body independently replayable, charge against the final selected group before sending upstream, and prevent GORM diagnostics from leaking bound values or driver-provided secrets.

**Architecture:** Extend the existing `BodyStorage` abstraction with independent readers and bind its replay metadata to `http.Request`; keep tiered billing inside the existing `BillingSession` lifecycle and refresh it after each successful channel selection; centralize GORM configuration and sanitize only the log representation of driver errors. No relay architecture or database abstraction is replaced.

**Tech Stack:** Go 1.22+, `net/http`, Gin, GORM v2, testify, existing `pkg/billingexpr`, SQLite/MySQL/PostgreSQL/ClickHouse drivers.

## Global Constraints

- Existing public Chat Completions, Responses, Responses Compact, Anthropic Messages, Gemini, and MCP contracts must remain stable unless the approved design explicitly tightens unsafe behavior.
- Existing Gateway channel type and model-routing data must remain compatible.
- Every behavior change must be developed with a failing regression test first.
- Upstream commits are reference material, not cherry-pick requirements; implementations must follow the current repository's package boundaries.
- Unsafe or ambiguous state must fail closed with a stable client-visible error and must not mutate billing, continuity state, or channel status.
- The unrelated untracked file `scripts/prepare-sidecar-env.py` must not be modified or committed.
- All JSON encoding and decoding must use `common.Marshal`, `common.Unmarshal`, or the other wrappers in `common/json.go`.
- Billing arithmetic must use the existing quota helpers and the invariants documented in `pkg/billingexpr/expr.md`.

---

### Task 1: Independent body-storage readers

**Files:**
- Modify: `common/body_storage.go` (`BodyStorage`, `memoryStorage`, `diskStorage`)
- Modify: `common/body_storage_test.go`

**Interfaces:**
- Consumes: existing `BodyStorage` lifecycle, disk-cache threshold controls, and `ErrStorageClosed`.
- Produces: `BodyStorage.NewReader() (io.ReadCloser, error)`, returning a fresh cursor at byte zero on every call.

- [ ] **Step 1: Write failing memory and disk replay tests**

Add table tests that force both storage modes and prove that primary reader, replay reader A, and replay reader B can be interleaved without moving one another's cursors. Also assert that a reader opened before storage close remains readable, while `NewReader` after close returns `ErrStorageClosed`.

```go
func TestBodyStorageNewReaderHasIndependentCursor(t *testing.T) {
	payload := []byte("independent-replay-body")
	for _, tc := range []struct {
		name string
		new  func(*testing.T) BodyStorage
	}{
		{name: "memory", new: func(t *testing.T) BodyStorage { return newMemoryStorage(payload) }},
		{name: "disk", new: func(t *testing.T) BodyStorage {
			storage, err := newDiskStorage(payload, t.TempDir())
			require.NoError(t, err)
			return storage
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := tc.new(t)
			t.Cleanup(func() { _ = storage.Close() })
			a, err := storage.NewReader()
			require.NoError(t, err)
			defer a.Close()
			b, err := storage.NewReader()
			require.NoError(t, err)
			defer b.Close()

			one := make([]byte, 3)
			_, err = io.ReadFull(storage, one)
			require.NoError(t, err)
			assert.Equal(t, "ind", string(one))
			allA, err := io.ReadAll(a)
			require.NoError(t, err)
			allB, err := io.ReadAll(b)
			require.NoError(t, err)
			assert.Equal(t, payload, allA)
			assert.Equal(t, payload, allB)
		})
	}
}

func TestBodyStorageNewReaderRejectsClosedStorage(t *testing.T) {
	storage := newMemoryStorage([]byte("body"))
	replay, err := storage.NewReader()
	require.NoError(t, err)
	require.NoError(t, storage.Close())
	_, err = storage.NewReader()
	require.ErrorIs(t, err, ErrStorageClosed)
	got, err := io.ReadAll(replay)
	require.NoError(t, err)
	assert.Equal(t, "body", string(got))
	require.NoError(t, replay.Close())
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./common -run 'TestBodyStorageNewReader' -count=1`

Expected: build failure because `BodyStorage.NewReader`, `memoryStorage.NewReader`, and `diskStorage.NewReader` do not exist.

- [ ] **Step 3: Add the independent-reader contract**

```go
type BodyStorage interface {
	io.ReadSeeker
	io.Closer
	Bytes() ([]byte, error)
	Size() int64
	IsDisk() bool
	NewReader() (io.ReadCloser, error)
}

func (m *memoryStorage) NewReader() (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.LoadInt32(&m.closed) == 1 {
		return nil, ErrStorageClosed
	}
	return io.NopCloser(bytes.NewReader(m.data)), nil
}

func (d *diskStorage) NewReader() (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if atomic.LoadInt32(&d.closed) == 1 {
		return nil, ErrStorageClosed
	}
	file, err := os.Open(d.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open body cache file for replay: %w", err)
	}
	return file, nil
}
```

- [ ] **Step 4: Verify GREEN and the race-sensitive cursor contract**

Run: `go test -race ./common -run 'TestBodyStorage(NewReader|Disk|Memory)' -count=1`

Expected: PASS with no race report.

- [ ] **Step 5: Commit Task 1**

```bash
git add common/body_storage.go common/body_storage_test.go
git commit -m "fix(relay): add independent readers to body storage"
```

### Task 2: Bind replay metadata to every upstream HTTP request

**Files:**
- Modify: `relay/common/outbound_body.go`
- Modify: `relay/common/outbound_body_test.go`
- Modify: `relay/common/relay_info.go`
- Modify: `relay/channel/api_request.go`
- Create: `relay/channel/api_request_getbody_test.go`
- Modify: `relay/chat_completions_via_responses.go`
- Modify: `relay/responses_compact_via_chat.go`
- Modify: `relay/responses_handler.go`
- Modify: `relay/image_handler.go`
- Modify: `relay/gemini_handler.go`
- Modify: `relay/claude_handler.go`
- Modify: `relay/rerank_handler.go`
- Modify: `relay/compatible_handler.go`
- Modify: `relay/embedding_handler.go`

**Interfaces:**
- Consumes: `BodyStorage.NewReader() (io.ReadCloser, error)` from Task 1.
- Produces: `NewOutboundJSONBody(data []byte) (io.Reader, int64, func() (io.ReadCloser, error), io.Closer, error)` and `ApplyUpstreamBodyMetadata(req *http.Request, info *relaycommon.RelayInfo)`.

- [ ] **Step 1: Write failing outbound-body and request metadata tests**

Cover exact length, two complete independent `GetBody` reads, metadata reset on `RelayInfo.InitChannelMeta`, preservation of a request-native `GetBody`, and removal of the task path's unsafe same-reader closure.

```go
func TestNewOutboundJSONBodyReturnsIndependentGetBody(t *testing.T) {
	payload := []byte(`{"model":"gpt-test","input":"hello"}`)
	body, size, getBody, closer, err := NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()
	assert.Equal(t, int64(len(payload)), size)

	primary, err := io.ReadAll(body)
	require.NoError(t, err)
	a, err := getBody()
	require.NoError(t, err)
	defer a.Close()
	b, err := getBody()
	require.NoError(t, err)
	defer b.Close()
	gotA, err := io.ReadAll(a)
	require.NoError(t, err)
	gotB, err := io.ReadAll(b)
	require.NoError(t, err)
	assert.Equal(t, payload, primary)
	assert.Equal(t, payload, gotA)
	assert.Equal(t, payload, gotB)
}

func TestApplyUpstreamBodyMetadata(t *testing.T) {
	getBody := func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("payload")), nil
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid", io.NopCloser(strings.NewReader("payload")))
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{UpstreamRequestBodySize: 7, UpstreamRequestGetBody: getBody}
	ApplyUpstreamBodyMetadata(req, info)
	assert.Equal(t, int64(7), req.ContentLength)
	require.NotNil(t, req.GetBody)
	replay, err := req.GetBody()
	require.NoError(t, err)
	defer replay.Close()
	got, err := io.ReadAll(replay)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./relay/common ./relay/channel -run '(OutboundJSONBody|UpstreamBodyMetadata|GetBody)' -count=1`

Expected: build failures for the new return value and missing `UpstreamRequestGetBody`/`ApplyUpstreamBodyMetadata`.

- [ ] **Step 3: Implement the replay metadata seam**

```go
func NewOutboundJSONBody(data []byte) (
	body io.Reader,
	size int64,
	getBody func() (io.ReadCloser, error),
	closer io.Closer,
	err error,
) {
	storage, err := common.CreateBodyStorage(data)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	return common.ReaderOnly(storage), storage.Size(), storage.NewReader, storage, nil
}

func ApplyUpstreamBodyMetadata(req *http.Request, info *relaycommon.RelayInfo) {
	if req == nil || info == nil {
		return
	}
	if info.UpstreamRequestBodySize > 0 && req.ContentLength <= 0 {
		req.ContentLength = info.UpstreamRequestBodySize
	}
	if info.UpstreamRequestGetBody != nil && req.GetBody == nil {
		req.GetBody = info.UpstreamRequestGetBody
	}
}
```

Add `UpstreamRequestGetBody func() (io.ReadCloser, error)` beside `UpstreamRequestBodySize`, and reset both at the start of `RelayInfo.InitChannelMeta`. Replace every call site with the five-value form and assign both metadata fields:

```go
body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
if err != nil {
	return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
}
defer closer.Close()
info.UpstreamRequestBodySize = size
info.UpstreamRequestGetBody = getBody
requestBody = body
```

For Claude pass-through storage, bind `storage.NewReader` directly. Call `ApplyUpstreamBodyMetadata` from `DoApiRequest`, `DoFormRequest`, and `DoTaskApiRequest`. Delete the task closure that returns `io.NopCloser(requestBody)`.

- [ ] **Step 4: Add transport-level regression coverage**

Use an `httptest` server/transport harness in `relay/channel/api_request_getbody_test.go` that reads the original body, invokes `req.GetBody` twice, and verifies the complete payload on each replay. Add a multipart case and a cancellation case; cancellation must return `context.Canceled` and must not make `GetBody` return a partial payload.

```go
func TestApplyUpstreamBodyMetadataKeepsNativeGetBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid", bytes.NewReader([]byte("native")))
	require.NoError(t, err)
	native := req.GetBody
	ApplyUpstreamBodyMetadata(req, &relaycommon.RelayInfo{
		UpstreamRequestBodySize: 8,
		UpstreamRequestGetBody: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("fallback")), nil
		},
	})
	require.NotNil(t, req.GetBody)
	reader, err := req.GetBody()
	require.NoError(t, err)
	defer reader.Close()
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "native", string(got))
	require.NotNil(t, native)
}
```

- [ ] **Step 5: Verify GREEN across all relay body producers**

Run: `go test -race ./common ./relay/common ./relay/channel ./relay -run '(BodyStorage|OutboundJSONBody|UpstreamBodyMetadata|GetBody|Replay|Cancellation)' -count=1`

Expected: PASS with no race report.

Run: `rg -n 'NewOutboundJSONBody\(' relay -g '*.go'`

Expected: every production call binds five return values and assigns `UpstreamRequestGetBody`; no task request sets `GetBody` to the original `requestBody`.

- [ ] **Step 6: Commit Task 2**

```bash
git add common/body_storage.go relay/common/outbound_body.go relay/common/outbound_body_test.go relay/common/relay_info.go relay/channel/api_request.go relay/channel/api_request_getbody_test.go relay/chat_completions_via_responses.go relay/responses_compact_via_chat.go relay/responses_handler.go relay/image_handler.go relay/gemini_handler.go relay/claude_handler.go relay/rerank_handler.go relay/compatible_handler.go relay/embedding_handler.go
git commit -m "fix(relay): make upstream bodies replay-safe"
```

### Task 3: Refresh tiered billing after the final channel selection

**Files:**
- Modify: `service/tiered_settle.go`
- Modify: `service/tiered_settle_test.go`
- Inspect: `service/billing_session.go` (`BillingSession.Reserve` must remain the reservation primitive; no change is planned)
- Modify: `controller/relay.go`
- Create: `controller/relay_tiered_retry_test.go`
- Read before editing: `pkg/billingexpr/expr.md`

**Interfaces:**
- Consumes: `BillingSettler.Reserve(int) error`, `BillingSettler.GetPreConsumedQuota() int`, `PreConsumeBilling`, `helper.HandleGroupRatio`, and `billingexpr.QuotaRoundStrict`.
- Produces: `PrepareTieredBillingForSelectedGroup(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError`.

- [ ] **Step 1: Read the billing-expression contract and write transition tests**

Add deterministic tests for free→paid, paid→free, cheaper→more-expensive, equal-ratio no-op, and reservation failure. The spy settler records reserve calls and exposes the resulting pre-consumed amount.

```go
type reserveSpy struct {
	reserved int
	err      error
}

func (s *reserveSpy) Reserve(target int) error {
	if s.err != nil {
		return s.err
	}
	if target > s.reserved {
		s.reserved = target
	}
	return nil
}
func (s *reserveSpy) GetPreConsumedQuota() int { return s.reserved }

func TestPrepareTieredBillingForSelectedGroupReservesHigherFinalGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	spy := &reserveSpy{reserved: 100}
	info := &relaycommon.RelayInfo{
		Billing: spy,
		PriceData: types.PriceData{GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 2}},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode: "tiered_expr", EstimatedQuotaBeforeGroup: 100,
			GroupRatio: 1, EstimatedQuotaAfterGroup: 100,
		},
	}
	err := PrepareTieredBillingForSelectedGroup(c, info)
	require.Nil(t, err)
	assert.Equal(t, 200, spy.reserved)
	assert.Equal(t, 200, info.FinalPreConsumedQuota)
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./service -run 'TestPrepareTieredBillingForSelectedGroup' -count=1`

Expected: build failure because `PrepareTieredBillingForSelectedGroup` does not exist.

- [ ] **Step 3: Implement group refresh and pre-send reservation**

```go
func refreshTieredBillingGroup(relayInfo *relaycommon.RelayInfo) (*billingexpr.BillingSnapshot, error) {
	if relayInfo == nil || relayInfo.TieredBillingSnapshot == nil || relayInfo.TieredBillingSnapshot.BillingMode != "tiered_expr" {
		return nil, nil
	}
	snap := relayInfo.TieredBillingSnapshot
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	if snap.GroupRatio == groupRatio {
		return snap, nil
	}
	estimated, err := billingexpr.QuotaRoundStrict(snap.EstimatedQuotaBeforeGroup * groupRatio)
	if err != nil {
		return nil, err
	}
	snap.GroupRatio = groupRatio
	snap.EstimatedQuotaAfterGroup = estimated
	return snap, nil
}

func PrepareTieredBillingForSelectedGroup(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	snap, err := refreshTieredBillingGroup(relayInfo)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeModelPriceError, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if snap == nil || snap.GroupRatio == 0 {
		return nil
	}
	relayInfo.PriceData.FreeModel = false
	if relayInfo.Billing == nil {
		return PreConsumeBilling(c, snap.EstimatedQuotaAfterGroup, relayInfo)
	}
	if err := relayInfo.Billing.Reserve(snap.EstimatedQuotaAfterGroup); err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	relayInfo.FinalPreConsumedQuota = relayInfo.Billing.GetPreConsumedQuota()
	return nil
}
```

- [ ] **Step 4: Move selection-dependent updates to the correct retry seam**

In `getChannel`, call `helper.HandleGroupRatio` only after channel selection succeeds. In the retry loop, preserve this exact order:

```go
channel, channelErr := getChannel(c, relayInfo, retryParam)
if channelErr != nil {
	newAPIError = channelErr
	break
}
addUsedChannel(c, channel.Id)
if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
	newAPIError = billingErr
	break
}
bodyStorage, bodyErr := common.GetBodyStorage(c)
```

The controller regression test must assert that a failed reservation records the attempted channel, makes zero upstream calls, and returns a skip-retry billing error.

- [ ] **Step 5: Verify GREEN and settlement invariants**

Run: `go test -race ./service ./controller -run '(Tiered|BillingSession|FinalGroup|Reservation)' -count=1`

Expected: PASS; free→paid creates/reserves before upstream, paid→free settles to zero, higher group reserves the delta, and failure prevents send.

- [ ] **Step 6: Commit Task 3**

```bash
git add service/tiered_settle.go service/tiered_settle_test.go service/billing_session.go controller/relay.go controller/relay_tiered_retry_test.go
git commit -m "fix(billing): prepare tiered quota for final group"
```

### Task 4: Parameterize and sanitize GORM diagnostics

**Files:**
- Create: `model/gorm_logger.go`
- Create: `model/gorm_logger_test.go`
- Modify: `model/main.go` (`chooseDB`)

**Interfaces:**
- Consumes: existing DB drivers and `common.DebugEnabled`/`common.GetEnvOrDefault`.
- Produces: `newGormConfig(prepareStmt bool) *gorm.Config`, `newGormLogger(io.Writer) logger.Interface`, and `sanitizeDBError(error) error`.

- [ ] **Step 1: Write failing sanitizer and parameterization tests**

Use representative MySQL, PostgreSQL, ClickHouse, and SQLite driver errors containing `super-secret`. Assert that non-debug output retains the driver class/code but never the secret, while an arbitrary network error remains unchanged. Restore `common.DebugEnabled` and `SQL_SLOW_THRESHOLD_MS` with `t.Cleanup`.

```go
func TestSanitizeDBErrorRedactsDriverMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "mysql", err: &mysql.MySQLError{Number: 1062, Message: "duplicate super-secret"}, want: "mysql error 1062"},
		{name: "postgres", err: &pgconn.PgError{Code: "23505", Detail: "super-secret"}, want: "postgres error SQLSTATE 23505"},
		{name: "clickhouse", err: &proto.Exception{Code: 241, Message: "super-secret"}, want: "clickhouse error 241"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeDBError(tc.err).Error()
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "super-secret")
		})
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./model -run '(SanitizeDBError|GormLogger|SlowThreshold)' -count=1`

Expected: build failure because `sanitizeDBError` and `newGormLogger` do not exist.

- [ ] **Step 3: Add the centralized GORM logger**

Implement the approved seam with constants `defaultSlowThresholdMs = 200` and `maxSlowThresholdMs = 60 * 60 * 1000`. The logger config must set:

```go
logger.Config{
	SlowThreshold:             time.Duration(slowThresholdMs) * time.Millisecond,
	LogLevel:                  logger.Warn,
	IgnoreRecordNotFoundError: true,
	ParameterizedQueries:      !common.DebugEnabled,
	Colorful:                  true,
}
```

The writer sanitizes only `error` arguments when debug is disabled. `sanitizeDBError` maps `*mysql.MySQLError`, `*pgconn.PgError`, `*proto.Exception`, and `*sqlitedriver.Error` to class/code-only errors, and returns every other error unchanged.

- [ ] **Step 4: Route every database open through the centralized config**

Use `newGormConfig(false)` for ClickHouse and `newGormConfig(true)` for PostgreSQL, MySQL, and SQLite. Preserve the current DSN normalization and dialect selection exactly.

- [ ] **Step 5: Verify GREEN**

Run: `go test -race ./model -run '(SanitizeDBError|GormLogger|SlowThreshold|ChooseDB)' -count=1`

Expected: PASS; captured logs contain SQL operation context and driver class/code, but no bound secret or driver message secret.

- [ ] **Step 6: Commit Task 4**

```bash
git add model/gorm_logger.go model/gorm_logger_test.go model/main.go
git commit -m "fix(model): redact parameters from database logs"
```

### Task 5: Subproject verification gate

**Files:**
- No planned file changes; this task is a verification-only gate.

**Interfaces:**
- Consumes: all Task 1–4 outputs.
- Produces: a reviewable, separately revertible subproject with fresh test evidence.

- [ ] **Step 1: Run focused race suites**

Run: `go test -race ./common ./relay/common ./relay/channel ./relay ./service ./controller ./model -count=1`

Expected: PASS with no race report.

- [ ] **Step 2: Run the full backend gate**

Run: `go test ./...`

Expected: all packages PASS.

Run: `go vet ./...`

Expected: exit 0 with no diagnostics.

- [ ] **Step 3: Run formatting and diff hygiene**

Run: `gofmt -w common/body_storage.go common/body_storage_test.go relay/common/outbound_body.go relay/common/outbound_body_test.go relay/common/relay_info.go relay/channel/api_request.go relay/channel/api_request_getbody_test.go relay/chat_completions_via_responses.go relay/responses_compact_via_chat.go relay/responses_handler.go relay/image_handler.go relay/gemini_handler.go relay/claude_handler.go relay/rerank_handler.go relay/compatible_handler.go relay/embedding_handler.go service/tiered_settle.go service/tiered_settle_test.go service/billing_session.go controller/relay.go controller/relay_tiered_retry_test.go model/gorm_logger.go model/gorm_logger_test.go model/main.go`

Run: `git diff --check`

Expected: no output and exit 0.

- [ ] **Step 4: Review scope before the checkpoint commit**

Run: `git status --short && git diff --stat HEAD~4..HEAD && git diff -- scripts/prepare-sidecar-env.py`

Expected: the untracked sidecar helper remains untouched; no UI, provider feature, channel type, or protected project identity changed.

- [ ] **Step 5: Preserve task-level commit boundaries**

If verification exposes a regression, return to the task that introduced it, add a failing regression test, implement the minimum fix, re-run that task's GREEN command and this full gate, then amend that task's commit with `git commit --amend --no-edit`. If the gate is clean, do not create an empty commit.
