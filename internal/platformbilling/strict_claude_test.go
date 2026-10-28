package platformbilling

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeClaudeToolThinkingHistoryPreservesWire(t *testing.T) {
	body := []byte(`{"model":"claude-test","max_tokens":2048,"stream":false,"system":[{"type":"text","text":"System","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":"lookup"},{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"opaque:001+/="},{"type":"redacted_thinking","data":"opaque:encrypted"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"model":"not-a-model","n":9007199254740993,"false":false}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"found"}],"is_error":false}]}],"tools":[{"name":"lookup","description":"lookup","input_schema":{"type":"object","properties":{"model":{"type":"string"},"candidateCount":{"const":0}},"additionalProperties":false}}],"tool_choice":{"type":"auto","disable_parallel_tool_use":false},"thinking":{"type":"enabled","budget_tokens":1024},"metadata":{"user_id":"user-1"}}`)
	c := Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768, NativeInputPolicies: map[string]map[string]NativeInputPolicy{"claude.messages.create": {"claude-test": {Version: "hard_context_v1", ProviderReference: "https://provider.example/context", ChannelType: constant.ChannelTypeAnthropic, UpstreamModel: "claude-test", HardInputTokens: 1 << 20, MaxBytes: 8192, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 10}}}}
	got, err := parseClaudeOperation(c, body)
	require.NoError(t, err)
	assert.Equal(t, "claude-test", got.Model)
	assert.Equal(t, 2048, got.Output)
	assert.Equal(t, c.MaxInputTokens, got.Quantity)
	var original map[string]any
	require.NoError(t, common.UnmarshalUnique(body, &original))
	canonical, err := common.Marshal(original)
	require.NoError(t, err)
	assert.Equal(t, canonical, got.Canonical)
	assert.Contains(t, string(got.Canonical), `9007199254740993`)
	assert.Contains(t, string(got.Canonical), `"is_error":false`)
	var decoded dto.ClaudeRequest
	require.NoError(t, common.Unmarshal(got.Canonical, &decoded))
	require.NotNil(t, decoded.Stream)
	assert.False(t, *decoded.Stream)
}

func TestNativeClaudeRejectsInvalidAdmission(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing cap", `{"model":"m","messages":[{"role":"user","content":"x"}]}`},
		{"legacy cap alias", `{"model":"m","max_tokens":20,"max_tokens_to_sample":20,"messages":[{"role":"user","content":"x"}]}`},
		{"unknown envelope", `{"model":"m","max_tokens":20,"n":2,"messages":[{"role":"user","content":"x"}]}`},
		{"case alias", `{"model":"m","Model":"m","max_tokens":20,"messages":[{"role":"user","content":"x"}]}`},
		{"duplicate schema", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":"x"}],"tools":[{"name":"f","input_schema":{"n":1,"n":2}}]}`},
		{"null stream", `{"model":"m","max_tokens":20,"stream":null,"messages":[{"role":"user","content":"x"}]}`},
		{"huge unsigned", `{"model":"m","max_tokens":18446744073686646784,"messages":[{"role":"user","content":"x"}]}`},
		{"fractional count", `{"model":"m","max_tokens":20.0,"messages":[{"role":"user","content":"x"}]}`},
		{"bad role", `{"model":"m","max_tokens":20,"messages":[{"role":"system","content":"x"}]}`},
		{"missing signature", `{"model":"m","max_tokens":20,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x"}]}]}`},
		{"tool input not object", `{"model":"m","max_tokens":20,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"f","input":"{}"}]}]}`},
		{"wrong result bool", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","is_error":"false"}]}]}`},
		{"unknown block", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":[{"type":"text","text":"x","n":2}]}]}`},
		{"bad image source", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":123}}]}]}`},
		{"source union", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png","data":"x"}}]}]}`},
		{"server tool", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":"x"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`},
		{"undeclared choice", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":"x"}],"tool_choice":{"type":"tool","name":"missing"}}`},
		{"thinking above cap", `{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled","budget_tokens":2048}}`},
		{"disabled budget", `{"model":"m","max_tokens":2048,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"disabled","budget_tokens":0}}`},
		{"sampler range", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":"x"}],"temperature":1.1}`},
		{"metadata type", `{"model":"m","max_tokens":20,"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseClaudeOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, []byte(tc.body))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestNativeClaudeMediaNeedsCertifiedReservation(t *testing.T) {
	for _, block := range []string{
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}`,
		`{"type":"image","source":{"type":"url","url":"https://example.com/image.png"}}`,
		`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGVsbG8="},"title":"PDF","citations":{"enabled":false}}`,
	} {
		body := []byte(`{"model":"m","max_tokens":20,"messages":[{"role":"user","content":[` + block + `]}]}`)
		_, err := parseClaudeOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, body)
		require.ErrorIs(t, err, errNativeMediaReservationUnavailable)
	}
}

func TestNativeClaudeHistoryAndBodyBounds(t *testing.T) {
	messages := strings.TrimSuffix(strings.Repeat(`{"role":"user","content":"x"},`, 80), ",")
	body := []byte(`{"model":"m","max_tokens":20,"messages":[` + messages + `]}`)
	c := Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}
	_, err := parseClaudeOperation(c, body)
	require.NoError(t, err)
	c.MaxBodyBytes = int64(len(body) - 1)
	_, err = parseClaudeOperation(c, body)
	require.ErrorIs(t, err, ErrInvalid)
	c.MaxBodyBytes = 1 << 20
	c.MaxInputTokens = len(body)*2 - 1
	_, err = parseClaudeOperation(c, body)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestNativeClaudeThinkingModesAndPlainContent(t *testing.T) {
	for _, thinking := range []string{`{"type":"enabled","budget_tokens":1024}`, `{"type":"adaptive","display":"omitted"}`, `{"type":"disabled"}`} {
		body := []byte(`{"model":"m","max_tokens":2048,"system":"System","messages":[{"role":"user","content":"x"}],"thinking":` + thinking + `}`)
		_, err := parseClaudeOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, body)
		require.NoError(t, err)
	}
}

func TestNativeClaudeRejectsManualThinkingSamplers(t *testing.T) {
	for _, tc := range []struct{ name, sampler string }{
		{"temperature zero", `"temperature":0`},
		{"temperature override", `"temperature":0.7`},
		{"top k zero", `"top_k":0`},
		{"top k override", `"top_k":20`},
		{"top p zero", `"top_p":0`},
		{"top p below manual range", `"top_p":0.94`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"m","max_tokens":2048,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled","budget_tokens":1024},` + tc.sampler + `}`)
			_, err := parseClaudeOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, body)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestNativeClaudeNonThinkingZeroSamplersPreserveWire(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":20,"messages":[{"role":"user","content":"x"}],"temperature":0,"top_p":0,"top_k":0,"stream":false}`)
	got, err := parseClaudeOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, body)
	require.NoError(t, err)
	var decoded dto.ClaudeRequest
	require.NoError(t, common.Unmarshal(got.Canonical, &decoded))
	require.NotNil(t, decoded.Temperature)
	assert.Zero(t, *decoded.Temperature)
	require.NotNil(t, decoded.TopP)
	assert.Zero(t, *decoded.TopP)
	require.NotNil(t, decoded.TopK)
	assert.Zero(t, *decoded.TopK)
	var original map[string]any
	require.NoError(t, common.UnmarshalUnique(body, &original))
	canonical, err := common.Marshal(original)
	require.NoError(t, err)
	assert.Equal(t, canonical, got.Canonical)
}

func TestNativeClaudeManualThinkingTopPRange(t *testing.T) {
	for _, sampler := range []string{`"top_p":0.95`, `"top_p":1`} {
		body := []byte(`{"model":"m","max_tokens":2048,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled","budget_tokens":1024},` + sampler + `}`)
		_, err := parseClaudeOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, body)
		require.NoError(t, err)
	}
}

func TestNativeClaudeManualThinkingExplicitDefaultTemperature(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":2048,"temperature":1,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"x"}]}`)
	got, err := parseClaudeOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, body)
	require.NoError(t, err)
	var original map[string]any
	require.NoError(t, common.UnmarshalUnique(body, &original))
	canonical, err := common.Marshal(original)
	require.NoError(t, err)
	assert.Equal(t, canonical, got.Canonical)
	assert.Contains(t, string(got.Canonical), `"temperature":1`)
}
