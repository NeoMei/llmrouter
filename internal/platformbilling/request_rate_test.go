package platformbilling

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRequestTariffReservesAndSettlesOnce(t *testing.T) {
	a, db, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	tariff := map[string]any{"version": "metered_v2", "operation": "chat", "policy": "sum", "terms": []any{map[string]any{"key": "requests", "unit": "request", "price": Rational{"73", "1"}}}}
	hub.rates = []map[string]any{{"id": "cash-rate", "model": "test", "asset": "CNY/6", "status": "published", "semantics": "metered_v2", "metered": tariff}}
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`)
	s, err := a.Admit(context.Background(), "uh_model_secret", "task", "flat-call", body)
	require.NoError(t, err)
	require.Equal(t, "73", s.call.Amount)
	require.NoError(t, s.Dispatch(context.Background(), 1))
	s.ObserveRawEnvelope([]byte(`{"choices":[{"index":0,"message":{"content":"ok"},"finish_reason":"stop"}]}`), false)
	require.NoError(t, s.Finish(context.Background()))
	var row Call
	require.NoError(t, db.First(&row, "call_id = ?", "flat-call").Error)
	require.Equal(t, "SETTLED", row.Status)
	var intent map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(row.Intent), &intent))
	require.Contains(t, string(intent["usage"]), `"requests"`)
	require.NotContains(t, string(intent["usage"]), `"input"`)
	_, err = a.Admit(context.Background(), "uh_model_secret", "task", "flat-call", body)
	require.ErrorIs(t, err, ErrDuplicate)
	require.Equal(t, 1, hub.settleCount)
}
func TestRequestTariffRejectsMixedUnitsAndIncompleteResponse(t *testing.T) {
	a, _, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	raw := `{"id":"cash-rate","model":"test","asset":"CNY/6","status":"published","semantics":"metered_v2","metered":{"version":"metered_v2","operation":"chat","policy":"sum","terms":[{"key":"requests","unit":"request","price":{"numerator":"73","denominator":"1"}}]}}`
	var rate Rate
	require.NoError(t, json.Unmarshal([]byte(raw), &rate))
	got, err := reserveBound(rate, 1000, 100)
	require.NoError(t, err)
	require.Equal(t, "73", got)
	rate.Input = Rational{"1", "1"}
	_, err = reserveBound(rate, 1000, 100)
	require.Error(t, err)
	var flat map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &flat))
	hub.rates = []map[string]any{flat}
	s, err := a.Admit(context.Background(), "uh_model_secret", "task", "partial-flat", []byte(`{"model":"test","messages":[],"max_tokens":10}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(context.Background(), 1))
	s.ObserveRawEnvelope([]byte(`{"choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`), true)
	require.NoError(t, s.Finish(context.Background()))
	require.Zero(t, hub.settleCount)
}
