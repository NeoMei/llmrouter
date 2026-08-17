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

	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type notifyingResponseWriter struct {
	gin.ResponseWriter
	once  sync.Once
	wrote chan struct{}
}

func (w *notifyingResponseWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "response.completed") {
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
	c.Writer = &notifyingResponseWriter{ResponseWriter: c.Writer, wrote: wrote}
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
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cancelled\",\"status\":\"completed\",\"output\":[]}}\n\n")
		<-requestContext.Done()
		_ = writer.Close()
	}()
	handlerDone := make(chan struct{})
	go func() {
		_, _ = OaiResponsesStreamHandler(c, info, resp)
		close(handlerDone)
	}()

	<-wrote
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
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
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
	require.Nil(t, apiErr)
	_, loadErr := store.Load(context.Background(), scope, "resp_not_terminal")
	assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
}
