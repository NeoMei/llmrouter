package responsesstate

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seededStore(t *testing.T, input, output string) Store {
	t.Helper()
	store := NewMemory(1*time.Hour, 1<<20)
	require.NoError(t, store.Save(context.Background(), Record{
		TenantID: 1, TokenID: 2, ResponseID: "resp_1",
		Input: json.RawMessage(input), Output: json.RawMessage(output),
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	return store
}

func TestStoreScopesResponseByTenantAndToken(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	record := Record{TenantID: 1, TokenID: 2, ResponseID: "resp_1", Input: json.RawMessage(`[]`), Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, store.Save(context.Background(), record))
	_, err := store.Load(context.Background(), 1, 3, "resp_1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStoreRejectsExpiredRecord(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	record := Record{TenantID: 1, TokenID: 2, ResponseID: "resp_1", Input: json.RawMessage(`[]`), Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(-time.Second)}
	require.Error(t, store.Save(context.Background(), record))
}

func TestExpandPreviousResponsePrependsStoredTurn(t *testing.T) {
	store := seededStore(t, `[{"type":"message","role":"user","content":"first"}]`, `[{"type":"message","role":"assistant","content":"answer"}]`)
	expanded, err := Expand(context.Background(), store, 1, 2, json.RawMessage(`{"previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"second"}]}`))
	require.NoError(t, err)
	require.Contains(t, string(expanded), `"content":"first"`)
	require.Contains(t, string(expanded), `"content":"second"`)
	require.NotContains(t, string(expanded), `"previous_response_id"`)
}

func TestExpandRejectsCycles(t *testing.T) {
	store := NewMemory(1*time.Hour, 1<<20)
	require.NoError(t, store.Save(context.Background(), Record{
		TenantID: 1, TokenID: 2, ResponseID: "resp_1",
		Input:  json.RawMessage(`[{"type":"previous_response_id","id":"resp_2"}]`),
		Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(time.Hour)}))
	require.NoError(t, store.Save(context.Background(), Record{
		TenantID: 1, TokenID: 2, ResponseID: "resp_2",
		Input:  json.RawMessage(`[{"type":"previous_response_id","id":"resp_1"}]`),
		Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(time.Hour)}))
	_, err := Expand(context.Background(), store, 1, 2, json.RawMessage(`{"previous_response_id":"resp_1","input":[]}`))
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
		require.NoError(t, store.Save(context.Background(), Record{
			TenantID: 1, TokenID: 2, ResponseID: fmt.Sprintf("resp_%d", i),
			Input: input, Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(time.Hour)}))
	}
	_, err := Expand(context.Background(), store, 1, 2, json.RawMessage(`{"previous_response_id":"resp_0","input":[]}`))
	require.ErrorIs(t, err, ErrReplayRejected)
}
