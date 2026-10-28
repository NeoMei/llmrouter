package platformbilling

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compactAdmissionFixture() (Config, CompactPolicy) {
	return Config{MaxInputTokens: 16384, MaxOutputTokens: 4096, MaxBodyBytes: 8192}, CompactPolicy{
		Version: "hard_context_v1", ProviderReference: "https://provider.example/compact-contract", PublicModel: "test",
		ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "supplier-test", WireOperation: "responses.compact", EndpointPath: "/v1/responses/compact",
		HardInputTokens: 12000, HardOutputTokens: 3000, ServiceTier: "auto",
	}
}

func TestCompactAdmissionNativeHistoryPreserved(t *testing.T) {
	// Real native compact has no output-cap field. Reusing create admission rejects it.
	c, p := compactAdmissionFixture()
	body := []byte(`{"model":"test","instructions":null,"previous_response_id":"resp_1","prompt_cache_key":"Case","prompt_cache_options":{"mode":"explicit","ttl":"30m"},"prompt_cache_retention":null,"service_tier":null,"input":[{"role":"user","content":"run"},{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":null},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"Case\":0,\"case\":false}"},{"type":"function_call_output","call_id":"call_1","output":"{\"Result\":false}"},{"type":"custom_tool_call","call_id":"call_2","name":"shell","input":"printf ok"},{"type":"custom_tool_call_output","call_id":"call_2","output":[{"type":"input_text","text":"ok"}]},{"type":"message","id":"msg_1","role":"assistant","phase":null,"status":"completed","content":[{"type":"output_text","text":"done","annotations":[],"logprobs":[]}]},{"type":"compaction","id":"cmp_1","encrypted_content":"Opaque+Case/=="}]}`)
	got, err := parseCompactOperation(c, p, body)
	require.NoError(t, err)
	assert.Equal(t, "test", got.Model)
	assert.Equal(t, 12000, got.Quantity)
	assert.Equal(t, 3000, got.Output)
	var original, canonical map[string]any
	require.NoError(t, common.UnmarshalUnique(body, &original))
	require.NoError(t, common.UnmarshalUnique(got.Canonical, &canonical))
	assert.Equal(t, original, canonical)
	assert.NotContains(t, string(got.Canonical), "max_output_tokens")
	assert.Contains(t, string(got.Canonical), `"encrypted_content":"Opaque+Case/=="`)
	var frozen struct {
		Policy CompactPolicy `json:"compact_policy"`
	}
	require.NoError(t, common.UnmarshalUnique([]byte(got.MediaSnapshot), &frozen))
	assert.Equal(t, p, frozen.Policy)
	again, err := parseCompactOperation(c, p, []byte(" \n"+string(got.Canonical)))
	require.NoError(t, err)
	assert.Equal(t, got, again)
	changed := p
	changed.HardOutputTokens = 2999
	different, err := parseCompactOperation(c, changed, body)
	require.NoError(t, err)
	assert.NotEqual(t, got.MediaSnapshot, different.MediaSnapshot)
}

func TestCompactAdmissionOptionalNativeFields(t *testing.T) {
	c, p := compactAdmissionFixture()
	for _, body := range []string{
		`{"model":"test","input":"hi"}`,
		`{"model":"test","input":null,"previous_response_id":"resp_1","instructions":"","prompt_cache_key":null,"prompt_cache_options":null,"prompt_cache_retention":"in_memory","service_tier":"auto"}`,
		`{"model":"test","previous_response_id":"resp_1","prompt_cache_retention":"24h","prompt_cache_options":{}}`,
		`{"model":"test","input":[{"type":"item_reference","id":"msg_1"},{"type":"reasoning","summary":[],"encrypted_content":"opaque"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			got, err := parseCompactOperation(c, p, []byte(body))
			require.NoError(t, err)
			var original, canonical map[string]any
			require.NoError(t, common.UnmarshalUnique([]byte(body), &original))
			require.NoError(t, common.UnmarshalUnique(got.Canonical, &canonical))
			assert.Equal(t, original, canonical)
		})
	}
	for _, tier := range []string{"default", "fast", "flex", "priority"} {
		t.Run(tier, func(t *testing.T) {
			selected := p
			selected.ServiceTier = tier
			got, err := parseCompactOperation(c, selected, []byte(`{"model":"test","input":"hi","service_tier":"`+tier+`"}`))
			require.NoError(t, err)
			assert.Equal(t, 3000, got.Output)
		})
	}
}

func TestCompactAdmissionRejectsMalformedIntent(t *testing.T) {
	c, p := compactAdmissionFixture()
	for name, body := range map[string]string{
		"missing model": `{"input":"hi"}`, "null model": `{"model":null,"input":"hi"}`, "empty model": `{"model":" ","input":"hi"}`,
		"wrong model": `{"model":"other","input":"hi"}`, "missing source": `{"model":"test"}`, "null source": `{"model":"test","input":null}`, "empty history": `{"model":"test","input":[]}`,
		"input object": `{"model":"test","input":{"role":"user","content":"hi"}}`, "role": `{"model":"test","input":[{"role":"tool","content":"hi"}]}`,
		"tool result":                  `{"model":"test","input":[{"type":"function_call_output","call_id":"x","output":{"Case":0}}]}`,
		"tool arguments":               `{"model":"test","input":[{"type":"function_call","call_id":"x","name":"f","arguments":false}]}`,
		"signature type":               `{"model":"test","input":[{"type":"compaction","encrypted_content":false}]}`,
		"reasoning signature type":     `{"model":"test","input":[{"type":"reasoning","summary":[],"encrypted_content":0}]}`,
		"compaction missing signature": `{"model":"test","input":[{"type":"compaction","id":"cmp_1"}]}`,
		"compaction unknown key":       `{"model":"test","input":[{"type":"compaction","encrypted_content":"opaque","Signature":"x"}]}`,
		"case collision":               `{"model":"test","Model":"test","input":"hi"}`,
		"duplicate":                    `{"model":"test","input":"hi","input":"bye"}`,
		"nested duplicate":             `{"model":"test","input":[{"type":"compaction","encrypted_content":"a","encrypted_content":"b"}]}`,
		"trailing":                     `{"model":"test","input":"hi"} {}`,
		"instructions type":            `{"model":"test","input":"hi","instructions":false}`,
		"previous type":                `{"model":"test","input":"hi","previous_response_id":0}`,
		"empty previous":               `{"model":"test","input":null,"previous_response_id":""}`,
		"cache type":                   `{"model":"test","input":"hi","prompt_cache_key":0}`,
		"cache too long":               `{"model":"test","input":"hi","prompt_cache_key":"` + strings.Repeat("a", 65) + `"}`,
		"cache options type":           `{"model":"test","input":"hi","prompt_cache_options":false}`,
		"cache options unknown":        `{"model":"test","input":"hi","prompt_cache_options":{"enabled":false}}`,
		"cache mode":                   `{"model":"test","input":"hi","prompt_cache_options":{"mode":"future"}}`,
		"cache ttl":                    `{"model":"test","input":"hi","prompt_cache_options":{"ttl":"12h"}}`,
		"cache retention":              `{"model":"test","input":"hi","prompt_cache_retention":"forever"}`,
		"service tier":                 `{"model":"test","input":"hi","service_tier":"future"}`,
		"uncertified service tier":     `{"model":"test","input":"hi","service_tier":"priority"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseCompactOperation(c, p, []byte(body))
			require.Error(t, err)
			assert.Empty(t, got.Canonical)
		})
	}
	for _, field := range []string{`"max_output_tokens":10`, `"max_output_tokens":0`, `"max_output_tokens":null`, `"tools":[]`, `"parallel_tool_calls":false`, `"reasoning":{"effort":"low"}`, `"text":{"verbosity":"low"}`, `"stream":false`, `"store":false`, `"metadata":{"Case":0}`, `"temperature":0`} {
		t.Run(field, func(t *testing.T) {
			_, err := parseCompactOperation(c, p, []byte(`{"model":"test","input":"hi",`+field+`}`))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestCompactAdmissionRequiresBoundCertificate(t *testing.T) {
	c, p := compactAdmissionFixture()
	for name, mutate := range map[string]func(*CompactPolicy){
		"missing": func(v *CompactPolicy) { *v = CompactPolicy{} }, "version": func(v *CompactPolicy) { v.Version = "guessed" }, "reference": func(v *CompactPolicy) { v.ProviderReference = "https://" },
		"insecure reference": func(v *CompactPolicy) { v.ProviderReference = "http://provider.example/context" }, "model": func(v *CompactPolicy) { v.PublicModel = "other" },
		"channel": func(v *CompactPolicy) { v.ChannelType = 0 }, "upstream": func(v *CompactPolicy) { v.UpstreamModel = "" },
		"wire": func(v *CompactPolicy) { v.WireOperation = "responses.create" }, "endpoint": func(v *CompactPolicy) { v.EndpointPath = "/v1/responses" },
		"zero input": func(v *CompactPolicy) { v.HardInputTokens = 0 }, "negative input": func(v *CompactPolicy) { v.HardInputTokens = -1 }, "input over config": func(v *CompactPolicy) { v.HardInputTokens = 16385 },
		"zero output": func(v *CompactPolicy) { v.HardOutputTokens = 0 }, "negative output": func(v *CompactPolicy) { v.HardOutputTokens = -1 }, "output over config": func(v *CompactPolicy) { v.HardOutputTokens = 4097 }, "tier": func(v *CompactPolicy) { v.ServiceTier = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := p
			mutate(&bad)
			got, err := parseCompactOperation(c, bad, []byte(`{"model":"test","input":"hi"}`))
			require.Error(t, err)
			assert.Empty(t, got.Canonical)
		})
	}
	for _, bad := range []Config{{MaxInputTokens: 0, MaxOutputTokens: 4096, MaxBodyBytes: 8192}, {MaxInputTokens: 16384, MaxOutputTokens: 0, MaxBodyBytes: 8192}, {MaxInputTokens: 16384, MaxOutputTokens: 4096, MaxBodyBytes: 0}} {
		_, err := parseCompactOperation(bad, p, []byte(`{"model":"test","input":"hi"}`))
		require.Error(t, err)
	}
	body := []byte(`{"model":"test","input":"hi"}`)
	c.MaxBodyBytes = int64(len(body))
	_, err := parseCompactOperation(c, p, body)
	require.NoError(t, err)
	c.MaxBodyBytes--
	got, err := parseCompactOperation(c, p, body)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Empty(t, got.Canonical)
}

func TestCompactAdmissionMediaRequiresWireAwareSnapshotIntegration(t *testing.T) {
	c, p := compactAdmissionFixture()
	for _, input := range []string{
		`[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]`,
		`[{"role":"user","content":[{"type":"input_file","file_id":"file_1"}]}]`,
		`[{"type":"function_call_output","call_id":"x","output":[{"type":"input_image","image_url":"https://example.invalid/image.png"}]}]`,
	} {
		_, err := parseCompactOperation(c, p, []byte(`{"model":"test","input":`+input+`}`))
		require.ErrorIs(t, err, errNativeMediaReservationUnavailable)
	}
}

func TestCompactAdmissionRejectsInvalidUTF8WithoutChangingOpaqueInput(t *testing.T) {
	c, p := compactAdmissionFixture()
	// JSON decoding would replace this byte and silently change the supplier input.
	body := append([]byte(`{"model":"test","input":"`), 0xff)
	body = append(body, []byte(`"}`)...)
	got, err := parseCompactOperation(c, p, body)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Empty(t, got.Canonical)
}

func TestCompactAdmissionCompactionIDPreservesNullableContract(t *testing.T) {
	c, policy := compactAdmissionFixture()
	for name, id := range map[string]string{"absent": "", "null": `,"id":null`, "string": `,"id":"cmp_1"`} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"test","input":[{"type":"compaction","encrypted_content":"Opaque+Case/=="` + id + `}]}`)
			got, err := parseCompactOperation(c, policy, body)
			require.NoError(t, err)
			var original, canonical map[string]any
			require.NoError(t, common.UnmarshalUnique(body, &original))
			require.NoError(t, common.UnmarshalUnique(got.Canonical, &canonical))
			assert.Equal(t, original, canonical)
			if name == "null" {
				assert.Contains(t, string(got.Canonical), `"id":null`)
			}
		})
	}
	for _, id := range []string{`0`, `false`, `{}`, `[]`} {
		t.Run(id, func(t *testing.T) {
			got, err := parseCompactOperation(c, policy, []byte(`{"model":"test","input":[{"type":"compaction","id":`+id+`,"encrypted_content":"Opaque+Case/=="}]}`))
			require.ErrorIs(t, err, ErrInvalid)
			assert.Empty(t, got.Canonical)
		})
	}
}
