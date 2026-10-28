package platformbilling

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogExportRequiresExactReadOnlyServiceScope(t *testing.T) {
	calls := 0
	cfg := CatalogConfig{Token: strings.Repeat("c", 32), ProductID: "superwagie", InstanceID: "fixture"}
	h := CatalogHandler(cfg, func(context.Context) (TechnicalCatalog, error) {
		calls++
		return TechnicalCatalog{SchemaVersion: 1, Version: "version", ProductID: "superwagie", InstanceID: "fixture", Models: []CatalogModel{}}, nil
	})
	for _, tc := range []struct {
		name, token, query string
		duplicate          bool
		status             int
	}{
		{"valid", cfg.Token, "?product_id=superwagie&instance_id=fixture", false, 200},
		{"missing", "", "?product_id=superwagie&instance_id=fixture", false, 401},
		{"wrong", strings.Repeat("x", 32), "?product_id=superwagie&instance_id=fixture", false, 401},
		{"duplicate", cfg.Token, "?product_id=superwagie&instance_id=fixture", true, 401},
		{"other product", cfg.Token, "?product_id=other&instance_id=fixture", false, 403},
		{"other instance", cfg.Token, "?product_id=superwagie&instance_id=other", false, 403},
		{"selector", cfg.Token, "?product_id=superwagie&instance_id=fixture&group=admin", false, 403},
		{"duplicate scope", cfg.Token, "?product_id=superwagie&product_id=other&instance_id=fixture", false, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/internal/v1/model-catalog"+tc.query, nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.duplicate {
				r.Header.Add("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			assert.Equal(t, tc.status, w.Code)
			assert.NotContains(t, w.Body.String(), cfg.Token)
		})
	}
	assert.Equal(t, 1, calls)
	h = CatalogHandler(CatalogConfig{}, func(context.Context) (TechnicalCatalog, error) {
		t.Fatal("unconfigured handler fetched")
		return TechnicalCatalog{}, nil
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/internal/v1/model-catalog", nil))
	assert.Equal(t, 503, w.Code)
}
func TestTechnicalCatalogAdvertisesDeclaredCapabilitiesAndManagedBoundaries(t *testing.T) {
	yes := true
	a := &Adapter{config: Config{Enabled: true, InstanceID: "fixture", OperationLimits: map[string]map[string]OperationLimits{"chat": {"chat": {Policy: "provider_reported_v1"}}, "image_generation": {"image": {Policy: "request_count_v1"}}, "embedding": {"embedding": {Policy: "provider_reported_v1"}}}}}
	models := []CatalogSourceModel{
		{PublicID: "chat", DisplayName: "Chat", SupportsTools: true, ModelCapabilities: dto.ModelCapabilities{Operations: []string{"chat.completions.create"}, Streaming: &yes}},
		{PublicID: "messages", DisplayName: "Messages", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"claude.messages.create"}}},
		{PublicID: "image", DisplayName: "Image", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"images.generations.create"}}},
		{PublicID: "embedding", DisplayName: "Embedding", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"embeddings.create"}, EmbeddingSpace: &dto.EmbeddingSpace{Provider: "test", Model: "embedding", Version: "v1", Dimensions: 3, InputSemantics: "text"}}},
		{PublicID: "missing-policy", DisplayName: "Missing", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"embeddings.create"}}},
	}
	got, err := a.ProjectCatalog("superwagie", models)
	require.NoError(t, err)
	require.Len(t, got.Models, 5)
	byID := map[string]CatalogModel{}
	for _, m := range got.Models {
		byID[m.ID] = m
	}
	assert.True(t, byID["chat"].SupportsTools)
	assert.Equal(t, []string{"json", "sse"}, byID["chat"].Operations[0].Transports)
	assert.Equal(t, "/v1/chat/completions", byID["chat"].Operations[0].Endpoint)
	assert.Equal(t, "usage_policy_unconfigured", byID["messages"].Operations[0].DisabledReason)
	assert.Equal(t, "usage_policy_unconfigured", byID["missing-policy"].Operations[0].DisabledReason)
	assert.Equal(t, 3, byID["embedding"].Capabilities.EmbeddingSpace.Dimensions)
	assert.False(t, byID["chat"].Operations[0].Executable) // account/rate authority belongs to UH
	again, err := a.ProjectCatalog("superwagie", models)
	require.NoError(t, err)
	assert.Equal(t, got.Version, again.Version)
	models[0].SupportsTools = false
	changed, err := a.ProjectCatalog("superwagie", models)
	require.NoError(t, err)
	assert.NotEqual(t, got.Version, changed.Version)
}

func TestCatalogExportNeverReusesFinancialOrRuntimeAuthority(t *testing.T) {
	env := map[string]string{"UNIHUB_CATALOG_EXPORT_TOKEN": strings.Repeat("c", 32), "UNIHUB_CATALOG_PRODUCT_ID": "superwagie", "UNIHUB_CATALOG_INSTANCE_ID": "fixture"}
	for _, key := range []string{"UNIHUB_MODEL_SERVICE_TOKEN", "UNIHUB_PRODUCT_RUNTIME_TOKEN", "MANAGEMENT_TOKEN"} {
		env[key] = env["UNIHUB_CATALOG_EXPORT_TOKEN"]
		assert.False(t, CatalogConfigFromEnv(func(k string) string { return env[k] }).Valid())
		delete(env, key)
	}
}

func TestCatalogRequiresEmbeddingIdentityAndDeclaredOperation(t *testing.T) {
	a := &Adapter{config: Config{Enabled: true, InstanceID: "fixture", OperationLimits: map[string]map[string]OperationLimits{"embedding": {"unknown": {Policy: "provider_reported_v1"}}}}}
	got, e := a.ProjectCatalog("superwagie", []CatalogSourceModel{{PublicID: "unknown", DisplayName: "Unknown", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"embeddings.create"}}}, {PublicID: "legacy", DisplayName: "Legacy"}})
	require.NoError(t, e)
	for _, m := range got.Models {
		if m.ID == "legacy" {
			assert.Equal(t, "capabilities_unconfigured", m.DisabledReason)
		} else {
			assert.False(t, m.Operations[0].Available)
			assert.Equal(t, "embedding_identity_unconfigured", m.Operations[0].DisabledReason)
		}
	}
}

func TestCatalogEffectiveGatewayBoundsDoNotMutateModel(t *testing.T) {
	configuredOutput := 100000
	configuredBody := int64(1000000)
	c := Config{Enabled: true, InstanceID: "default", MaxBodyBytes: 32768, MaxInputTokens: 10000, MaxOutputTokens: 100, OperationLimits: map[string]map[string]OperationLimits{"chat": {"native": {Policy: "sum"}}}}
	a := &Adapter{config: c}
	source := CatalogSourceModel{PublicID: "native", ModelCapabilities: dto.ModelCapabilities{MaxOutputTokens: &configuredOutput, MediaLimits: &dto.MediaLimits{MaxRequestBytes: &configuredBody}, Operations: []string{"claude.messages.create"}}}
	out, err := a.ProjectCatalog("superwagie", []CatalogSourceModel{source})
	require.NoError(t, err)
	cap := out.Models[0].Capabilities
	require.NotNil(t, cap.MaxOutputTokens)
	assert.Equal(t, 100, *cap.MaxOutputTokens)
	require.NotNil(t, cap.MediaLimits)
	assert.EqualValues(t, 5000, *cap.MediaLimits.MaxRequestBytes)
	assert.Equal(t, 100000, configuredOutput)
	assert.EqualValues(t, 1000000, configuredBody)
	body := []byte(`{"model":"native","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	_, err = parseClaudeOperation(c, body)
	require.NoError(t, err)
	_, err = parseClaudeOperation(c, []byte(strings.Replace(string(body), `"max_tokens":100`, `"max_tokens":101`, 1)))
	require.Error(t, err)
}

func TestCatalogEffectiveOrdinaryInputBoundary(t *testing.T) {
	for _, wire := range []string{"chat.completions.create", "responses.create", "claude.messages.create", "gemini.generate"} {
		t.Run(wire, func(t *testing.T) {
			c := Config{Enabled: true, MaxBodyBytes: 32768, MaxInputTokens: 8192, MaxOutputTokens: 100}
			a := &Adapter{config: c}
			nativeBody := int64(32768)
			source := CatalogSourceModel{PublicID: "test", ContextWindow: 131072, ModelCapabilities: dto.ModelCapabilities{Operations: []string{wire}, MediaLimits: &dto.MediaLimits{MaxRequestBytes: &nativeBody}}}
			out, err := a.ProjectCatalog("superwagie", []CatalogSourceModel{source})
			require.NoError(t, err)
			m := out.Models[0]
			assert.Equal(t, 8192, m.ContextWindow)
			require.EqualValues(t, 4096, *m.Capabilities.MediaLimits.MaxRequestBytes)
			template := `{"model":"test","max_tokens":10,"messages":[{"role":"user","content":"%s"}]}`
			if wire == "responses.create" {
				template = `{"model":"test","max_output_tokens":10,"input":"%s"}`
			}
			if wire == "gemini.generate" {
				template = `{"contents":[{"parts":[{"text":"%s"}]}],"generationConfig":{"maxOutputTokens":10}}`
			}
			for _, extra := range []int{0, 1} {
				body := []byte(fmt.Sprintf(template, strings.Repeat("a", int(*m.Capabilities.MediaLimits.MaxRequestBytes)-len(fmt.Sprintf(template, ""))+extra)))
				switch wire {
				case "claude.messages.create":
					_, err = parseClaudeOperation(c, body)
				case "gemini.generate":
					_, err = parseGeminiOperation(c, "test", body)
				case "responses.create":
					_, err = parseOperation(c, "responses", "application/json", body)
				default:
					_, err = parseOperation(c, "chat", "application/json", body)
				}
				if extra == 0 {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			}
			assert.EqualValues(t, 32768, nativeBody)
			assert.Equal(t, 131072, source.ContextWindow)
			*m.Capabilities.MediaLimits.MaxRequestBytes = 1
			assert.EqualValues(t, 32768, nativeBody)
		})
	}
}

func TestCatalogCertifiedAndNativeContextIntersection(t *testing.T) {
	policy := NativeInputPolicy{Version: "hard_context_v1", ProviderReference: "https://fixture.example/context", ChannelType: constant.ChannelTypeAnthropic, UpstreamModel: "test", HardInputTokens: 6000, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 2}
	c := Config{MaxBodyBytes: 32768, MaxInputTokens: 8192, MaxOutputTokens: 100, NativeInputPolicies: map[string]map[string]NativeInputPolicy{"claude.messages.create": {"test": policy}}}
	a := &Adapter{config: c}
	for _, declaredContext := range []int{131072, 2000} {
		declaredBody := int64(3000)
		source := CatalogSourceModel{PublicID: "test", ContextWindow: declaredContext, ModelCapabilities: dto.ModelCapabilities{Operations: []string{"claude.messages.create"}, MediaLimits: &dto.MediaLimits{MaxRequestBytes: &declaredBody}}}
		out, err := a.ProjectCatalog("superwagie", []CatalogSourceModel{source})
		require.NoError(t, err)
		assert.Equal(t, min(declaredContext, 6000), out.Models[0].ContextWindow)
		assert.EqualValues(t, 3000, *out.Models[0].Capabilities.MediaLimits.MaxRequestBytes)
		*out.Models[0].Capabilities.MediaLimits.MaxRequestBytes = 1
		assert.EqualValues(t, 3000, declaredBody)
		assert.Equal(t, declaredContext, source.ContextWindow)
	}
}

func TestCatalogCertifiedMediaFormatIntersection(t *testing.T) {
	policy := NativeInputPolicy{Version: "hard_context_v1", ProviderReference: "https://fixture.example/context", ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "test", HardInputTokens: 6000, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 2}
	config := Config{Enabled: true, MaxBodyBytes: 32768, MaxInputTokens: 8192, MaxOutputTokens: 100, NativeInputPolicies: map[string]map[string]NativeInputPolicy{"chat.completions.create": {"test": policy}, "responses.create": {"test": policy}}, OperationLimits: map[string]map[string]OperationLimits{"chat": {"test": {Policy: "sum"}}, "responses": {"test": {Policy: "sum"}}}, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CNY/6": {"test": "rate"}}, "responses": {"CNY/6": {"test": "rate"}}}}
	for _, tc := range []struct {
		name               string
		declared, expected []string
		available          bool
	}{
		{"codec", []string{"png", "jpeg", "application/pdf"}, []string{"image/png", "image/jpeg", "application/pdf"}, true},
		{"mime", []string{"image/png", "image/jpeg", "application/pdf"}, []string{"image/png", "image/jpeg", "application/pdf"}, true},
		{"mixed", []string{"png", "image/png", "image/jpeg"}, []string{"image/png", "image/jpeg"}, true},
		{"jpeg_only", []string{"jpeg"}, []string{"image/jpeg"}, true},
		{"unknown", []string{"image/bmp"}, []string{}, false},
		{"no_inferred_jpg_alias", []string{"jpg"}, []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := CatalogSourceModel{PublicID: "test", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"chat.completions.create", "responses.create"}, MediaLimits: &dto.MediaLimits{Formats: tc.declared}}}
			a := &Adapter{config: config}
			catalog, err := a.ProjectCatalog("superwagie", []CatalogSourceModel{source})
			require.NoError(t, err)
			assert.Equal(t, tc.expected, catalog.Models[0].Capabilities.MediaLimits.Formats)
			for _, op := range catalog.Models[0].Operations {
				assert.Equal(t, tc.available, op.Available)
				if !tc.available {
					assert.Equal(t, "managed_media_formats_unavailable", op.DisabledReason)
				}
			}
			assert.Equal(t, tc.declared, source.MediaLimits.Formats)
		})
	}
}
