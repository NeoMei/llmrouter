package responsesstate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
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
	require.Contains(t, string(expanded), `"content":"first"`)
	require.Contains(t, string(expanded), `"content":"second"`)
	require.NotContains(t, string(expanded), `"previous_response_id"`)
}

func TestExpandRejectsCycles(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	record1 := testRecord("resp_1")
	record1.Input = json.RawMessage(`[{"type":"previous_response_id","id":"resp_2"}]`)
	require.NoError(t, store.Save(context.Background(), record1))
	record2 := testRecord("resp_2")
	record2.Input = json.RawMessage(`[{"type":"previous_response_id","id":"resp_1"}]`)
	require.NoError(t, store.Save(context.Background(), record2))
	_, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_1","input":[]}`))
	require.ErrorIs(t, err, ErrReplayRejected)
}

func TestExpandRejectsTooManyHops(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	for i := 0; i < MaxReplayHops+1; i++ {
		next := fmt.Sprintf("resp_%d", i+1)
		input := json.RawMessage(`[]`)
		if i < MaxReplayHops {
			input = json.RawMessage(fmt.Sprintf(`[{"type":"previous_response_id","id":"%s"}]`, next))
		}
		record := testRecord(fmt.Sprintf("resp_%d", i))
		record.Input = input
		require.NoError(t, store.Save(context.Background(), record))
	}
	_, err := Expand(context.Background(), store, testScope(), json.RawMessage(`{"previous_response_id":"resp_0","input":[]}`))
	require.ErrorIs(t, err, ErrReplayRejected)
}
