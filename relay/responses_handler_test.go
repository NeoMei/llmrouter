package relay

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/stretchr/testify/require"
)

// TestResponsesStateExpandOnChatWire verifies that a request carrying a
// previous_response_id is expanded into the full conversation when the selected
// wire is chat conversion, and that the stored turn is prepended.
func TestResponsesStateExpandOnChatWire(t *testing.T) {
	store := responsesstate.NewMemory(time.Hour, 1<<20)
	require.NoError(t, store.Save(context.Background(), responsesstate.Record{
		TenantID:   11,
		TokenID:    7,
		ResponseID: "resp_prev",
		Model:      "model-a",
		Input:      json.RawMessage(`[{"type":"message","role":"user","content":"first"}]`),
		Output:     json.RawMessage(`[{"type":"message","role":"assistant","content":"answer"}]`),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	req := json.RawMessage(`{"model":"model-a","previous_response_id":"resp_prev","input":[{"type":"message","role":"user","content":"second"}]}`)
	expanded, err := responsesstate.Expand(context.Background(), store, 11, 7, req)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(expanded, &payload))
	require.Empty(t, payload["previous_response_id"], "previous_response_id must be removed after expansion")

	var input []any
	require.NoError(t, common.Unmarshal(json.RawMessage(mustJSON(t, payload["input"])), &input))
	require.Len(t, input, 3, "expected first turn (user+assistant) plus second user message")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
