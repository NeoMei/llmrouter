package platformbilling

import (
	"bytes"
	"encoding/base64"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestCertifiedFrozenVisionReservation(t *testing.T) {
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	encoded := base64.StdEncoding.EncodeToString(imageBytes.Bytes())
	for _, wire := range []string{"chat.completions.create", "responses.create", "claude.messages.create", "gemini.generate"} {
		t.Run(wire, func(t *testing.T) {
			c := Config{MaxBodyBytes: 65536, MaxInputTokens: 10000, MaxOutputTokens: 100}
			body := map[string]any{"model": "vision", "max_tokens": 10, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + encoded}}}}}}
			channel := constant.ChannelTypeOpenAI
			switch wire {
			case "responses.create":
				body = map[string]any{"model": "vision", "max_output_tokens": 10, "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + encoded}}}}}
			case "claude.messages.create":
				channel = constant.ChannelTypeAnthropic
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": encoded}}}}}
			case "gemini.generate":
				channel = constant.ChannelTypeGemini
				body = map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": encoded}}}}}, "generationConfig": map[string]any{"maxOutputTokens": 10}}
			}
			raw, e := common.Marshal(body)
			require.NoError(t, e)
			parse := func() (operationRequest, error) {
				switch wire {
				case "claude.messages.create":
					return parseClaudeOperation(c, raw)
				case "gemini.generate":
					return parseGeminiOperation(c, "vision", raw)
				case "responses.create":
					return parseOperation(c, "responses", "application/json", raw)
				default:
					return parseOperation(c, "chat", "application/json", raw)
				}
			}
			_, e = parse()
			require.Error(t, e, "uncertified media must never be funded")
			policy := NativeInputPolicy{Version: "hard_context_v1", ProviderReference: "https://provider.example/model-contract", ChannelType: channel, UpstreamModel: "vision", HardInputTokens: 9000, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 10}
			c.NativeInputPolicies = map[string]map[string]NativeInputPolicy{wire: {"vision": policy}}
			req, e := parse()
			require.NoError(t, e)
			assert.Equal(t, 9000, req.Quantity)
			assert.NotEmpty(t, req.MediaSnapshot)
			assert.Contains(t, req.MediaSnapshot, `"height":3`)
			assert.JSONEq(t, string(raw), string(req.Canonical))
			first := req.MediaSnapshot
			policy.HardInputTokens = 8000
			c.NativeInputPolicies[wire]["vision"] = policy
			req, e = parse()
			require.NoError(t, e)
			assert.NotEqual(t, first, req.MediaSnapshot)
			policy.MaxPixels = 5
			c.NativeInputPolicies[wire]["vision"] = policy
			_, e = parse()
			require.Error(t, e)
			policy.MaxPixels = 100
			c.NativeInputPolicies[wire]["vision"] = policy
			raw = []byte(strings.ReplaceAll(string(raw), encoded, base64.StdEncoding.EncodeToString([]byte("not an image"))))
			_, e = parse()
			require.Error(t, e)
		})
	}
}

func TestResponsesToolOutputCannotBypassMediaReservation(t *testing.T) {
	_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "responses", "application/json", []byte(`{"model":"test","max_output_tokens":10,"input":[{"type":"function_call_output","call_id":"f","output":[{"type":"input_image","image_url":"https://example.test/mutable.png"}]}]}`))
	require.Error(t, err)
}

func TestProviderStoredContextRequiresHardBound(t *testing.T) {
	c := Config{MaxInputTokens: 10000, MaxOutputTokens: 100}
	body := []byte(`{"model":"test","max_output_tokens":10,"input":"continue","previous_response_id":"resp_stored"}`)
	_, err := parseOperation(c, "responses", "application/json", body)
	require.Error(t, err)
	c.NativeInputPolicies = map[string]map[string]NativeInputPolicy{"responses.create": {"test": {Version: "hard_context_v1", ProviderReference: "https://provider.example/context", ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "test", HardInputTokens: 9000, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 10}}}
	req, err := parseOperation(c, "responses", "application/json", body)
	require.NoError(t, err)
	assert.Equal(t, 9000, req.Quantity)
	assert.NotEmpty(t, req.MediaSnapshot)
}

func TestMappedOpenAIModelMayContainProviderNamespace(t *testing.T) {
	s := &Session{call: Call{Operation: "chat", WireOperation: "chat.completions.create"}, canonical: []byte(`{"model":"public","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)}
	body, err := s.GenerationBody(constant.ChannelTypeOpenAI, types.RelayFormatOpenAI, "vendor/opaque-model")
	require.NoError(t, err)
	assert.Contains(t, string(body), `"model":"vendor/opaque-model"`)
}

func TestRedactedThinkingRequiresCertifiedContext(t *testing.T) {
	body := []byte(`{"model":"test","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]}]}`)
	_, err := parseClaudeOperation(Config{MaxBodyBytes: 8192, MaxInputTokens: 8192, MaxOutputTokens: 100}, body)
	require.Error(t, err)
}
