package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResponsesStateExpandOnChatWire verifies that a request carrying a
// previous_response_id is expanded into the full conversation when the selected
// wire is chat conversion, and that the stored turn is prepended.
func TestResponsesStateExpandOnChatWire(t *testing.T) {
	store := responsesstate.NewMemory(time.Hour, 1<<20)
	scope := responsesstate.Scope{
		TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3,
		TargetID: 4, ChannelID: 5, ClientTaskID: "task-a", Model: "model-a",
	}
	require.NoError(t, store.Save(context.Background(), responsesstate.Record{
		Version:    responsesstate.CurrentVersion,
		Scope:      scope,
		ResponseID: "resp_prev",
		Input:      json.RawMessage(`[{"type":"message","role":"user","content":"first"}]`),
		Output:     json.RawMessage(`[{"type":"message","role":"assistant","content":"answer"}]`),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	req := json.RawMessage(`{"model":"model-a","previous_response_id":"resp_prev","input":[{"type":"message","role":"user","content":"second"}]}`)
	expanded, err := responsesstate.Expand(context.Background(), store, scope, req)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(expanded.Request, &payload))
	require.Empty(t, payload["previous_response_id"], "previous_response_id must be removed after expansion")

	var input []any
	require.NoError(t, common.Unmarshal(json.RawMessage(mustJSON(t, payload["input"])), &input))
	require.Len(t, input, 3, "expected first turn (user+assistant) plus second user message")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestResponsesHelperDefaultsOmittedStoreToFalse(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{Model: "model-a", Input: json.RawMessage(`[]`)}
	normalizeResponsesStore(request)
	assert.JSONEq(t, `false`, string(request.Store))
}

func TestResponsesHelperPreservesExplicitStoreTrue(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{Model: "model-a", Store: json.RawMessage(`true`)}
	normalizeResponsesStore(request)
	assert.JSONEq(t, `true`, string(request.Store))
}

func TestPrepareResponsesContinuityUsesBoundedClientTaskID(t *testing.T) {
	store := responsesstate.NewMemory(time.Hour, 1<<20)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4,
		ResolvedChannelId: 5, ResolvedModelName: "model-a",
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"},
	}
	request := &dto.OpenAIResponsesRequest{
		Model: "model-a", Input: json.RawMessage(`[]`),
		ClientMetadata: json.RawMessage(`{"task_id":" task.valid:1 "}`),
	}

	_, err := prepareResponsesContinuity(c, info, store, request)
	require.NoError(t, err)
	raw, exists := c.Get(string(appconstant.ContextKeyResponsesContinuity))
	require.True(t, exists)
	continuity, ok := raw.(responsesstate.ContinuityContext)
	require.True(t, ok)
	assert.Equal(t, "task.valid:1", continuity.Scope.ClientTaskID)
}

func TestPrepareResponsesContinuityRejectsInvalidClientTaskID(t *testing.T) {
	store := responsesstate.NewMemory(time.Hour, 1<<20)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4,
		ResolvedChannelId: 5, ResolvedModelName: "model-a",
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"},
	}
	request := &dto.OpenAIResponsesRequest{
		Model: "model-a", Input: json.RawMessage(`[]`),
		ClientMetadata: json.RawMessage(`{"task_id":"bad task"}`),
	}

	_, err := prepareResponsesContinuity(c, info, store, request)
	require.Error(t, err)
	assert.ErrorIs(t, err, responsesstate.ErrInvalidClientTaskID)
	raw, _ := c.Get(string(appconstant.ContextKeyResponsesContinuity))
	assert.Nil(t, raw)
}

func TestResponsesContinuityErrorMapsScopeFailuresToConflict(t *testing.T) {
	for _, stateErr := range []error{responsesstate.ErrLegacyScope, responsesstate.ErrScopeMismatch} {
		apiErr := responsesContinuityAPIError(stateErr)
		require.NotNil(t, apiErr)
		assert.Equal(t, http.StatusConflict, apiErr.StatusCode)
		assert.Equal(t, types.ErrorCodeResponsesStateScopeInvalid, apiErr.GetErrorCode())
		assert.True(t, types.IsSkipRetryError(apiErr))
	}
}

func TestPrepareResponsesContinuityForAttemptRejectsInvalidTaskBeforeRelay(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		Request: &dto.OpenAIResponsesRequest{
			Model:          "model-a",
			Input:          json.RawMessage(`[]`),
			ClientMetadata: json.RawMessage(`{"task_id":"not allowed"}`),
		},
		UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4,
		ResolvedChannelId: 5, ResolvedModelName: "model-a",
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"},
	}

	apiErr := PrepareResponsesContinuityForAttempt(c, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
}

func TestResponsesHelperReturnsInvalidClientTaskID(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		Request: &dto.OpenAIResponsesRequest{
			Model: "model-a", Input: json.RawMessage(`[]`),
			ClientMetadata: json.RawMessage(`{"task_id":"not allowed"}`),
		},
		UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4,
		ResolvedChannelId: 5, ResolvedModelName: "model-a", SelectedWire: string(modelcatalog.WireResponses),
	}

	apiErr := ResponsesHelper(c, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
}

func TestPrepareNativeResponsesContinuationAdoptsStoredTaskID(t *testing.T) {
	store := responsesstate.NewMemory(time.Hour, 1<<20)
	scope := responsesstate.Scope{
		TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3,
		TargetID: 4, ChannelID: 5, ClientTaskID: "resp_parent", Model: "model-a",
	}
	require.NoError(t, store.Save(context.Background(), responsesstate.Record{
		Version: responsesstate.CurrentVersion, Scope: scope, ResponseID: "resp_parent",
		Input: json.RawMessage(`[]`), Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(time.Hour),
	}))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4,
		ResolvedChannelId: 5, ResolvedModelName: "model-a", SelectedWire: string(modelcatalog.WireResponses),
		ResponsesViaChat: true, // stale from a prior attempt; explicit wire is authoritative
		ChannelMeta:      &relaycommon.ChannelMeta{UpstreamModelName: "model-a"},
	}
	request := &dto.OpenAIResponsesRequest{Model: "model-a", PreviousResponseID: "resp_parent", Input: json.RawMessage(`[]`)}

	prepared, err := prepareResponsesContinuity(c, info, store, request)
	require.NoError(t, err)
	assert.Equal(t, "resp_parent", prepared.PreviousResponseID, "native Responses keeps the upstream parent reference")
	raw, exists := c.Get(string(appconstant.ContextKeyResponsesContinuity))
	require.True(t, exists)
	continuity, ok := raw.(responsesstate.ContinuityContext)
	require.True(t, ok)
	assert.Equal(t, "resp_parent", continuity.Scope.ClientTaskID)
}

func TestResponsesContinuityHandlerToPersistenceKeepsLinearChild(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	scope := responsesstate.Scope{
		TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3,
		TargetID: 4, ChannelID: 5, ClientTaskID: "task-a", Model: "model-a",
	}
	require.NoError(t, store.Save(context.Background(), responsesstate.Record{
		Version:    responsesstate.CurrentVersion,
		Scope:      scope,
		ResponseID: "resp_parent",
		Input:      json.RawMessage(`[{"type":"message","role":"user","content":"first"}]`),
		Output:     json.RawMessage(`[{"type":"message","role":"assistant","content":"answer"}]`),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId:            11,
		TokenId:           7,
		ModelRouteId:      3,
		ModelTargetId:     4,
		ResolvedChannelId: 5,
		ResolvedModelName: "model-a",
		ResponsesViaChat:  true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "model-a",
		},
	}
	request := &dto.OpenAIResponsesRequest{
		Model:              "model-a",
		PreviousResponseID: "resp_parent",
		Input:              json.RawMessage(`[{"type":"message","role":"user","content":"second"}]`),
	}
	prepared, err := prepareResponsesContinuity(c, info, store, request)
	require.NoError(t, err)
	assert.Empty(t, prepared.PreviousResponseID)
	assert.Contains(t, string(prepared.Input), "first")
	assert.Contains(t, string(prepared.Input), "second")

	rawContinuity, exists := c.Get(string(appconstant.ContextKeyResponsesContinuity))
	require.True(t, exists)
	continuity, ok := rawContinuity.(responsesstate.ContinuityContext)
	require.True(t, ok)
	assert.Equal(t, scope, continuity.Scope)
	assert.Equal(t, "resp_parent", continuity.ParentResponseID)
	assert.JSONEq(t, string(request.Input), string(continuity.CurrentInput))
	assert.NotContains(t, string(continuity.CurrentInput), "first")

	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_child",
			"status":"completed",
			"model":"model-a",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"child-answer"}]}],
			"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}
		}`)),
	}
	_, apiErr := openai.OaiResponsesHandler(c, info, upstream)
	require.Nil(t, apiErr)

	child, err := store.Load(context.Background(), scope, "resp_child")
	require.NoError(t, err)
	assert.Equal(t, "resp_parent", child.ParentResponseID)
	assert.Equal(t, "task-a", child.Scope.ClientTaskID)
	assert.JSONEq(t, string(request.Input), string(child.Input))
	assert.NotContains(t, string(child.Input), "first")

	grandchild, err := responsesstate.Expand(context.Background(), store, scope, json.RawMessage(`{"previous_response_id":"resp_child","input":[{"type":"message","role":"user","content":"third"}]}`))
	require.NoError(t, err)
	var payload struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, common.Unmarshal(grandchild.Request, &payload))
	require.Len(t, payload.Input, 5)
	assert.Contains(t, string(payload.Input[0]), "first")
	assert.Contains(t, string(payload.Input[2]), "second")
	assert.Contains(t, string(payload.Input[4]), "third")
}

func TestPrepareResponsesContinuityClearsStaleContextAfterRetryFailure(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	scope := responsesstate.Scope{
		TenantID: 21, UserID: 21, TokenID: 8, RouteID: 6,
		TargetID: 7, ChannelID: 9, ClientTaskID: "task-retry", Model: "model-retry",
	}
	require.NoError(t, store.Save(context.Background(), responsesstate.Record{
		Version:    responsesstate.CurrentVersion,
		Scope:      scope,
		ResponseID: "resp_attempt_a_parent",
		Input:      json.RawMessage(`[{"type":"message","role":"user","content":"a"}]`),
		Output:     json.RawMessage(`[]`),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId:            scope.UserID,
		TokenId:           scope.TokenID,
		ModelRouteId:      scope.RouteID,
		ModelTargetId:     scope.TargetID,
		ResolvedChannelId: scope.ChannelID,
		ResolvedModelName: scope.Model,
		ResponsesViaChat:  true,
		ChannelMeta:       &relaycommon.ChannelMeta{UpstreamModelName: scope.Model},
	}
	attemptA := &dto.OpenAIResponsesRequest{
		Model:              scope.Model,
		PreviousResponseID: "resp_attempt_a_parent",
		Input:              json.RawMessage(`[{"type":"message","role":"user","content":"attempt-a"}]`),
	}
	_, err = prepareResponsesContinuity(c, info, store, attemptA)
	require.NoError(t, err)
	raw, exists := c.Get(string(appconstant.ContextKeyResponsesContinuity))
	require.True(t, exists)
	_, ok := raw.(responsesstate.ContinuityContext)
	require.True(t, ok)

	attemptB := &dto.OpenAIResponsesRequest{
		Model:              scope.Model,
		PreviousResponseID: "resp_missing_for_attempt_b",
		Input:              json.RawMessage(`[{"type":"message","role":"user","content":"attempt-b"}]`),
	}
	_, err = prepareResponsesContinuity(c, info, store, attemptB)
	require.ErrorIs(t, err, responsesstate.ErrNotFound)
	raw, _ = c.Get(string(appconstant.ContextKeyResponsesContinuity))
	assert.Nil(t, raw, "a failed retry attempt must clear the previous attempt's continuity context")

	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_attempt_b",
			"status":"completed",
			"model":"model-retry",
			"output":[],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)),
	}
	_, apiErr := openai.OaiResponsesHandler(c, info, upstream)
	require.Nil(t, apiErr)
	_, err = store.Load(context.Background(), scope, "resp_attempt_b")
	require.ErrorIs(t, err, responsesstate.ErrNotFound, "attempt B must not persist using attempt A's stale continuity context")
}
