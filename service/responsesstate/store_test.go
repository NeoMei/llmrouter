package responsesstate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testScope() Scope {
	return Scope{
		TenantID:     1,
		UserID:       11,
		TokenID:      2,
		RouteID:      3,
		TargetID:     4,
		ChannelID:    5,
		ClientTaskID: "task-a",
		Model:        "model-a",
	}
}

func testRecord(responseID string) Record {
	return Record{
		Version:    CurrentVersion,
		Scope:      testScope(),
		ResponseID: responseID,
		Input:      json.RawMessage(`[]`),
		Output:     json.RawMessage(`[]`),
		ExpiresAt:  time.Now().Add(time.Hour),
	}
}

func TestV2KeyHashesResponseID(t *testing.T) {
	const responseID = "resp_secret_raw_id"
	key := Key(responseID)
	require.True(t, strings.HasPrefix(key, "responses-state:v2:"))
	assert.NotContains(t, key, responseID)
}

func TestStoreScopeMismatchIndependent(t *testing.T) {
	store := NewMemory(time.Hour, 1<<20)
	require.NoError(t, store.Save(context.Background(), testRecord("resp_scope")))

	tests := []struct {
		name   string
		mutate func(*Scope)
	}{
		{name: "tenant", mutate: func(scope *Scope) { scope.TenantID++ }},
		{name: "user", mutate: func(scope *Scope) { scope.UserID++ }},
		{name: "token", mutate: func(scope *Scope) { scope.TokenID++ }},
		{name: "route", mutate: func(scope *Scope) { scope.RouteID++ }},
		{name: "target", mutate: func(scope *Scope) { scope.TargetID++ }},
		{name: "channel", mutate: func(scope *Scope) { scope.ChannelID++ }},
		{name: "model", mutate: func(scope *Scope) { scope.Model = "model-b" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := testScope()
			tt.mutate(&expected)
			_, err := store.Load(context.Background(), expected, "resp_scope")
			require.ErrorIs(t, err, ErrScopeMismatch)
		})
	}
}

func TestStoreTaskIsolation(t *testing.T) {
	store := NewMemory(time.Hour, 1<<20)
	require.NoError(t, store.Save(context.Background(), testRecord("resp_task")))

	expected := testScope()
	expected.ClientTaskID = ""
	record, err := store.Load(context.Background(), expected, "resp_task")
	require.NoError(t, err)
	assert.Equal(t, "task-a", record.Scope.ClientTaskID)

	expected.ClientTaskID = "task-b"
	_, err = store.Load(context.Background(), expected, "resp_task")
	require.ErrorIs(t, err, ErrScopeMismatch)
}

func assertScopedResponseIDCollisionsAreIsolated(t *testing.T, store Store) {
	t.Helper()
	base := testScope()
	scopes := []Scope{base}
	mutations := []func(*Scope){
		func(scope *Scope) { scope.TenantID++ },
		func(scope *Scope) { scope.UserID++ },
		func(scope *Scope) { scope.TokenID++ },
		func(scope *Scope) { scope.RouteID++ },
		func(scope *Scope) { scope.TargetID++ },
		func(scope *Scope) { scope.ChannelID++ },
		func(scope *Scope) { scope.Model = "model-b" },
	}
	for _, mutate := range mutations {
		scope := base
		mutate(&scope)
		scopes = append(scopes, scope)
	}

	for i, scope := range scopes {
		record := testRecord("resp_collision")
		record.Scope = scope
		record.Output = json.RawMessage(fmt.Sprintf(`[{"scope":%d}]`, i))
		require.NoError(t, store.Save(context.Background(), record))
	}
	for i, scope := range scopes {
		record, err := store.Load(context.Background(), scope, "resp_collision")
		require.NoError(t, err)
		assert.Equal(t, json.RawMessage(fmt.Sprintf(`[{"scope":%d}]`, i)), record.Output)
	}

	expectedWithoutTask := base
	expectedWithoutTask.ClientTaskID = ""
	record, err := store.Load(context.Background(), expectedWithoutTask, "resp_collision")
	require.NoError(t, err)
	assert.Equal(t, base.ClientTaskID, record.Scope.ClientTaskID)

	secondTask := base
	secondTask.ClientTaskID = "task-b"
	secondTaskRecord := testRecord("resp_collision")
	secondTaskRecord.Scope = secondTask
	secondTaskRecord.Output = json.RawMessage(`[{"task":"b"}]`)
	require.NoError(t, store.Save(context.Background(), secondTaskRecord))

	loadedSecondTask, err := store.Load(context.Background(), secondTask, "resp_collision")
	require.NoError(t, err)
	assert.Equal(t, secondTaskRecord.Output, loadedSecondTask.Output)
	_, err = store.Load(context.Background(), expectedWithoutTask, "resp_collision")
	require.ErrorIs(t, err, ErrScopeMismatch, "an omitted task ID must fail closed when adoption is ambiguous")
}

func TestMemoryStoreIsolatesScopedResponseIDCollisions(t *testing.T) {
	assertScopedResponseIDCollisionsAreIsolated(t, NewMemory(time.Hour, 1<<20))
}

func TestRedisStoreIsolatesScopedResponseIDCollisions(t *testing.T) {
	server := miniredis.RunT(t)
	previousRDB := common.RDB
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, common.RDB.Close())
		common.RDB = previousRDB
	})

	assertScopedResponseIDCollisionsAreIsolated(t, &RedisStore{})
}

func TestStoreRejectsLegacyUnscopedRecord(t *testing.T) {
	store := NewMemory(time.Hour, 1<<20)
	store.records[legacyKey(1, 2, "resp_old")] = Record{}

	_, err := store.Load(context.Background(), testScope(), "resp_old")
	require.ErrorIs(t, err, ErrLegacyScope)
}

func TestRedisStoreMatchesMemoryScopeSemantics(t *testing.T) {
	server := miniredis.RunT(t)
	previousRDB := common.RDB
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, common.RDB.Close())
		common.RDB = previousRDB
	})

	store := &RedisStore{}
	require.NoError(t, store.Save(context.Background(), testRecord("resp_redis")))
	record, err := store.Load(context.Background(), testScope(), "resp_redis")
	require.NoError(t, err)
	assert.Equal(t, testScope(), record.Scope)

	mismatched := testScope()
	mismatched.ChannelID++
	_, err = store.Load(context.Background(), mismatched, "resp_redis")
	require.ErrorIs(t, err, ErrScopeMismatch)

	require.NoError(t, common.RedisSet(legacyKey(1, 2, "resp_legacy_redis"), `{}`, time.Hour))
	_, err = store.Load(context.Background(), testScope(), "resp_legacy_redis")
	require.ErrorIs(t, err, ErrLegacyScope)
}

func TestValidateRecordRequiresV2FullScope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
		error  error
	}{
		{name: "version", mutate: func(record *Record) { record.Version = 1 }, error: ErrLegacyScope},
		{name: "tenant", mutate: func(record *Record) { record.Scope.TenantID = 0 }, error: ErrScopeMismatch},
		{name: "user", mutate: func(record *Record) { record.Scope.UserID = 0 }, error: ErrScopeMismatch},
		{name: "token", mutate: func(record *Record) { record.Scope.TokenID = 0 }, error: ErrScopeMismatch},
		{name: "route", mutate: func(record *Record) { record.Scope.RouteID = 0 }, error: ErrScopeMismatch},
		{name: "target", mutate: func(record *Record) { record.Scope.TargetID = 0 }, error: ErrScopeMismatch},
		{name: "channel", mutate: func(record *Record) { record.Scope.ChannelID = 0 }, error: ErrScopeMismatch},
		{name: "model", mutate: func(record *Record) { record.Scope.Model = " " }, error: ErrScopeMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := testRecord("resp_invalid")
			tt.mutate(&record)
			require.ErrorIs(t, ValidateRecord(record), tt.error)
		})
	}

	record := testRecord("resp_no_explicit_task")
	record.Scope.ClientTaskID = ""
	require.NoError(t, ValidateRecord(record))
}

func TestMemoryStoreAppliesConfiguredTTL(t *testing.T) {
	store := NewMemory(15*time.Minute, 1<<20)
	record := testRecord("resp_memory_ttl")
	record.ExpiresAt = time.Time{}
	require.NoError(t, store.Save(context.Background(), record))

	loaded, err := store.Load(context.Background(), testScope(), record.ResponseID)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), loaded.ExpiresAt, time.Second)
}

func TestMemoryStoreKeepsByteAccountingAfterRejectedReplacement(t *testing.T) {
	first := testRecord("resp_limit")
	serialized, err := common.Marshal(first)
	require.NoError(t, err)
	store := NewMemory(time.Hour, len(serialized)+1)
	require.NoError(t, store.Save(context.Background(), first))

	replacement := first
	replacement.Output = json.RawMessage(`[` + strings.Repeat(`"x",`, 20) + `"x"]`)
	require.Error(t, store.Save(context.Background(), replacement))

	second := testRecord("resp_other")
	require.Error(t, store.Save(context.Background(), second), "the retained first record must still count against the byte ceiling")
	loaded, err := store.Load(context.Background(), testScope(), first.ResponseID)
	require.NoError(t, err)
	assert.Equal(t, first.Output, loaded.Output)
}

func TestMemoryStoreCopiesRawMessagesOnSaveAndLoad(t *testing.T) {
	store := NewMemory(time.Hour, 1<<20)
	record := testRecord("resp_copy")
	originalInput := append(json.RawMessage(nil), record.Input...)
	originalOutput := append(json.RawMessage(nil), record.Output...)
	require.NoError(t, store.Save(context.Background(), record))

	record.Input[0] = '{'
	record.Output[0] = '{'
	loaded, err := store.Load(context.Background(), testScope(), "resp_copy")
	require.NoError(t, err)
	assert.Equal(t, originalInput, loaded.Input)
	assert.Equal(t, originalOutput, loaded.Output)

	loaded.Input[0] = '{'
	loaded.Output[0] = '{'
	reloaded, err := store.Load(context.Background(), testScope(), "resp_copy")
	require.NoError(t, err)
	assert.Equal(t, originalInput, reloaded.Input)
	assert.Equal(t, originalOutput, reloaded.Output)
}

func TestMemoryStoreReturnsIndependentRawMessagesConcurrently(t *testing.T) {
	store := NewMemory(time.Hour, 1<<20)
	record := testRecord("resp_race_copy")
	record.Input = json.RawMessage(`[1]`)
	require.NoError(t, store.Save(context.Background(), record))

	const readers = 32
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func(marker byte) {
			defer wg.Done()
			loaded, err := store.Load(context.Background(), testScope(), record.ResponseID)
			assert.NoError(t, err)
			if err == nil {
				loaded.Input[1] = marker
			}
		}(byte('a' + i%26))
	}
	wg.Wait()

	loaded, err := store.Load(context.Background(), testScope(), record.ResponseID)
	require.NoError(t, err)
	assert.Equal(t, json.RawMessage(`[1]`), loaded.Input)
}

func TestRedisStoreAppliesDefaultTTL(t *testing.T) {
	server := miniredis.RunT(t)
	previousRDB := common.RDB
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, common.RDB.Close())
		common.RDB = previousRDB
	})

	store := &RedisStore{}
	record := testRecord("resp_redis_ttl")
	record.ExpiresAt = time.Time{}
	require.NoError(t, store.Save(context.Background(), record))

	loaded, err := store.Load(context.Background(), testScope(), record.ResponseID)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(DefaultRecordTTL), loaded.ExpiresAt, time.Second)
}

func TestRedisStoreRejectsExplicitPastExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	previousRDB := common.RDB
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, common.RDB.Close())
		common.RDB = previousRDB
	})

	record := testRecord("resp_redis_expired")
	record.ExpiresAt = time.Now().Add(-time.Second)
	require.Error(t, (&RedisStore{}).Save(context.Background(), record))
}

func seededStore(t *testing.T, input, output string) Store {
	t.Helper()
	store := NewMemory(1*time.Hour, 1<<20)
	record := testRecord("resp_1")
	record.Input = json.RawMessage(input)
	record.Output = json.RawMessage(output)
	require.NoError(t, store.Save(context.Background(), record))
	return store
}

func TestStoreScopesResponseByTenantAndToken(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	record := testRecord("resp_1")
	require.NoError(t, store.Save(context.Background(), record))
	expected := testScope()
	expected.TokenID++
	_, err := store.Load(context.Background(), expected, "resp_1")
	require.ErrorIs(t, err, ErrScopeMismatch)
}

func TestStoreRejectsExpiredRecord(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	record := testRecord("resp_1")
	record.ExpiresAt = time.Now().Add(-time.Second)
	require.Error(t, store.Save(context.Background(), record))
}

func TestExpandPreviousResponsePrependsStoredTurn(t *testing.T) {
	store := seededStore(t, `[{"type":"message","role":"user","content":"first"}]`, `[{"type":"message","role":"assistant","content":"answer"}]`)
	expanded, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"second"}]}`))
	require.NoError(t, err)
	require.Contains(t, string(expanded.Request), `"content":"first"`)
	require.Contains(t, string(expanded.Request), `"content":"second"`)
	require.NotContains(t, string(expanded.Request), `"previous_response_id"`)
	assert.JSONEq(t, `[{"type":"message","role":"user","content":"second"}]`, string(expanded.CurrentInput))
	assert.Equal(t, "resp_1", expanded.ParentResponseID)
	assert.Equal(t, testScope(), expanded.Scope)
}

func TestExpandRejectsCycles(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	record1 := testRecord("resp_1")
	record1.ParentResponseID = "resp_2"
	require.NoError(t, store.Save(context.Background(), record1))
	record2 := testRecord("resp_2")
	record2.ParentResponseID = "resp_1"
	require.NoError(t, store.Save(context.Background(), record2))
	_, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_1","input":[]}`))
	require.ErrorIs(t, err, ErrReplayRejected)
}

func TestExpandRejectsTooManyHops(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	for i := 0; i < MaxReplayHops+1; i++ {
		next := fmt.Sprintf("resp_%d", i+1)
		record := testRecord(fmt.Sprintf("resp_%d", i))
		if i < MaxReplayHops {
			record.ParentResponseID = next
		}
		require.NoError(t, store.Save(context.Background(), record))
	}
	_, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_0","input":[]}`))
	require.ErrorIs(t, err, ErrReplayRejected)
}

type loadCountingStore struct {
	Store
	loads int
}

func (s *loadCountingStore) Load(ctx context.Context, expected Scope, responseID string) (Record, error) {
	s.loads++
	return s.Store.Load(ctx, expected, responseID)
}

func TestExpandUsesParentLinksWithoutResavingMaterializedHistory(t *testing.T) {
	memory := NewMemory(time.Hour, 1<<20)
	root := testRecord("resp_1")
	root.Input = json.RawMessage(`[{"turn":"root-input"}]`)
	root.Output = json.RawMessage(`[{"turn":"root-output"}]`)
	require.NoError(t, memory.Save(context.Background(), root))

	child := testRecord("resp_2")
	child.ParentResponseID = root.ResponseID
	child.Input = json.RawMessage(`[{"turn":"child-input"}]`)
	child.Output = json.RawMessage(`[{"turn":"child-output"}]`)
	require.NoError(t, memory.Save(context.Background(), child))

	store := &loadCountingStore{Store: memory}
	current := json.RawMessage(`[{ "turn": "current-input" }]`)
	expansion, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"model":"model-a","previous_response_id":"resp_2","input":[{ "turn": "current-input" }]}`))
	require.NoError(t, err)
	assert.Equal(t, 2, store.loads, "a retained chain of N turns must perform exactly N loads")
	assert.Equal(t, "resp_2", expansion.ParentResponseID)
	assert.Equal(t, testScope(), expansion.Scope)
	assert.Equal(t, current, expansion.CurrentInput, "current input must remain the original unmaterialized input")

	var payload struct {
		PreviousResponseID string            `json:"previous_response_id"`
		Input              []json.RawMessage `json:"input"`
	}
	require.NoError(t, common.Unmarshal(expansion.Request, &payload))
	assert.Empty(t, payload.PreviousResponseID)
	require.Len(t, payload.Input, 5)
	assert.JSONEq(t, `{"turn":"root-input"}`, string(payload.Input[0]))
	assert.JSONEq(t, `{"turn":"root-output"}`, string(payload.Input[1]))
	assert.JSONEq(t, `{"turn":"child-input"}`, string(payload.Input[2]))
	assert.JSONEq(t, `{"turn":"child-output"}`, string(payload.Input[3]))
	assert.JSONEq(t, `{"turn":"current-input"}`, string(payload.Input[4]))

	require.NoError(t, SaveTurn(context.Background(), memory, Record{
		Version:          CurrentVersion,
		Scope:            expansion.Scope,
		ResponseID:       "resp_3",
		ParentResponseID: expansion.ParentResponseID,
		Input:            expansion.CurrentInput,
		Output:           json.RawMessage(`[{"turn":"current-output"}]`),
	}))
	saved, err := memory.Load(context.Background(), testScope(), "resp_3")
	require.NoError(t, err)
	assert.JSONEq(t, string(expansion.CurrentInput), string(saved.Input))
	assert.NotContains(t, string(saved.Input), "root-input")
	assert.NotContains(t, string(saved.Input), "child-input")
}

func TestExpandIgnoresSyntheticPreviousResponseItems(t *testing.T) {
	memory := NewMemory(time.Hour, 1<<20)
	unused := testRecord("resp_unused")
	unused.Input = json.RawMessage(`[{"turn":"must-not-load"}]`)
	require.NoError(t, memory.Save(context.Background(), unused))

	record := testRecord("resp_leaf")
	record.Input = json.RawMessage(`[{"type":"previous_response_id","id":"resp_unused"}]`)
	require.NoError(t, memory.Save(context.Background(), record))

	store := &loadCountingStore{Store: memory}
	expansion, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_leaf","input":[]}`))
	require.NoError(t, err)
	assert.Equal(t, 1, store.loads)
	assert.NotContains(t, string(expansion.Request), "must-not-load")
	assert.Contains(t, string(expansion.Request), `"type":"previous_response_id"`)
}

func TestExpandAdoptsStoredTaskScope(t *testing.T) {
	store := NewMemory(time.Hour, 1<<20)
	record := testRecord("resp_task_adopt")
	require.NoError(t, store.Save(context.Background(), record))

	expected := testScope()
	expected.ClientTaskID = ""
	expansion, err := Expand(context.Background(), store, expected, json.RawMessage(`{"previous_response_id":"resp_task_adopt","input":[]}`))
	require.NoError(t, err)
	assert.Equal(t, "task-a", expansion.Scope.ClientTaskID)
}

func TestExpandWithoutParentStillPreservesCurrentInput(t *testing.T) {
	request := json.RawMessage(`{"model":"model-a","input":[ {"turn":"current"} ]}`)
	expansion, err := Expand(context.Background(), nil, testScope(), request)
	require.NoError(t, err)
	assert.Equal(t, request, expansion.Request)
	assert.Equal(t, testScope(), expansion.Scope)
	assert.Empty(t, expansion.ParentResponseID)
	assert.Equal(t, json.RawMessage(`[ {"turn":"current"} ]`), expansion.CurrentInput)
}

func TestExpandNormalizesCurrentStringInputButPreservesOriginal(t *testing.T) {
	store := seededStore(t, `[{"type":"message","role":"user","content":"first"}]`, `[{"type":"message","role":"assistant","content":"answer"}]`)
	expansion, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_1","input":"second"}`))
	require.NoError(t, err)
	assert.Equal(t, json.RawMessage(`"second"`), expansion.CurrentInput)

	var payload struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, common.Unmarshal(expansion.Request, &payload))
	require.Len(t, payload.Input, 3)
	assert.JSONEq(t, `{"type":"message","role":"user","content":"second"}`, string(payload.Input[2]))
}

func TestExpandNormalizesStoredStringInput(t *testing.T) {
	store := seededStore(t, `"first"`, `[{"type":"message","role":"assistant","content":"answer"}]`)
	expansion, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"second"}]}`))
	require.NoError(t, err)

	var payload struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, common.Unmarshal(expansion.Request, &payload))
	require.Len(t, payload.Input, 3)
	assert.JSONEq(t, `{"type":"message","role":"user","content":"first"}`, string(payload.Input[0]))
}

func TestExpandPreservesLargeIntegersAcrossRequestAndStoredItems(t *testing.T) {
	store := seededStore(t,
		`[{"type":"message","role":"user","content":[{"type":"input_text","metadata":{"turn":9007199254740993}}]}]`,
		`[{"type":"function_call","name":"lookup","arguments":{"turn":9007199254740993}}]`,
	)
	request := json.RawMessage(`{
		"model":"model-a",
		"previous_response_id":"resp_1",
		"metadata":{"request":9007199254740993},
		"client_metadata":{"task_id":"task-a","request":9007199254740993},
		"instructions":{"request":9007199254740993},
		"input":[{"type":"function_call_output","call_id":"call_1","output":{"request":9007199254740993}}]
	}`)

	expansion, err := Expand(context.Background(), store, testScope(), request)
	require.NoError(t, err)
	require.NotContains(t, string(expansion.Request), `"previous_response_id"`)
	assert.Equal(t, 6, strings.Count(string(expansion.Request), "9007199254740993"),
		"every large integer must survive without float64 rounding")

	var payload map[string]json.RawMessage
	require.NoError(t, common.Unmarshal(expansion.Request, &payload))
	assert.JSONEq(t, `{"request":9007199254740993}`, string(payload["metadata"]))
	assert.JSONEq(t, `{"task_id":"task-a","request":9007199254740993}`, string(payload["client_metadata"]))
	assert.JSONEq(t, `{"request":9007199254740993}`, string(payload["instructions"]))

	var input []json.RawMessage
	require.NoError(t, common.Unmarshal(payload["input"], &input))
	require.Len(t, input, 3)
	assert.Contains(t, string(input[0]), "9007199254740993")
	assert.Contains(t, string(input[1]), "9007199254740993")
	assert.Contains(t, string(input[2]), "9007199254740993")
	assert.Contains(t, string(expansion.CurrentInput), "9007199254740993")
}

func TestExpandWithParentRejectsNilStore(t *testing.T) {
	_, err := Expand(context.Background(), nil, testScope(), json.RawMessage(`{"previous_response_id":"resp_1","input":[]}`))
	require.Error(t, err)
}

func TestSaveTurnReturnsValidationAndStorageErrors(t *testing.T) {
	err := SaveTurn(context.Background(), nil, testRecord("resp_nil"))
	require.Error(t, err)

	store := NewMemory(time.Hour, 1<<20)
	record := testRecord("resp_defaulted")
	record.Scope.ClientTaskID = ""
	record.ExpiresAt = time.Time{}
	require.NoError(t, SaveTurn(context.Background(), store, record))

	expected := testScope()
	expected.ClientTaskID = record.ResponseID
	saved, err := store.Load(context.Background(), expected, record.ResponseID)
	require.NoError(t, err)
	assert.Equal(t, record.ResponseID, saved.Scope.ClientTaskID)
	assert.WithinDuration(t, time.Now().Add(DefaultRecordTTL), saved.ExpiresAt, time.Second)

	invalid := testRecord("")
	require.Error(t, SaveTurn(context.Background(), store, invalid))
}

func TestParseClientTaskID(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
		want     string
		wantErr  bool
	}{
		{name: "omitted"},
		{name: "only task id is read", metadata: json.RawMessage(`{"task_id":" task-1:_ok. ","thread_id":"ignored"}`), want: "task-1:_ok."},
		{name: "unrelated metadata does not create identity", metadata: json.RawMessage(`{"thread_id":"not-a-task"}`)},
		{name: "task id key is case sensitive", metadata: json.RawMessage(`{"TASK_ID":"must-not-be-used"}`)},
		{name: "blank becomes omitted", metadata: json.RawMessage(`{"task_id":"   "}`)},
		{name: "invalid character", metadata: json.RawMessage(`{"task_id":"bad task"}`), wantErr: true},
		{name: "too long in bytes", metadata: json.RawMessage(`{"task_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), wantErr: true},
		{name: "non string", metadata: json.RawMessage(`{"task_id":12}`), wantErr: true},
		{name: "malformed", metadata: json.RawMessage(`{"task_id":`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseClientTaskID(tt.metadata)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEligibleCompletedResponse(t *testing.T) {
	completed := func() *dto.OpenAIResponsesResponse {
		return &dto.OpenAIResponsesResponse{
			ID:     "resp_1",
			Status: json.RawMessage(`"completed"`),
			Output: []dto.ResponsesOutput{{
				Type:    "message",
				Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "ok"}},
			}},
		}
	}

	tests := []struct {
		name   string
		mutate func(*dto.OpenAIResponsesResponse)
		want   bool
	}{
		{name: "eligible", want: true},
		{name: "missing id", mutate: func(r *dto.OpenAIResponsesResponse) { r.ID = "" }},
		{name: "incomplete status", mutate: func(r *dto.OpenAIResponsesResponse) { r.Status = json.RawMessage(`"incomplete"`) }},
		{name: "malformed status", mutate: func(r *dto.OpenAIResponsesResponse) { r.Status = json.RawMessage(`completed`) }},
		{name: "error", mutate: func(r *dto.OpenAIResponsesResponse) { r.Error = map[string]any{"message": "failed"} }},
		{name: "incomplete details", mutate: func(r *dto.OpenAIResponsesResponse) {
			r.IncompleteDetails = &dto.IncompleteDetails{Reason: "max_output_tokens"}
		}},
		{name: "output refusal", mutate: func(r *dto.OpenAIResponsesResponse) { r.Output[0].Type = "refusal" }},
		{name: "content refusal", mutate: func(r *dto.OpenAIResponsesResponse) { r.Output[0].Content[0].Type = "refusal" }},
		{name: "content refusal payload", mutate: func(r *dto.OpenAIResponsesResponse) { r.Output[0].Content[0].Refusal = "not allowed" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := completed()
			if tt.mutate != nil {
				tt.mutate(response)
			}
			assert.Equal(t, tt.want, EligibleCompletedResponse(response))
		})
	}
}
