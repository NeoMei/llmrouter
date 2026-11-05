package platformbilling

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTextInputWithoutPolicyKeepsLegacyFullContext(t *testing.T) {
	for _, tc := range []struct{ operation, body string }{
		{"chat", ` {"model":"test","messages":[{"role":"user","content":"hello"}],"max_tokens":10,"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]} `},
		{"responses", ` {"model":"test","instructions":"system","input":"hello","max_output_tokens":10,"metadata":{"purpose":"test"}} `},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			body := []byte(tc.body)
			req, err := parseOperation(Config{MaxInputTokens: 65536, MaxOutputTokens: 4096, MaxBodyBytes: 1 << 20}, tc.operation, "application/json", body)
			require.NoError(t, err)
			assert.Equal(t, 65536, req.Quantity)
			assert.Empty(t, req.TextInputSnapshot)
			assert.Empty(t, req.MediaSnapshot)
		})
	}
}

func textPolicyFixture() TextInputPolicy {
	return TextInputPolicy{Version: "full_body_bytes_v1", ChannelID: 7, ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "deepseek-v4-flash", Endpoint: "https://api.deepseek.com/chat/completions", MaxInputTokens: 262144, ContextTokens: 262144, MaxOutputTokens: 4096, ProviderReference: "https://api-docs.deepseek.com/"}
}

func TestTextInputModelPolicyBoundsFull99750ByteRequest(t *testing.T) {
	for _, operation := range []string{"chat", "responses"} {
		t.Run(operation, func(t *testing.T) {
			prefix, suffix := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"`, `"}],"max_tokens":4096}`
			if operation == "responses" {
				prefix, suffix = `{"model":"deepseek-v4-flash","input":"`, `","max_output_tokens":4096}`
			}
			prefix, suffix = " \n"+prefix, suffix+" \t"
			body := []byte(prefix + strings.Repeat("x", 99750-len(prefix)-len(suffix)) + suffix)
			require.Len(t, body, 99750)
			config := Config{MaxInputTokens: 65536, MaxOutputTokens: 4096, MaxBodyBytes: 1 << 20}
			_, err := parseOperation(config, operation, "application/json", body)
			require.ErrorIs(t, err, ErrInvalid, "the default context stays at 65536")
			wire := legacyWireOperation(operation)
			policy := textPolicyFixture()
			if operation == "responses" {
				policy.Endpoint = "https://api.deepseek.com/responses"
			}
			config.TextInputPolicies = map[string]map[string]TextInputPolicy{wire: {"deepseek-v4-flash": policy}}
			req, err := parseOperation(config, operation, "application/json", body)
			require.NoError(t, err)
			assert.Equal(t, 199500, req.Quantity)
			assert.Equal(t, 4096, req.Output)
			var frozen frozenTextReservation
			require.NoError(t, common.UnmarshalUnique([]byte(req.TextInputSnapshot), &frozen))
			assert.Equal(t, frozenTextReservation{Version: "full_body_bytes_v1", Input: 199500, Output: 4096, Policy: &policy}, frozen)
			for _, tc := range []struct {
				name string
				edit func(*TextInputPolicy)
			}{
				{"input_cap", func(p *TextInputPolicy) { p.MaxInputTokens = 199499 }},
				{"context_minus_output", func(p *TextInputPolicy) { p.ContextTokens = 199500 + 4095 }},
				{"output_cap", func(p *TextInputPolicy) { p.MaxOutputTokens = 4095 }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					limited := policy
					tc.edit(&limited)
					config.TextInputPolicies[wire]["deepseek-v4-flash"] = limited
					_, err := parseOperation(config, operation, "application/json", body)
					assert.ErrorIs(t, err, ErrInvalid)
				})
			}
			config.TextInputPolicies[wire] = map[string]TextInputPolicy{"another-model": policy}
			_, err = parseOperation(config, operation, "application/json", body)
			assert.ErrorIs(t, err, ErrInvalid, "a model certificate cannot enlarge another model")
		})
	}
}

func TestTextInputTargetRemainsBoundToFrozenPolicy(t *testing.T) {
	policy := textPolicyFixture()
	config := Config{MaxInputTokens: 65536, MaxOutputTokens: 4096, TextInputPolicies: map[string]map[string]TextInputPolicy{"chat.completions.create": {"deepseek-v4-flash": policy}}}
	quantity, snapshot, err := textReservation(config, "chat.completions.create", "deepseek-v4-flash", 99750, 4096)
	require.NoError(t, err)
	session := &Session{call: Call{TextInputSnapshot: snapshot, ReservationQuantity: int64(quantity), ReservationOutput: 4096}}
	require.NoError(t, session.ValidateTextInputTarget(7, constant.ChannelTypeOpenAI, policy.UpstreamModel, policy.Endpoint))
	for _, tc := range []struct {
		name          string
		channel, kind int
		upstream, url string
	}{
		{"channel", 8, constant.ChannelTypeOpenAI, policy.UpstreamModel, policy.Endpoint},
		{"type", 7, constant.ChannelTypeDeepSeek, policy.UpstreamModel, policy.Endpoint},
		{"upstream", 7, constant.ChannelTypeOpenAI, "other", policy.Endpoint},
		{"host", 7, constant.ChannelTypeOpenAI, policy.UpstreamModel, "https://forwarder.example/chat/completions"},
		{"path", 7, constant.ChannelTypeOpenAI, policy.UpstreamModel, "https://api.deepseek.com/other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, session.ValidateTextInputTarget(tc.channel, tc.kind, tc.upstream, tc.url), ErrInvalid)
		})
	}
	req, err := http.NewRequest(http.MethodPost, policy.Endpoint, nil)
	require.NoError(t, err)
	require.NoError(t, session.ValidateTextInputOutbound(7, constant.ChannelTypeOpenAI, policy.UpstreamModel, req, false))
	assert.ErrorIs(t, session.ValidateTextInputOutbound(7, constant.ChannelTypeOpenAI, policy.UpstreamModel, req, true), ErrInvalid)
	req.Method = http.MethodGet
	assert.ErrorIs(t, session.ValidateTextInputOutbound(7, constant.ChannelTypeOpenAI, policy.UpstreamModel, req, false), ErrInvalid)
	req.Method = http.MethodPost
	req.Host = "forwarder.example"
	assert.ErrorIs(t, session.ValidateTextInputOutbound(7, constant.ChannelTypeOpenAI, policy.UpstreamModel, req, false), ErrInvalid)
}

func TestTextInputManagedDefaultPreservesOriginalBodyBytes(t *testing.T) {
	policy := textPolicyFixture()
	policy.MaxOutputTokens = 1024
	config := Config{MaxInputTokens: 65536, MaxOutputTokens: 4096, TextInputPolicies: map[string]map[string]TextInputPolicy{"chat.completions.create": {"deepseek-v4-flash": policy}}}
	a := &Adapter{config: config}
	body := []byte(" \n" + `{"model":"deepseek-v4-flash", "messages":[{"role":"user","content":"\u4f60\u597d"}]}` + " \t")
	admitted, err := a.ManagedRequestBody("chat", body)
	require.NoError(t, err)
	assert.Equal(t, string(body), strings.Replace(string(admitted), `,"max_tokens":1024`, "", 1))
	req, err := parseOperation(config, "chat", "application/json", admitted)
	require.NoError(t, err)
	assert.Equal(t, 1024, req.Output)
	assert.Equal(t, 2*len(admitted), req.Quantity)
	assert.Greater(t, req.Quantity, 2*len(body), "the server-added output field is included in the bound")
}

func TestTextInputManagedDefaultReplayUsesOriginalNormalization(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical_marshal", true: "frozen_policy"}[configured], func(t *testing.T) {
			fail := false
			a, db := fixture(t, &fail)
			policy := textPolicyFixture()
			policy.MaxOutputTokens = a.config.MaxOutputTokens
			if configured {
				a.config.TextInputPolicies = map[string]map[string]TextInputPolicy{"chat.completions.create": {"test": policy}}
			}
			body := []byte(` {"messages":[{"content":"hello", "role":"user"}], "model":"test"} `)
			original, err := a.ManagedRequestBody("chat", body)
			require.NoError(t, err)
			_, err = a.Admit(context.Background(), "uh_model_token", "task", "call", original)
			require.NoError(t, err)
			if !configured {
				require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "call").Update("reservation_output", 0).Error)
			}
			var before Call
			require.NoError(t, db.First(&before, "call_id = ?", "call").Error)
			policy.MaxOutputTokens = 50
			a.config.TextInputPolicies = map[string]map[string]TextInputPolicy{"chat.completions.create": {"test": policy}}
			replay, err := a.ManagedRequestBody("chat", body, "call")
			require.NoError(t, err)
			assert.Equal(t, original, replay)
			_, err = a.Admit(context.Background(), "uh_model_token", "task", "call", replay)
			assert.ErrorIs(t, err, ErrDuplicate)
			var after Call
			require.NoError(t, db.First(&after, "call_id = ?", "call").Error)
			assert.Equal(t, before, after)
		})
	}
}

func TestTextInputPolicyConfigurationRejectsAmbiguityAndUnsafeBounds(t *testing.T) {
	config := Config{MaxInputTokens: 65536, MaxOutputTokens: 4096}
	policy := textPolicyFixture()
	raw, err := common.Marshal(map[string]map[string]TextInputPolicy{"chat.completions.create": {"deepseek-v4-flash": policy}})
	require.NoError(t, err)
	require.NoError(t, decodeTextInputPolicies(raw, &config))
	for _, tc := range []struct{ name, body string }{
		{"duplicate_key", strings.Replace(string(raw), `"channel_id":7`, `"channel_id":7,"channel_id":7`, 1)},
		{"unknown_key", strings.Replace(string(raw), `"channel_id":7`, `"channel_id":7,"untrusted":true`, 1)},
		{"trailing_value", string(raw) + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.ErrorIs(t, decodeTextInputPolicies([]byte(tc.body), &config), ErrInvalid) })
	}
	for _, tc := range []struct {
		name string
		edit func(*TextInputPolicy)
	}{
		{"input_limit", func(p *TextInputPolicy) { p.MaxInputTokens = 1<<20 + 1 }},
		{"context_limit", func(p *TextInputPolicy) { p.ContextTokens = 1<<20 + 1 }},
		{"output_limit", func(p *TextInputPolicy) { p.MaxOutputTokens = 4097 }},
		{"non_https", func(p *TextInputPolicy) { p.Endpoint = "http://api.deepseek.com/chat/completions" }},
		{"url_credentials", func(p *TextInputPolicy) { p.Endpoint = "https://user:pass@api.deepseek.com/chat/completions" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := policy
			tc.edit(&invalid)
			raw, err := common.Marshal(map[string]map[string]TextInputPolicy{"chat.completions.create": {"deepseek-v4-flash": invalid}})
			require.NoError(t, err)
			assert.ErrorIs(t, decodeTextInputPolicies(raw, &config), ErrInvalid)
		})
	}
}

func TestTextInputDoesNotDiscountMediaOrOpaqueHistory(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(imageFixtureBytes(t))
	for _, tc := range []struct{ operation, body string }{
		{"chat", `{"model":"test","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + encoded + `"}}]}]}`},
		{"responses", `{"model":"test","max_output_tokens":10,"input":"continue","previous_response_id":"resp_stored"}`},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			wire := legacyWireOperation(tc.operation)
			config := Config{MaxInputTokens: 65536, MaxOutputTokens: 4096, MaxBodyBytes: 1 << 20, TextInputPolicies: map[string]map[string]TextInputPolicy{wire: {"test": textPolicyFixture()}}}
			_, err := parseOperation(config, tc.operation, "application/json", []byte(tc.body))
			require.Error(t, err, "text certification must not authorize media or opaque history")
			config.NativeInputPolicies = map[string]map[string]NativeInputPolicy{wire: {"test": {Version: "hard_context_v1", ProviderReference: "https://provider.example/context", ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "test", HardInputTokens: 60000, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 10}}}
			req, err := parseOperation(config, tc.operation, "application/json", []byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, 60000, req.Quantity)
			assert.Empty(t, req.TextInputSnapshot)
			assert.NotEmpty(t, req.MediaSnapshot)
		})
	}
}

func TestTextInputMeteredCacheTotalOverBoundRemainsUnknown(t *testing.T) {
	failSettle := false
	a, db := fixture(t, &failSettle)
	remoteCalls := 0
	a.client.http.Transport = meteredReserveTransport(func(r *http.Request) (*http.Response, error) {
		remoteCalls++
		return nil, ErrPending
	})
	price := metering.Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []metering.Term{
		{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
		{Key: "cache_read", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
		{Key: "output", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
	}}
	rate, err := common.Marshal(Rate{ID: "rate", Model: "test", Semantics: "metered_v2", Metered: &price})
	require.NoError(t, err)
	text, err := common.Marshal(frozenTextReservation{Version: "full_body_bytes_v1", Input: 100, Output: 10})
	require.NoError(t, err)
	row := Call{CallID: "call", Operation: "chat", WireOperation: "chat.completions.create", Status: "UNKNOWN", ReservationQuantity: 100, ReservationOutput: 10, TextInputSnapshot: string(text), RateSnapshot: string(rate)}
	require.NoError(t, db.Create(&row).Error)
	session := &Session{adapter: a, call: row}
	usage := metering.Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{
		{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "60"}},
		{Key: "cache_read", Unit: "token", Quantity: metering.Quantity{Value: "50"}},
		{Key: "output", Unit: "token", Quantity: metering.Quantity{Value: "1"}},
	}}
	require.NoError(t, session.ObserveMetering(context.Background(), usage))
	require.NoError(t, session.Finish(context.Background()))
	require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
	require.NoError(t, common.UnmarshalUnique([]byte(row.Evidence), &usage))
	assert.Equal(t, "incomplete", usage.State)
	assert.Equal(t, "UNKNOWN", row.Status)
	assert.Empty(t, row.Charged)
	assert.Zero(t, remoteCalls, "incomplete usage must not settle or release")
}

func TestTextInputLegacyAdmissionAndOverBoundUsage(t *testing.T) {
	failSettle := false
	a, db := fixture(t, &failSettle)
	a.config.MaxInputTokens, a.config.MaxOutputTokens, a.config.MaxBodyBytes = 65536, 4096, 1<<20
	policy := textPolicyFixture()
	policy.ChannelType, policy.UpstreamModel = constant.ChannelTypeDeepSeek, "test"
	a.config.TextInputPolicies = map[string]map[string]TextInputPolicy{"chat.completions.create": {"test": policy}}
	prefix, suffix := `{"model":"test","messages":[{"role":"user","content":"`, `"}],"max_tokens":4096}`
	body := []byte(prefix + strings.Repeat("x", 99750-len(prefix)-len(suffix)) + suffix)
	session, err := a.Admit(context.Background(), "uh_model_token", "task", "call", body)
	require.NoError(t, err)
	var row Call
	require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
	assert.EqualValues(t, 199500, row.ReservationQuantity)
	assert.EqualValues(t, 4096, row.ReservationOutput)
	assert.Equal(t, "207692", row.Amount)
	assert.NotEmpty(t, row.TextInputSnapshot)
	require.NoError(t, session.Dispatch(context.Background(), 7))
	session.observe(&dto.Usage{PromptTokens: 199501, CompletionTokens: 1, TotalTokens: 199502})
	require.NoError(t, session.Finish(context.Background()))
	require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
	assert.Equal(t, "UNKNOWN", row.Status)
	assert.Empty(t, row.Charged)
}

func TestTextInputOldCallReplayIgnoresNewBounds(t *testing.T) {
	failSettle := false
	a, db := fixture(t, &failSettle)
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}],"max_tokens":50}`)
	_, err := a.Admit(context.Background(), "uh_model_token", "task", "call", body)
	require.NoError(t, err)
	// Historical exclusive_v1 rows have neither a text certificate nor persisted
	// quantity/output fields. Their canonical identity and original hold survive.
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "call").Updates(map[string]any{"text_input_snapshot": "", "reservation_quantity": 0, "reservation_output": 0, "amount": "1100"}).Error)
	var before Call
	require.NoError(t, db.First(&before, "call_id = ?", "call").Error)
	policy := textPolicyFixture()
	policy.MaxInputTokens, policy.MaxOutputTokens = 1, 100
	a.config.TextInputPolicies = map[string]map[string]TextInputPolicy{"chat.completions.create": {"test": policy}}
	session, err := a.Admit(context.Background(), "uh_model_token", "task", "call", body)
	assert.Nil(t, session)
	assert.ErrorIs(t, err, ErrDuplicate)
	var after Call
	require.NoError(t, db.First(&after, "call_id = ?", "call").Error)
	assert.Equal(t, before, after, "replay must preserve the original amount, digest and snapshot")
}

func TestTextInputReplayKeepsFrozenReservationAfterPolicyChange(t *testing.T) {
	failSettle := false
	a, db := fixture(t, &failSettle)
	policy := textPolicyFixture()
	policy.MaxInputTokens, policy.MaxOutputTokens = 1024, 100
	a.config.TextInputPolicies = map[string]map[string]TextInputPolicy{"chat.completions.create": {"test": policy}}
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}],"max_tokens":50}`)
	_, err := a.Admit(context.Background(), "uh_model_token", "task", "call", body)
	require.NoError(t, err)
	var before Call
	require.NoError(t, db.First(&before, "call_id = ?", "call").Error)
	require.NotEmpty(t, before.TextInputSnapshot)
	assert.EqualValues(t, 2*len(body), before.ReservationQuantity)
	policy.MaxInputTokens = 1
	a.config.TextInputPolicies["chat.completions.create"]["test"] = policy
	session, err := a.Admit(context.Background(), "uh_model_token", "task", "call", body)
	assert.Nil(t, session)
	assert.ErrorIs(t, err, ErrDuplicate)
	_, err = a.Admit(context.Background(), "uh_model_token", "task", "call", append([]byte(" \n"), body...))
	assert.ErrorIs(t, err, ErrConflict, "legacy exclusive digests continue to bind exact body bytes")
	var after Call
	require.NoError(t, db.First(&after, "call_id = ?", "call").Error)
	assert.Equal(t, before, after, "retries and current policy cannot replace frozen bounds")
}
