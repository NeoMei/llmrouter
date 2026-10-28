package platformbilling

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesAdmissionClientToolLoop(t *testing.T) {
	// Rejecting array input or dropping opaque/zero fields breaks actual agent continuation.
	body := []byte(`{"model":"test","input":[{"role":"user","content":[{"type":"input_text","text":"run"}]},{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"opaque"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"Case\":0}","status":"completed"},{"type":"function_call_output","call_id":"call_1","output":"{\"Result\":false}"},{"type":"custom_tool_call","id":"ct_1","call_id":"call_2","name":"shell","input":"printf ok","status":"completed"},{"type":"custom_tool_call_output","call_id":"call_2","output":"ok"},{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"Case":{"type":"integer"},"case":{"type":"boolean"}},"additionalProperties":false},"strict":false},{"type":"custom","name":"shell","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}],"tool_choice":{"type":"custom","name":"shell"},"max_output_tokens":100,"max_tool_calls":0,"include":["reasoning.encrypted_content"],"reasoning":{"effort":"low","summary":"auto"},"text":{"format":{"type":"json_schema","name":"result","schema":{"type":"object","properties":{"Case":{"type":"boolean"}}},"strict":false},"verbosity":"low"},"previous_response_id":"resp_1","background":false,"parallel_tool_calls":false,"store":false,"temperature":0,"top_p":0}`)
	cfg := Config{MaxInputTokens: 16384, MaxOutputTokens: 100, MaxBodyBytes: 8192, NativeInputPolicies: map[string]map[string]NativeInputPolicy{"responses.create": {"test": {Version: "hard_context_v1", ProviderReference: "https://provider.example/context", ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "test", HardInputTokens: 16384, MaxBytes: 8192, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 10}}}}
	got, err := parseOperation(cfg, "responses", "application/json", body)
	require.NoError(t, err)
	assert.Equal(t, 100, got.Output)
	var canonical, original map[string]any
	require.NoError(t, common.UnmarshalUnique(got.Canonical, &canonical))
	require.NoError(t, common.UnmarshalUnique(body, &original))
	assert.Equal(t, original, canonical)
	assert.Contains(t, string(got.Canonical), `"Case":{"type":"integer"},"case":{"type":"boolean"}`)
	assert.Contains(t, string(got.Canonical), `"background":false`)
	assert.Contains(t, string(got.Canonical), `"max_tool_calls":0`)
	reordered := []byte(" \n" + string(got.Canonical))
	again, err := parseOperation(cfg, "responses", "application/json", reordered)
	require.NoError(t, err)
	assert.Equal(t, got.Canonical, again.Canonical)
}

func TestResponsesAdmissionSafeShapes(t *testing.T) {
	for _, extra := range []string{
		`"input":[{"role":"developer","content":"You help"},{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA==","detail":"auto"},{"type":"input_file","file_id":"file_1"}]}],"tools":[],"tool_choice":"auto"`,
		`"input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"ok"}]}],"previous_response_id":"resp_1"`,
		`"input":[{"type":"item_reference","id":"msg_1"}],"previous_response_id":"resp_1","truncation":"auto"`,
		`"input":"hi","background":false,"max_tool_calls":64,"stream":true,"stream_options":{"include_usage":true,"include_obfuscation":false},"text":{"format":{"type":"text"}}`,
		`"input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"}`,
	} {
		t.Run(extra, func(t *testing.T) {
			_, err := strictResponsesRequest([]byte(`{"model":"test","max_output_tokens":10,` + extra + `}`))
			require.NoError(t, err)
		})
	}
}

func TestResponsesAdmissionRejectsMalformedUnions(t *testing.T) {
	for name, extra := range map[string]string{
		"metadata scalar":              `"input":"hi","metadata":3`,
		"input object":                 `"input":{"role":"user","content":"hi"}`,
		"explicit null message tag":    `"input":[{"type":null,"role":"user","content":"hi"}]`,
		"multiword protocol key":       `"input":"hi","tools":[{"type":"function","name":"lookup","name description":true}]`,
		"multiword detail enum":        `"input":[{"role":"user","content":[{"type":"input_image","image_url":"x","detail":"auto low"}]}]`,
		"input scalar":                 `"input":[1]`,
		"message role":                 `"input":[{"role":false,"content":"hi"}]`,
		"message missing content":      `"input":[{"role":"user"}]`,
		"content scalar":               `"input":[{"role":"user","content":42}]`,
		"unknown item":                 `"input":[{"type":"web_search_call","id":"ws_1"}]`,
		"content conflicting branches": `"input":[{"role":"user","content":[{"type":"input_text","text":"hi","image_url":"bad"}]}]`,
		"image missing source":         `"input":[{"role":"user","content":[{"type":"input_image","detail":"auto"}]}]`,
		"function wrong arguments":     `"input":[{"type":"function_call","call_id":"a","name":"lookup","arguments":{"X":1}}]`,
		"function missing call id":     `"input":[{"type":"function_call","name":"lookup","arguments":"{}"}]`,
		"output wrong union":           `"input":[{"type":"function_call_output","call_id":"a","output":{"Result":1}}]`,
		"custom wrong input":           `"input":[{"type":"custom_tool_call","call_id":"a","name":"shell","input":false}]`,
		"reasoning bad summary":        `"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":0}]}]`,
		"reasoning unknown key":        `"input":"hi","reasoning":{"effort":"low","Budget":100}`,
		"reasoning wrong type":         `"input":"hi","reasoning":false`,
		"tools wrong type":             `"input":"hi","tools":{}`,
		"server tool":                  `"input":"hi","tools":[{"type":"web_search_preview"}]`,
		"unknown tool":                 `"input":"hi","tools":[{"type":"future_paid_tool"}]`,
		"bad function strict":          `"input":"hi","tools":[{"type":"function","name":"lookup","strict":"false"}]`,
		"schema scalar":                `"input":"hi","tools":[{"type":"function","name":"lookup","parameters":3}]`,
		"custom grammar malformed":     `"input":"hi","tools":[{"type":"custom","name":"shell","format":{"type":"grammar","syntax":false,"definition":"x"}}]`,
		"named choice undeclared":      `"input":"hi","tool_choice":{"type":"function","name":"missing"}`,
		"choice malformed":             `"input":"hi","tool_choice":{"type":"function","name":"lookup","Name":"other"},"tools":[{"type":"function","name":"lookup"}]`,
		"format malformed":             `"input":"hi","text":{"format":{"type":"json_schema","name":"result","schema":false}}`,
		"background async":             `"input":"hi","background":true`,
		"background null":              `"input":"hi","background":null`,
		"boolean wrong type":           `"input":"hi","parallel_tool_calls":"false"`,
		"previous handle wrong type":   `"input":"hi","previous_response_id":42`,
		"include wrong member":         `"input":"hi","include":[42]`,
		"max tool huge":                `"input":"hi","max_tool_calls":18446744073709551615`,
		"max tool excessive":           `"input":"hi","max_tool_calls":2147483648`,
		"duplicate inside schema":      `"input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"X":0,"X":1}}]`,
		"cased protocol key":           `"input":[{"role":"user","content":[{"type":"input_text","Text":"hi"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "responses", "application/json", []byte(`{"model":"test","max_output_tokens":10,`+extra+`}`))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestResponsesAdmissionBoundsBeforeFinancialIO(t *testing.T) {
	// A malformed request must never get far enough to contact model/rate/reserve APIs.
	a, db, _ := meteredFixture(t, "speech")
	var financialRequests atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		financialRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(trap.Close)
	config := a.config
	config.Origin = trap.URL
	var err error
	a.client, err = newClient(config)
	require.NoError(t, err)
	for _, body := range []string{
		`{"model":"test","input":"hi","max_output_tokens":18446744073709551615}`,
		`{"model":"test","input":"hi","max_output_tokens":0}`,
		`{"model":"test","input":"hi","max_output_tokens":101}`,
		`{"model":"test","input":"hi","max_output_tokens":10,"tools":[{"type":"file_search"}]}`,
		`{"model":"test","input":"hi","max_output_tokens":10,"background":true}`,
		`{"model":"test","input":"` + strings.Repeat("x", 501) + `","max_output_tokens":10}`,
	} {
		_, err := a.AdmitOperation(context.Background(), "uh_model_token", "task", "call", "responses", "application/json", []byte(body))
		require.ErrorIs(t, err, ErrInvalid)
	}
	var calls int64
	require.NoError(t, db.Model(&Call{}).Count(&calls).Error)
	assert.Zero(t, calls)
	assert.Zero(t, financialRequests.Load())
}

func TestResponsesAdmissionCatalogIsNotConcurrentCallState(t *testing.T) {
	// A list of available tools does not allocate concurrent stream call state.
	tools := make([]map[string]any, 65)
	for i := range tools {
		tools[i] = map[string]any{"type": "function", "name": fmt.Sprintf("tool_%d", i)}
	}
	body, err := common.Marshal(map[string]any{"model": "test", "max_output_tokens": 10, "input": "hi", "tools": tools})
	require.NoError(t, err)
	_, err = parseOperation(Config{MaxInputTokens: 32768, MaxOutputTokens: 100}, "responses", "application/json", body)
	require.NoError(t, err)
}

func TestResponsesAdmissionPreservesAssistantPhase(t *testing.T) {
	for _, phase := range []string{`"final_answer"`, `"commentary"`, `null`} {
		t.Run(phase, func(t *testing.T) {
			body := []byte(`{"model":"test","max_output_tokens":10,"store":false,"input":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":` + phase + `,"content":[{"type":"output_text","text":"done","annotations":[]}]}]}`)
			got, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "responses", "application/json", body)
			require.NoError(t, err)
			var original, canonical map[string]any
			require.NoError(t, common.UnmarshalUnique(body, &original))
			require.NoError(t, common.UnmarshalUnique(got.Canonical, &canonical))
			assert.Equal(t, original, canonical)
		})
	}
	for _, item := range []string{
		`{"type":"message","role":"assistant","content":"hi","phase":"final"}`,
		`{"type":"message","role":"assistant","content":"hi","phase":0}`,
		`{"type":"message","role":"assistant","content":"hi","phase":{}}`,
		`{"role":"user","content":"hi","phase":"commentary"}`,
		`{"role":"user","content":"hi","phase":null}`,
	} {
		_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "responses", "application/json", []byte(`{"model":"test","max_output_tokens":10,"input":[`+item+`]}`))
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestResponsesAdmissionPreservesNullableEncryptedReasoning(t *testing.T) {
	for _, encrypted := range []string{`null`, `"opaque"`} {
		body := []byte(`{"model":"test","max_output_tokens":10,"input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":` + encrypted + `}]}`)
		request, err := strictResponsesRequest(body)
		canonical, marshalErr := common.Marshal(request)
		require.NoError(t, marshalErr)
		got := operationRequest{Canonical: canonical}
		require.NoError(t, err)
		assert.Contains(t, string(got.Canonical), `"encrypted_content":`+encrypted)
	}
	for _, encrypted := range []string{`0`, `{}`, `false`, `[]`} {
		_, err := parseOperation(Config{MaxInputTokens: 8192, MaxOutputTokens: 100}, "responses", "application/json", []byte(`{"model":"test","max_output_tokens":10,"input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":`+encrypted+`}]}`))
		require.ErrorIs(t, err, ErrInvalid)
	}
}
