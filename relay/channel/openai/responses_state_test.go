package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type notifyingResponseWriter struct {
	gin.ResponseWriter
	once  sync.Once
	wrote chan struct{}
	match string
}

func (w *notifyingResponseWriter) Write(data []byte) (int, error) {
	match := w.match
	if match == "" {
		match = "response.completed"
	}
	if strings.Contains(string(data), match) {
		w.once.Do(func() { close(w.wrote) })
	}
	return w.ResponseWriter.Write(data)
}

func TestPersistResponsesTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	store := responsesstate.NewMemory(time.Hour, 1<<20)
	info := &relaycommon.RelayInfo{
		UserId:            11,
		TokenId:           7,
		ModelRouteId:      3,
		ModelTargetId:     4,
		ResolvedChannelId: 5,
		ResolvedModelName: "model-a",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "model-a",
		},
	}

	require.NoError(t, responsesstate.SaveTurn(
		c.Request.Context(),
		store,
		responsesstate.Record{
			Version: responsesstate.CurrentVersion,
			Scope: responsesstate.Scope{
				TenantID: info.UserId, UserID: info.UserId, TokenID: info.TokenId, RouteID: info.ModelRouteId,
				TargetID: info.ModelTargetId, ChannelID: info.ResolvedChannelId, Model: info.ResolvedModelNameOrOrigin(),
			},
			ResponseID: "resp_1",
			Input:      json.RawMessage(`[{"type":"message","role":"user","content":"hello"}]`),
			Output:     json.RawMessage(`[{"type":"message","role":"assistant","content":"answer"}]`),
		},
	))

	expected := responsesstate.Scope{
		TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3,
		TargetID: 4, ChannelID: 5, Model: "model-a",
	}
	record, err := store.Load(c.Request.Context(), expected, "resp_1")
	require.NoError(t, err)
	require.Equal(t, "model-a", record.Scope.Model)
	require.Contains(t, string(record.Input), "hello")
	require.Contains(t, string(record.Output), "answer")
}

func TestPersistResponsesTurnRejectsIneligibleResponses(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a"}

	cases := []*dto.OpenAIResponsesResponse{
		{ID: "resp_incomplete", Status: json.RawMessage(`"incomplete"`)},
		{ID: "resp_error", Status: json.RawMessage(`"completed"`), Error: map[string]any{"message": "failed"}},
		{ID: "resp_details", Status: json.RawMessage(`"completed"`), IncompleteDetails: &dto.IncompleteDetails{Reason: "max_output_tokens"}},
		{ID: "resp_refusal", Status: json.RawMessage(`"completed"`), Output: []dto.ResponsesOutput{{Type: "message", Content: []dto.ResponsesOutputContent{{Type: "refusal"}}}}},
	}
	for _, response := range cases {
		persistResponsesTurn(c, info, response)
		_, loadErr := store.Load(context.Background(), scope, response.ID)
		assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
	}
}

func TestResponsesStreamPersistsEligibleCompletedTerminal(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, ParentResponseID: "resp_parent", CurrentInput: json.RawMessage(`[{"type":"message","role":"user","content":"current"}]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_complete\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n"))}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	record, loadErr := store.Load(context.Background(), scope, "resp_complete")
	require.NoError(t, loadErr)
	assert.Equal(t, "resp_parent", record.ParentResponseID)
	assert.Contains(t, string(record.Input), "current")
}

func TestResponsesStreamCancelledAfterCompletedEventDoesNotPersist(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	wrote := make(chan struct{})
	c.Writer = &notifyingResponseWriter{ResponseWriter: c.Writer, wrote: wrote, match: "response.output_text.delta"}
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestContext)
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}

	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: reader}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cancelled\",\"status\":\"completed\",\"output\":[]}}\n\n")
		<-requestContext.Done()
		_ = writer.Close()
	}()
	handlerDone := make(chan struct{})
	go func() {
		_, _ = OaiResponsesStreamHandler(c, info, resp)
		close(handlerDone)
	}()

	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream output")
	}
	cancel()
	<-handlerDone
	<-writerDone
	_, loadErr := store.Load(context.Background(), scope, "resp_cancelled")
	assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
}

func TestResponsesStreamDoesNotPersistWhenFinalTerminalIsMalformed(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	body := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_not_terminal","status":"completed","output":[]}}`,
		``,
		`data: {"type":"response.completed"}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
	assert.NotContains(t, recorder.Body.String(), "response.completed")
	_, loadErr := store.Load(context.Background(), scope, "resp_not_terminal")
	assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
}

func TestChatToResponsesNonStreamPersistsEligibleConvertedResponse(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "state-chat-nonstream")
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[{"type":"message","role":"user","content":"question"}]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"id":"upstream-chat","object":"chat.completion","created":1,"model":"model-a",
		"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"answer"}}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`))}

	_, apiErr := OaiChatToResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	record, loadErr := store.Load(context.Background(), scope, "chatcmpl-state-chat-nonstream")
	require.NoError(t, loadErr)
	assert.Contains(t, string(record.Input), "question")
	assert.Contains(t, string(record.Output), "answer")

	expansion, expandErr := responsesstate.Expand(context.Background(), store, record.Scope, json.RawMessage(`{"previous_response_id":"chatcmpl-state-chat-nonstream","input":[{"type":"message","role":"user","content":"follow-up"}]}`))
	require.NoError(t, expandErr)
	assert.Contains(t, string(expansion.Request), "answer")
	assert.Contains(t, string(expansion.Request), "follow-up")
}

func TestChatToResponsesStreamPersistsGeneratedCompletedTerminalExactlyOnce(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "state-chat-stream")
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[{"type":"message","role":"user","content":"stream-question"}]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	body := strings.Join([]string{
		`data: {"id":"upstream-chat","object":"chat.completion.chunk","model":"model-a","choices":[{"index":0,"delta":{"content":"stream-answer"},"finish_reason":null}]}`,
		`data: {"id":"upstream-chat","object":"chat.completion.chunk","model":"model-a","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

	_, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	record, loadErr := store.Load(context.Background(), scope, "chatcmpl-state-chat-stream")
	require.NoError(t, loadErr)
	assert.Contains(t, string(record.Output), "stream-answer")
	assert.Equal(t, "chatcmpl-state-chat-stream", record.Scope.ClientTaskID)
}

func TestNativeResponsesStreamTerminalStateIsStickyAndOrdered(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	completed := `data: {"type":"response.completed","response":{"id":"resp_sequence","status":"completed","output":[]}}`
	tests := []struct {
		name   string
		events []string
	}{
		{name: "ordinary after completed", events: []string{completed, `data: {"type":"response.output_text.delta","delta":"late"}`}},
		{name: "duplicate completed", events: []string{completed, completed}},
		{name: "failed after completed", events: []string{completed, `data: {"type":"response.failed","response":{"id":"resp_sequence","status":"failed"}}`}},
		{name: "incomplete after completed", events: []string{completed, `data: {"type":"response.incomplete","response":{"id":"resp_sequence","status":"incomplete"}}`}},
		{name: "error after completed", events: []string{completed, `data: {"type":"error"}`}},
		{name: "error before completed", events: []string{`data: {"type":"response.failed","response":{"id":"resp_sequence","status":"failed"}}`, completed}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RESPONSES_STATE_ENABLED", "1")
			responsesstate.ResetForTest()
			t.Cleanup(responsesstate.ResetForTest)
			store, err := responsesstate.Default()
			require.NoError(t, err)

			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
			c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
			info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
			body := strings.Join(append(append([]string{}, tt.events...), "data: [DONE]", ""), "\n\n")
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

			_, apiErr := OaiResponsesStreamHandler(c, info, resp)
			require.NotNil(t, apiErr)
			assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
			_, loadErr := store.Load(context.Background(), scope, "resp_sequence")
			assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
		})
	}
}

func TestChatToResponsesStreamCancellationDoesNotPersistGeneratedTerminal(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	wrote := make(chan struct{})
	c.Writer = &notifyingResponseWriter{ResponseWriter: c.Writer, wrote: wrote, match: "response.output_text.delta"}
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestContext)
	c.Set(common.RequestIdKey, "state-chat-cancel")
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(appconstant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}

	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: reader}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = io.WriteString(writer, "data: {\"id\":\"upstream-chat\",\"object\":\"chat.completion.chunk\",\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		<-requestContext.Done()
		_ = writer.Close()
	}()
	handlerDone := make(chan struct{})
	go func() {
		_, _ = OaiChatToResponsesStreamHandler(c, info, resp)
		close(handlerDone)
	}()

	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for converted stream output")
	}
	cancel()
	<-handlerDone
	<-writerDone
	_, loadErr := store.Load(context.Background(), scope, "chatcmpl-state-chat-cancel")
	assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
}
