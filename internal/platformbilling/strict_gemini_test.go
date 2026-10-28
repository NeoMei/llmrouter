package platformbilling

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeGeminiFunctionHistoryPreservesWire(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"lookup"}]},{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"c1","args":{"model":"opaque","requests":[1,2],"n":9007199254740993}},"thoughtSignature":"opaque+/="}]},{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"lookup","response":{"candidateCount":7,"model":"payload"},"willContinue":false}}]}],"system_instruction":{"parts":[{"text":"System"}]},"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"OBJECT","properties":{"maxOutputTokens":{"type":"INTEGER"}}}}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]},"includeServerSideToolInvocations":false},"generationConfig":{"max_output_tokens":2048,"candidate_count":1,"temperature":0,"top_p":0,"top_k":0,"response_logprobs":false,"enable_enhanced_civic_answers":false,"thinking_config":{"thinking_budget":0,"include_thoughts":false},"response_mime_type":"application/json","response_json_schema":{"type":"object","properties":{"requests":{"type":"array"}}}}}`)
	c := Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}
	got, err := parseGeminiOperation(c, "gemini-test", body)
	require.NoError(t, err)
	assert.Equal(t, "gemini-test", got.Model)
	assert.Equal(t, 2048, got.Output)
	assert.Equal(t, c.MaxInputTokens, got.Quantity)
	var original map[string]any
	require.NoError(t, common.UnmarshalUnique(body, &original))
	canonical, err := common.Marshal(original)
	require.NoError(t, err)
	assert.Equal(t, canonical, got.Canonical)
	assert.Contains(t, string(got.Canonical), `9007199254740993`)
	assert.Contains(t, string(got.Canonical), `"thinking_budget":0`)
	var decoded dto.GeminiChatRequest
	require.NoError(t, common.Unmarshal(got.Canonical, &decoded))
	require.NotNil(t, decoded.GenerationConfig.ThinkingConfig)
	require.NotNil(t, decoded.GenerationConfig.ThinkingConfig.ThinkingBudget)
	assert.Zero(t, *decoded.GenerationConfig.ThinkingConfig.ThinkingBudget)
	require.NotNil(t, decoded.GenerationConfig.ResponseLogprobs)
	assert.False(t, *decoded.GenerationConfig.ResponseLogprobs)
}

func TestNativeGeminiRejectsInvalidAdmission(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing cap", `{"contents":[{"parts":[{"text":"x"}]}]}`},
		{"path mismatch", `{"model":"other","contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"batch", `{"requests":[{"model":"other"}],"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"duplicate alias", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"max_output_tokens":20}}`},
		{"thinking alias", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"thinkingConfig":{"thinkingBudget":0,"thinking_budget":0}}}`},
		{"system alias", `{"contents":[{"parts":[{"text":"x"}]}],"systemInstruction":{"parts":[{"text":"a"}]},"system_instruction":{"parts":[{"text":"b"}]},"generationConfig":{"maxOutputTokens":20}}`},
		{"duplicate args", `{"contents":[{"parts":[{"functionCall":{"name":"f","args":{"x":1,"x":2}}}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"cap null", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":null}}`},
		{"huge unsigned", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":18446744073686646784}}`},
		{"candidate multiply", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"candidateCount":2}}`},
		{"part union", `{"contents":[{"parts":[{"text":"x","functionCall":{"name":"f","args":{}}}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"nested model", `{"contents":[{"model":"other","parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"bad response", `{"contents":[{"parts":[{"functionResponse":{"name":"f","response":"x"}}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"bad signature", `{"contents":[{"parts":[{"text":"x","thoughtSignature":false}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"bad boolean", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"responseLogprobs":0}}`},
		{"thinking above cap", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"thinkingConfig":{"thinkingBudget":21}}}`},
		{"unknown config", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"sampleCount":2}}`},
		{"grounding", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20},"tools":[{"googleSearch":{}}]}`},
		{"undeclared choice", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20},"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["missing"]}}}`},
		{"audio output", `{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"responseModalities":["AUDIO"]}}`},
		{"malformed inline", `{"contents":[{"parts":[{"inline_data":{"mime_type":"image/png","data":5}}]}],"generationConfig":{"maxOutputTokens":20}}`},
		{"malformed file", `{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":42}}]}],"generationConfig":{"maxOutputTokens":20}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGeminiOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, "gemini-test", []byte(tc.body))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestNativeGeminiMediaNeedsCertifiedReservation(t *testing.T) {
	for _, part := range []string{`{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}`, `{"inline_data":{"mime_type":"audio/wav","data":"aGVsbG8="}}`, `{"fileData":{"mimeType":"application/pdf","fileUri":"https://generativelanguage.googleapis.com/v1beta/files/a"}}`} {
		body := []byte(`{"contents":[{"parts":[` + part + `]}],"generationConfig":{"maxOutputTokens":20}}`)
		_, err := parseGeminiOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, "gemini-test", body)
		require.ErrorIs(t, err, errNativeMediaReservationUnavailable)
	}
}

func TestNativeGeminiRejectsProtocolCouplingAndResponseMedia(t *testing.T) {
	for _, body := range []string{
		`{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,"logprobs":0,"responseLogprobs":false}}`,
		`{"contents":[{"parts":[{"functionResponse":{"name":"f","response":{},"parts":[{"fileData":{"mimeType":"image/png","fileUri":"https://example.com/a"}}]}}]}],"generationConfig":{"maxOutputTokens":20}}`,
		`{"contents":[{"parts":[{"fileData":{"mimeType":"invalid","fileUri":"https://example.com/a"}}]}],"generationConfig":{"maxOutputTokens":20}}`,
	} {
		_, err := parseGeminiOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, "gemini-test", []byte(body))
		require.ErrorIs(t, err, ErrInvalid)
		assert.NotErrorIs(t, err, errNativeMediaReservationUnavailable)
	}
}

func TestNativeGeminiCamelConfigAndBounds(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"x","thought":false}]}],"generationConfig":{"maxOutputTokens":20,"responseLogprobs":true,"logprobs":0,"seed":-42,"thinkingConfig":{"includeThoughts":false,"thinkingBudget":0}}}`)
	c := Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}
	_, err := parseGeminiOperation(c, "gemini-test", body)
	require.NoError(t, err)
	c.MaxBodyBytes = int64(len(body) - 1)
	_, err = parseGeminiOperation(c, "gemini-test", body)
	require.ErrorIs(t, err, ErrInvalid)
	c.MaxBodyBytes = 1 << 20
	c.MaxInputTokens = len(body)*2 - 1
	_, err = parseGeminiOperation(c, "gemini-test", body)
	require.ErrorIs(t, err, ErrInvalid)
	c.MaxInputTokens = 1 << 20
	c.MaxOutputTokens = 19
	_, err = parseGeminiOperation(c, "gemini-test", body)
	require.ErrorIs(t, err, ErrInvalid)
	c.MaxOutputTokens = 32768
	_, err = parseGeminiOperation(c, "gemini-test", body)
	require.NoError(t, err)
	for _, path := range []string{"", "models/gemini-test", "gemini-test:generateContent", "gemini-test?alt=sse"} {
		_, err := parseGeminiOperation(c, path, body)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestNativeGeminiSchemaRequiresCompatibleMIME(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"schema omitted MIME", `"responseSchema":{"type":"OBJECT"}`},
		{"schema plain MIME", `"responseSchema":{"type":"OBJECT"},"responseMimeType":"text/plain"`},
		{"json schema omitted MIME", `"response_json_schema":{"type":"object"}`},
		{"json schema enum MIME", `"response_json_schema":{"type":"object"},"response_mime_type":"text/x.enum"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,` + tc.config + `}}`)
			_, err := parseGeminiOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, "gemini-test", body)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestNativeGeminiSchemaMIMEAliasesPreserveWire(t *testing.T) {
	for _, config := range []string{
		`"responseSchema":{"type":"OBJECT","properties":{"model":{"type":"STRING"}}},"responseMimeType":"application/json"`,
		`"response_schema":{"type":"OBJECT"},"response_mime_type":"application/json"`,
		`"responseJsonSchema":{"type":"object","properties":{"n":{"const":9007199254740993}}},"responseMimeType":"application/json"`,
		`"response_json_schema":{"type":"object"},"response_mime_type":"application/json"`,
	} {
		body := []byte(`{"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":20,` + config + `}}`)
		got, err := parseGeminiOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, "gemini-test", body)
		require.NoError(t, err)
		var original map[string]any
		require.NoError(t, common.UnmarshalUnique(body, &original))
		canonical, err := common.Marshal(original)
		require.NoError(t, err)
		assert.Equal(t, canonical, got.Canonical)
	}
}

func TestNativeGeminiStringEnumMIMEPreservesWire(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"camel", `"responseMimeType":"text/x.enum","responseSchema":{"type":"STRING","enum":["yes","no"]}`},
		{"snake", `"response_mime_type":"text/x.enum","response_schema":{"type":"STRING","enum":["yes","no"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"contents":[{"parts":[{"text":"Choose yes or no"}]}],"generationConfig":{"maxOutputTokens":20,` + tc.config + `}}`)
			got, err := parseGeminiOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, "gemini-test", body)
			require.NoError(t, err)
			var original map[string]any
			require.NoError(t, common.UnmarshalUnique(body, &original))
			canonical, err := common.Marshal(original)
			require.NoError(t, err)
			assert.Equal(t, canonical, got.Canonical)
		})
	}
}

func TestNativeGeminiRejectsIncompatibleEnumSchema(t *testing.T) {
	for _, config := range []string{
		`"responseMimeType":"text/x.enum","responseSchema":{"type":"OBJECT","enum":["yes","no"]}`,
		`"responseMimeType":"text/x.enum","responseSchema":{"type":"STRING","enum":["yes",1]}`,
		`"responseMimeType":"text/x.enum","responseSchema":{"type":"STRING"}`,
		`"responseMimeType":"text/x.enum","responseJsonSchema":{"type":"string","enum":["yes","no"]}`,
	} {
		body := []byte(`{"contents":[{"parts":[{"text":"Choose yes or no"}]}],"generationConfig":{"maxOutputTokens":20,` + config + `}}`)
		_, err := parseGeminiOperation(Config{MaxBodyBytes: 1 << 20, MaxInputTokens: 1 << 20, MaxOutputTokens: 32768}, "gemini-test", body)
		require.ErrorIs(t, err, ErrInvalid)
	}
}
