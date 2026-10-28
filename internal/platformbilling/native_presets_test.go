package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func presetFixture(wire string) (Config, NativePresetPolicy) {
	sum := sha256.Sum256([]byte("synthetic-member"))
	p := NativePresetPolicy{Version: "fixture-v1", PublicModel: "test", WireOperation: wire, ChannelID: 1, ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "upstream", ChannelKeySHA256: hex.EncodeToString(sum[:]), Origin: "https://fixture.invalid", APIVersion: "v1"}
	params := `{"reasoning_effort":"high"}`
	switch wire {
	case "responses.create":
		params = `{"reasoning":{"effort":"high"}}`
	case "claude.messages.create":
		p.ChannelType = constant.ChannelTypeAnthropic
		p.APIVersion = "2023-06-01"
		params = `{"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":2048}`
	case "gemini.generate":
		p.ChannelType = constant.ChannelTypeGemini
		p.APIVersion = "v1beta"
		params = `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0,"includeThoughts":false}}}`
	}
	p.Variants = []NativePreset{{ID: "deliberate", Label: "Exact <preset>", Parameters: json.RawMessage(params)}}
	c := Config{Enabled: true, InstanceID: "fixture", MaxBodyBytes: 8192, MaxInputTokens: 8192, MaxOutputTokens: 4096, NativePresetPolicies: map[string]map[string]NativePresetPolicy{wire: {"test": p}}, OperationLimits: map[string]map[string]OperationLimits{"chat": {"test": {Policy: "provider_reported_v1", MaxBodyBytes: 8192}}, "responses": {"test": {Policy: "provider_reported_v1", MaxBodyBytes: 8192}}}, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CNY/6": {"test": "native-rate"}}}}
	return c, p
}
func TestNativePresetsStrictProtocolAndBounds(t *testing.T) {
	cases := []struct {
		wire, params   string
		adaptive, want bool
	}{
		{"chat.completions.create", `{"reasoning_effort":"high"}`, false, true},
		{"responses.create", `{"reasoning":{"effort":"low"}}`, false, true},
		{"claude.messages.create", `{"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":2048}`, false, true},
		{"claude.messages.create", `{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":2048}`, false, false},
		{"claude.messages.create", `{"thinking":{"type":"enabled","budget_tokens":1024}}`, false, false},
		{"claude.messages.create", `{"thinking":{"type":"adaptive"},"max_tokens":2048}`, false, false},
		{"claude.messages.create", `{"thinking":{"type":"adaptive"},"max_tokens":2048}`, true, true},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0,"includeThoughts":false}}}`, false, true},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`, false, true},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`, false, true},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":5000}}}`, false, false},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0,"thinkingLevel":"low"}}}`, false, false},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0,"includeThoughts":null}}}`, false, false},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":"0"}}}`, false, false},
		{"gemini.generate", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0,"thinkingBudget":1}}}`, false, false},
		{"gemini.generate", `{"reasoning_effort":"high"}`, false, false},
		{"responses.create", `{"reasoning":{"effort":"low"},"api_key":"forbidden"}`, false, false},
		{"chat.completions.create", `null`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.wire+tc.params, func(t *testing.T) {
			c, _ := presetFixture(tc.wire)
			e := validateNativePreset(c, "test", tc.wire, []byte(tc.params), tc.adaptive)
			assert.Equal(t, tc.want, e == nil)
		})
	}
}
func TestNativePresetCatalogHTTPAndImmutableCertificate(t *testing.T) {
	for _, wire := range []string{"chat.completions.create", "responses.create", "claude.messages.create", "gemini.generate"} {
		t.Run(wire, func(t *testing.T) {
			c, p := presetFixture(wire)
			require.True(t, p.valid(c, "test", wire))
			adapter := &Adapter{config: c}
			source := CatalogSourceModel{PublicID: "test", DisplayName: "Test", ContextWindow: 8192, ModelCapabilities: dto.ModelCapabilities{Operations: []string{wire}}, NativePresetTargets: map[string]bool{wire: true}}
			project := func() (TechnicalCatalog, error) {
				return adapter.ProjectCatalog("superwagie", []CatalogSourceModel{source})
			}
			catalog, e := project()
			require.NoError(t, e)
			require.Len(t, catalog.Models[0].Operations[0].ParameterVariants, 1)
			first := catalog.Models[0].Operations[0].ParameterVariants[0]
			assert.JSONEq(t, string(p.Variants[0].Parameters), string(first.Parameters))
			cfg := CatalogConfig{Token: strings.Repeat("x", 32), ProductID: "superwagie", InstanceID: "fixture"}
			h := CatalogHandler(cfg, func(context.Context) (TechnicalCatalog, error) { return project() })
			r := httptest.NewRequest("GET", "/internal/v1/model-catalog?product_id=superwagie&instance_id=fixture", nil)
			r.Header.Set("Authorization", "Bearer "+cfg.Token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, 200, w.Code)
			assert.NotContains(t, w.Body.String(), "synthetic-member")
			assert.NotContains(t, w.Body.String(), p.Origin)
			assert.NotContains(t, w.Body.String(), p.ChannelKeySHA256)
			if dir := os.Getenv("NATIVE_PRESET_FIXTURE_DIR"); dir != "" {
				require.NoError(t, os.WriteFile(dir+"/"+wire+".json", w.Body.Bytes(), 0600))
			}
			p.Version = "fixture-v2"
			c.NativePresetPolicies[wire]["test"] = p
			next, e := project()
			require.NoError(t, e)
			assert.NotEqual(t, catalog.Version, next.Version)
			assert.NotEqual(t, first.CertificateSHA256, next.Models[0].Operations[0].ParameterVariants[0].CertificateSHA256)
			source.NativePresetTargets[wire] = false
			next, e = project()
			require.NoError(t, e)
			assert.Empty(t, next.Models[0].Operations[0].ParameterVariants)
		})
	}
}
func TestNativePresetCertificateFailsChangedTargetAndConfig(t *testing.T) {
	c, p := presetFixture("gemini.generate")
	a := &Adapter{config: c}
	require.True(t, a.NativePresetTarget("test", p.WireOperation, 1, p.ChannelType, "upstream", "synthetic-member", p.Origin))
	for _, args := range []struct {
		id, typ         int
		up, key, origin string
	}{{2, p.ChannelType, "upstream", "synthetic-member", p.Origin}, {1, constant.ChannelTypeOpenAI, "upstream", "synthetic-member", p.Origin}, {1, p.ChannelType, "other", "synthetic-member", p.Origin}, {1, p.ChannelType, "upstream", "other-member", p.Origin}, {1, p.ChannelType, "upstream", "synthetic-member", "https://other.invalid"}} {
		assert.False(t, a.NativePresetTarget("test", p.WireOperation, args.id, args.typ, args.up, args.key, args.origin))
	}
	raw, e := common.Marshal(c.NativePresetPolicies)
	require.NoError(t, e)
	for _, bad := range []string{strings.Replace(string(raw), `"v1beta"`, `"v1"`, 1), strings.Replace(string(raw), `"variants":`, `"unknown":true,"variants":`, 1), strings.Replace(string(raw), `"thinkingBudget":0`, `"thinkingBudget":0,"thinkingBudget":1`, 1)} {
		assert.Error(t, decodeNativePresetPolicies([]byte(bad), &c))
	}
}
