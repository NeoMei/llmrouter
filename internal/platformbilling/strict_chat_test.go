package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChatAdmissionMediaAndClientToolHistory(t *testing.T) {
	body := []byte(`{"model":"test","max_tokens":10,"temperature":0,"store":false,"messages":[{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA==","detail":"low"}},{"type":"input_audio","input_audio":{"data":"AA==","format":"wav"}},{"type":"file","file":{"filename":"a.pdf","file_data":"AA=="}},{"type":"video_url","video_url":{"url":"https://example.test/video.mp4","fps":1}}]},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"Case\":0}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"Case":{"type":"number"},"case":{"type":"boolean"}}},"strict":false}}],"tool_choice":{"type":"function","function":{"name":"lookup"}}}`)
	request, err := strictChatRequest(body)
	require.NoError(t, err)
	canonicalBytes, err := common.Marshal(request)
	req := operationRequest{Canonical: canonicalBytes}
	_, admissionErr := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "chat", "application/json", body)
	require.ErrorIs(t, admissionErr, errNativeMediaReservationUnavailable)
	require.NoError(t, err)
	var original, canonical map[string]any
	require.NoError(t, common.UnmarshalUnique(body, &original))
	require.NoError(t, common.UnmarshalUnique(req.Canonical, &canonical))
	assert.Equal(t, original, canonical)
}

func TestChatAdmissionRejectsMalformedUnions(t *testing.T) {
	for name, extra := range map[string]string{
		"stop wrong union":           `"messages":[{"role":"user","content":"hi"}],"stop":false`,
		"metadata scalar":            `"messages":[{"role":"user","content":"hi"}],"metadata":3`,
		"multiword role":             `"messages":[{"role":"user assistant","content":"hi"}]`,
		"missing messages":           `"temperature":0`,
		"empty messages":             `"messages":[]`,
		"messages object":            `"messages":{}`,
		"message scalar":             `"messages":[3]`,
		"unknown role":               `"messages":[{"role":"stranger","content":"hi"}]`,
		"missing content":            `"messages":[{"role":"user"}]`,
		"content number":             `"messages":[{"role":"user","content":0}]`,
		"content object":             `"messages":[{"role":"user","content":{"text":"hi"}}]`,
		"unknown media":              `"messages":[{"role":"user","content":[{"type":"future_paid_media","text":"hi"}]}]`,
		"text wrong type":            `"messages":[{"role":"user","content":[{"type":"text","text":1}]}]`,
		"media mixed branches":       `"messages":[{"role":"user","content":[{"type":"text","text":"hi","image_url":{"url":"x"}}]}]`,
		"missing image":              `"messages":[{"role":"user","content":[{"type":"image_url"}]}]`,
		"image url wrong type":       `"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":false}}]}]`,
		"image unknown protocol key": `"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x","URL":"y"}}]}]`,
		"audio missing data":         `"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"format":"wav"}}]}]`,
		"audio format wrong type":    `"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AA==","format":2}}]}]`,
		"file no source":             `"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf"}}]}]`,
		"file conflicting source":    `"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"f","file_data":"AA=="}}]}]`,
		"video invalid fps":          `"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"x","fps":-1}}]}]`,
		"tool missing call id":       `"messages":[{"role":"tool","content":"hi"}]`,
		"tool calls object":          `"messages":[{"role":"assistant","content":null,"tool_calls":{}}]`,
		"tool arguments object":      `"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"lookup","arguments":{}}}]}]`,
		"tool missing name":          `"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"arguments":"{}"}}]}]`,
		"server tool":                `"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search"}]`,
		"tools object":               `"messages":[{"role":"user","content":"hi"}],"tools":{}`,
		"boolean wrong type":         `"messages":[{"role":"user","content":"hi"}],"store":"false"`,
		"format malformed":           `"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "chat", "application/json", []byte(`{"model":"test","max_tokens":10,`+extra+`}`))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestChatAdmissionKeepsOutputMediaFundingGate(t *testing.T) {
	for _, extra := range []string{`"modalities":["audio"],"audio":{"voice":"alloy","format":"wav"}`, `"modalities":["text"]`, `"max_completion_tokens":18446744073709551615`} {
		_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "chat", "application/json", []byte(`{"model":"test","max_tokens":10,"messages":[{"role":"user","content":"hi"}],`+extra+`}`))
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestChatAdmissionCustomTools(t *testing.T) {
	_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "chat", "application/json", []byte(`{"model":"test","max_tokens":10,"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"custom","custom":{"name":"shell","input":"echo ok"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}],"tools":[{"type":"custom","custom":{"name":"shell","format":{"type":"text"}}}],"tool_choice":{"type":"custom","custom":{"name":"shell"}}}`))
	require.NoError(t, err)
}

func TestChatAdmissionContentBelongsToRole(t *testing.T) {
	for _, message := range []string{
		`{"role":"system","content":"Be useful"}`,
		`{"role":"system","content":[{"type":"text","text":"Be useful"}]}`,
		`{"role":"developer","content":[{"type":"text","text":"Be useful"}]}`,
		`{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"ok"}]}`,
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"Cannot comply"},{"type":"refusal","refusal":"Cannot comply"}]}`,
		`{"role":"function","name":"lookup","content":null}`,
	} {
		t.Run("valid "+message, func(t *testing.T) {
			_, err := strictChatRequest([]byte(`{"model":"test","max_tokens":10,"messages":[` + message + `]}`))
			require.NoError(t, err)
		})
	}
	for _, message := range []string{
		`{"role":"system","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}`,
		`{"role":"developer","content":[{"type":"input_audio","input_audio":{"data":"AA==","format":"wav"}}]}`,
		`{"role":"tool","tool_call_id":"call_1","content":[{"type":"file","file":{"file_id":"file_1"}}]}`,
		`{"role":"assistant","content":[{"type":"video_url","video_url":"https://example.test/a.mp4"}]}`,
		`{"role":"user","content":[{"type":"refusal","refusal":"no"}]}`,
		`{"role":"assistant","content":[{"type":"refusal","refusal":false}]}`,
		`{"role":"function","name":"lookup","content":[{"type":"text","text":"ok"}]}`,
		`{"role":"function","content":"ok"}`,
	} {
		t.Run("invalid "+message, func(t *testing.T) {
			_, err := strictChatRequest([]byte(`{"model":"test","max_tokens":10,"messages":[` + message + `]}`))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestChatAdmissionIntegerSeedAndTokenBias(t *testing.T) {
	for _, extra := range []string{`"seed":0,"logit_bias":{"42":0,"1":-100,"2":100}`, `"seed":-7,"logit_bias":null`, `"seed":0,"logit_bias":{"42":0.5,"1":-0.5}`, `"seed":null,"logit_bias":{}`} {
		t.Run(extra, func(t *testing.T) {
			got, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "chat", "application/json", []byte(`{"model":"test","max_tokens":10,"store":false,"messages":[{"role":"user","content":"hi"}],`+extra+`}`))
			require.NoError(t, err)
			assert.Contains(t, string(got.Canonical), `"store":false`)
		})
	}
	for _, extra := range []string{`"seed":1.5`, `"seed":"1"`, `"seed":true`, `"logit_bias":false`, `"logit_bias":{"42":false}`, `"logit_bias":{"42":101}`, `"logit_bias":{"42":-101}`, `"logit_bias":{"token":1}`} {
		t.Run(extra, func(t *testing.T) {
			_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "chat", "application/json", []byte(`{"model":"test","max_tokens":10,"messages":[{"role":"user","content":"hi"}],`+extra+`}`))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}
