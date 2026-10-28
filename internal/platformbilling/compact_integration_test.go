package platformbilling

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompactIntegrationAdmissionWireAndCertificate(t *testing.T) {
	config := Config{Enabled: true, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100, ServiceToken: "compact-service-token-at-least-32"}
	certificate := CompactPolicy{Version: "hard_context_v1", ProviderReference: "https://fixture.example/compact", PublicModel: "test", ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "upstream-test", WireOperation: "responses.compact", ChannelID: 1, EndpointPath: "/v1/responses/compact", HardInputTokens: 1000, HardOutputTokens: 100, ServiceTier: "auto", RateIDs: map[string]string{"CREDIT/6": "compact-rate"}}
	raw, err := common.Marshal(map[string]any{"CompactPolicies": map[string]any{"test": certificate}})
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &config))
	grants := []string{"responses.create"}
	reserves := 0
	price := metering.Price{Version: "metered_v2", Operation: "responses", Policy: "sum", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}, {Key: "output", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	rate := Rate{ID: "compact-rate", Model: "test", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			out = Claims{ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", TaskID: "task", Models: []string{"test"}, Operations: map[string][]string{"test": grants}, ExpiresAt: time.Now().Add(time.Hour)}
		case "/internal/v1/rates":
			out = []Rate{rate}
		case "/internal/v1/calls/reserve":
			reserves++
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			out = RemoteCall{WireOperation: in["wire_operation"], CallID: in["call_id"], ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", CredentialID: "credential", RequestDigest: in["request_digest"], Rate: rate, Status: "RESERVED", Held: in["amount"], ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter"}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		data, e := common.Marshal(out)
		require.NoError(t, e)
		_, _ = w.Write(data)
	}))
	defer hub.Close()
	config.Origin = hub.URL
	db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/compact.db"), &gorm.Config{})
	require.NoError(t, e)
	a, e := New(db, config)
	require.NoError(t, e)
	body := []byte(`{"model":"test","input":[{"type":"compaction","id":null,"encrypted_content":"opaque-history"}],"instructions":null}`)
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "call", WireOperation{ID: "responses.compact"}, "application/json", body)
	require.Error(t, e)
	assert.Zero(t, reserves)
	grants = []string{"responses.create", "responses.compact"}
	session, e := a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "call", WireOperation{ID: "responses.compact"}, "application/json", body)
	require.NoError(t, e)
	require.Error(t, session.Dispatch(context.Background(), 2), "certificate must bind concrete channel ID")
	assert.Equal(t, "responses", session.call.Operation)
	assert.Equal(t, "responses.compact", session.call.WireOperation)
	assert.EqualValues(t, 1000, session.call.ReservationQuantity)
	assert.EqualValues(t, 100, session.call.ReservationOutput)
	out, e := session.GenerationBody(constant.ChannelTypeOpenAI, types.RelayFormatOpenAIResponsesCompaction, "upstream-test")
	require.NoError(t, e)
	assert.JSONEq(t, `{"model":"upstream-test","input":[{"type":"compaction","id":null,"encrypted_content":"opaque-history"}],"instructions":null}`, string(out))
	_, e = session.GenerationBody(constant.ChannelTypeOpenAI, types.RelayFormatOpenAIResponsesCompaction, "wrong")
	require.Error(t, e)
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "call", WireOperation{ID: "responses.compact"}, "application/json", body)
	require.ErrorIs(t, e, ErrDuplicate)
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "call", WireOperation{ID: "responses.create"}, "application/json", []byte(`{"model":"test","input":"hi","max_output_tokens":100}`))
	require.ErrorIs(t, e, ErrConflict)
	for _, bad := range []string{`{"model":"test","input":"hi","max_output_tokens":100}`, `{"model":"test","input":"hi","previous_response_id":"unowned"}`, `{"model":"test","input":[{"type":"item_reference","id":"unowned"}]}`} {
		_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "other", WireOperation{ID: "responses.compact"}, "application/json", []byte(bad))
		require.Error(t, e)
	}
	changed := a.config.CompactPolicies["test"]
	changed.HardOutputTokens = 99
	a.config.CompactPolicies["test"] = changed
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "call", WireOperation{ID: "responses.compact"}, "application/json", body)
	require.ErrorIs(t, e, ErrDuplicate, "replay uses the original immutable certificate before current policy lookup")
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "call", WireOperation{ID: "responses.compact"}, "application/json", []byte(`{"model":"test","input":"changed"}`))
	require.ErrorIs(t, e, ErrConflict)
	a.config.CompactPolicies["test"] = certificate
	for _, task := range []string{"other-root"} {
		_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", task, "other-root-call", WireOperation{ID: "responses.compact"}, "application/json", body)
		require.Error(t, e)
	}
	rate.ID = "unpinned-rate"
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "unpinned-call", WireOperation{ID: "responses.compact"}, "application/json", body)
	require.Error(t, e)
	assert.Equal(t, 1, reserves)
}

func TestCompactIntegrationCatalogRequiresExactCertificate(t *testing.T) {
	yes := true
	c := Config{Enabled: true, MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100, OperationLimits: map[string]map[string]OperationLimits{"responses": {"test": {Policy: "sum"}}}}
	a := &Adapter{config: c}
	sources := []CatalogSourceModel{{PublicID: "test", SelectableChannelTypes: []int{constant.ChannelTypeOpenAI}, ModelCapabilities: dto.ModelCapabilities{Operations: []string{"responses.compact"}, Streaming: &yes}}}
	out, err := a.ProjectCatalog("superwagie", sources)
	require.NoError(t, err)
	require.Len(t, out.Models, 1)
	assert.False(t, out.Models[0].Operations[0].Available)
	a.config.CompactPolicies = map[string]CompactPolicy{"test": {Version: "hard_context_v1", ProviderReference: "https://fixture.example/compact", PublicModel: "test", ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "upstream-test", WireOperation: "responses.compact", ChannelID: 1, EndpointPath: "/v1/responses/compact", HardInputTokens: 900, HardOutputTokens: 80, ServiceTier: "auto", RateIDs: map[string]string{"CREDIT/6": "rate"}}}
	out, err = a.ProjectCatalog("superwagie", sources)
	require.NoError(t, err)
	op := out.Models[0].Operations[0]
	require.True(t, op.Available, op.DisabledReason)
	assert.Equal(t, "/v1/responses/compact", op.Endpoint)
	assert.Equal(t, []string{"json"}, op.Transports)
	assert.Equal(t, "responses", op.BillingOperation)
	assert.Equal(t, 900, out.Models[0].ContextWindow)
	assert.Equal(t, 80, *out.Models[0].Capabilities.MaxOutputTokens)
	assert.Equal(t, map[string]string{"CREDIT/6": "rate"}, op.RateIDs)
	output := 70
	sources[0].MaxOutputTokens = &output
	out, err = a.ProjectCatalog("superwagie", sources)
	require.NoError(t, err)
	assert.False(t, out.Models[0].Operations[0].Available, "native output ceiling cannot undercut certified compact output")
	sources[0].MaxOutputTokens = nil
	sources[0].ContextWindow = 800
	out, err = a.ProjectCatalog("superwagie", sources)
	require.NoError(t, err)
	assert.False(t, out.Models[0].Operations[0].Available, "native context ceiling cannot undercut certified compact input")
}

func TestCompactIntegrationConfigCertificateValidation(t *testing.T) {
	env := map[string]string{"UNIHUB_PLATFORM_ENABLED": "true", "UNIHUB_ROUTING_PRINCIPAL_ID": "1", "UNIHUB_MAX_INPUT_TOKENS": "1000", "UNIHUB_MAX_OUTPUT_TOKENS": "100", "UNIHUB_MAX_BODY_BYTES": "8192"}
	for _, bad := range []string{`{"test":{"version":"bad"}}`, `{"test":{},"test":{}}`, `{"test":{"version":"hard_context_v1","provider_reference":"https://fixture.example","public_model":"test","channel_id":1,"channel_type":1,"upstream_model":"test","wire_operation":"responses.compact","endpoint_path":"/v1/responses/compact","hard_input_tokens":1000,"hard_output_tokens":100,"service_tier":"auto","rate_ids":{"CNY/6":"rate"},"unrecognized":true}}`} {
		env["UNIHUB_COMPACT_POLICIES"] = bad
		_, err := ConfigFromEnv(func(k string) string { return env[k] })
		require.Error(t, err)
	}
}
