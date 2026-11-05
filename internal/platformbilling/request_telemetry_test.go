package platformbilling

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestTechnicalUsageFrozenSnapshotAndHistory(t *testing.T) {
	a, db, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	tariff := map[string]any{"version": "metered_v2", "operation": "chat", "policy": "sum", "terms": []any{map[string]any{"key": "requests", "unit": "request", "price": Rational{"73", "1"}}}}
	hub.rates = []map[string]any{{"id": "cash-rate", "model": "test", "asset": "CNY/6", "status": "published", "semantics": "metered_v2", "metered": tariff}}
	selected, err := common.Marshal(hub.rates[0])
	require.NoError(t, err)
	var expected Rate
	require.NoError(t, common.Unmarshal(selected, &expected))
	ctx := context.Background()
	body := []byte(`{"model":"test","messages":[],"max_tokens":10}`)
	s, err := a.Admit(ctx, "uh_model_secret", "task", "request-technical", body)
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(ctx, 1))
	s.ObserveRawEnvelope([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`), false)
	assert.Nil(t, s.ConfirmedTokenUsage())
	require.NoError(t, s.Finish(ctx))
	var row Call
	require.NoError(t, db.First(&row, "call_id = ?", s.call.CallID).Error)
	var frozen Rate
	assert.NoError(t, common.UnmarshalJsonStr(row.RateSnapshot, &frozen))
	assert.Equal(t, expected, frozen)
	_, err = a.Admit(ctx, "uh_model_secret", "task", s.call.CallID, body)
	require.ErrorIs(t, err, ErrDuplicate)
	assert.Equal(t, 1, hub.settleCount)
	// Mutating current rates cannot reinterpret this settled call.
	hub.rates[0]["metered"] = map[string]any{"invalid": "current"}
	var intent struct {
		Usage Usage `json:"usage"`
	}
	require.NoError(t, common.UnmarshalJsonStr(row.Intent, &intent))
	require.NotNil(t, intent.Usage.Metered)
	assert.EqualError(t, metering.ValidateUsage(*expected.Metered, *intent.Usage.Metered), "operation/unit mismatch")
	snapshot, err := common.Marshal(expected)
	require.NoError(t, err)
	originalIntent := row.Intent
	for _, hidden := range []Call{
		{CallID: "hidden-actor", ActorID: "other", FundingProductID: "llmrouter", InstanceID: "default"},
		{CallID: "hidden-instance", ActorID: "actor", FundingProductID: "llmrouter", InstanceID: "private"},
		{CallID: "hidden-product", ActorID: "actor", FundingProductID: "superwagie", InstanceID: "default"},
	} {
		require.NoError(t, db.Create(&hidden).Error)
	}
	cases := []struct {
		name, snapshot, state string
		rawIntent             string
		mutate                func(*metering.Usage)
		conflict              bool
	}{
		{name: "frozen request", snapshot: string(snapshot), state: "confirmed"},
		{name: "historical absent", state: "unverifiable"},
		{name: "corrupt snapshot", snapshot: "{", state: "invalid"},
		{name: "corrupt intent", snapshot: string(snapshot), state: "invalid", rawIntent: "{"},
		{name: "wrong quantity", snapshot: string(snapshot), state: "invalid", mutate: func(u *metering.Usage) { u.Measurements[0].Quantity.Value = "2" }},
		{name: "scaled quantity", snapshot: string(snapshot), state: "invalid", mutate: func(u *metering.Usage) { u.Measurements[0].Quantity.Scale = 1 }},
		{name: "wrong source", snapshot: string(snapshot), state: "invalid", mutate: func(u *metering.Usage) { u.Source = "estimated" }},
		{name: "wrong operation", snapshot: string(snapshot), state: "invalid", mutate: func(u *metering.Usage) { u.Operation = "responses" }},
		{name: "wrong version", snapshot: string(snapshot), state: "invalid", mutate: func(u *metering.Usage) { u.Version = "v3" }},
		{name: "provider dimension", snapshot: string(snapshot), state: "invalid", mutate: func(u *metering.Usage) { u.Measurements[0].ProviderUnit = "extra" }},
		{name: "extra measurement", snapshot: string(snapshot), state: "invalid", mutate: func(u *metering.Usage) { u.Measurements = append(u.Measurements, u.Measurements[0]) }},
		{name: "bad absent evidence", state: "invalid", mutate: func(u *metering.Usage) { u.Measurements[0].Quantity.Value = "-1" }},
		{name: "conflict wins", state: "conflict", conflict: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, common.UnmarshalJsonStr(originalIntent, &intent))
			if tc.mutate != nil {
				tc.mutate(intent.Usage.Metered)
			}
			raw, err := common.Marshal(intent)
			require.NoError(t, err)
			if tc.rawIntent != "" {
				raw = []byte(tc.rawIntent)
			}
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Updates(map[string]any{"rate_snapshot": tc.snapshot, "intent": string(raw), "evidence_conflict": tc.conflict}).Error)
			rows, err := ReadTechnicalUsage(ctx, db, TechnicalScope{ActorID: "actor", InstanceID: "default"}, time.Now().Unix()-60, time.Now().Unix()+60)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, tc.state, rows[0].UsageState)
			assert.Zero(t, rows[0].PromptTokens)
			assert.Zero(t, rows[0].CompletionTokens)
			if tc.state == "confirmed" {
				require.Len(t, rows[0].Measurements, 1)
				assert.Equal(t, "1", rows[0].Measurements[0].Quantity.Value)
			} else {
				assert.Empty(t, rows[0].Measurements)
			}
		})
	}
}

func TestTechnicalRequestTariffRejectsExpandedContract(t *testing.T) {
	fail := false
	_, db := fixture(t, &fail)
	usage := metering.Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1"}}}}
	rawIntent, err := common.Marshal(struct {
		Usage Usage `json:"usage"`
	}{Usage{Metered: &usage}})
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		mutate func(*Rate)
	}{
		{"additional term", func(r *Rate) { r.Metered.Terms = append(r.Metered.Terms, r.Metered.Terms[0]) }},
		{"provider tariff", func(r *Rate) { r.Metered.Terms[0].ProviderUnit = "extra" }},
		{"minimum", func(r *Rate) { r.Metered.Terms[0].Minimum = &metering.Quantity{Value: "1"} }},
		{"step", func(r *Rate) { r.Metered.Terms[0].Step = &metering.Quantity{Value: "1"} }},
		{"legacy token price", func(r *Rate) { r.Input = Rational{"1", "1"} }},
		{"wrong operation", func(r *Rate) { r.Metered.Operation = "responses" }},
		{"wrong version", func(r *Rate) { r.Metered.Version = "v3" }},
		{"wrong policy", func(r *Rate) { r.Metered.Policy = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rate := Rate{Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "73", Denominator: "1"}}}}}
			tc.mutate(&rate)
			rawRate, err := common.Marshal(rate)
			require.NoError(t, err)
			row := Call{CallID: tc.name, ActorID: "actor", FundingProductID: "llmrouter", InstanceID: "default", Status: "SETTLED", RateSnapshot: string(rawRate), Intent: string(rawIntent)}
			require.NoError(t, db.Create(&row).Error)
			rows, err := ReadTechnicalUsage(context.Background(), db, TechnicalScope{ActorID: "actor", InstanceID: "default"}, time.Now().Unix()-60, time.Now().Unix()+60)
			require.NoError(t, err)
			for _, got := range rows {
				assert.Equal(t, "invalid", got.UsageState)
				assert.Empty(t, got.Measurements)
			}
		})
	}
}
