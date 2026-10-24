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
	"github.com/QuantumNous/new-api/relay/responsesprobe"
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
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a","input":[],"client_metadata":{"task_id":"not allowed"}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	defer common.CleanupBodyStorage(c)
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
	assert.ErrorIs(t, apiErr, responsesstate.ErrInvalidClientTaskID)
}

func TestResponsesHelperReturnsInvalidClientTaskID(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a","input":[],"client_metadata":{"task_id":"not allowed"}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	defer common.CleanupBodyStorage(c)
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
	assert.ErrorIs(t, apiErr, responsesstate.ErrInvalidClientTaskID)
}

func TestResponsesHelperRejectsDuplicateJSONBeforeNativePassthrough(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "0")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"authorized","model":"other","input":[]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	defer common.CleanupBodyStorage(c)
	info := &relaycommon.RelayInfo{
		RelayMode:    relayconstant.RelayModeResponses,
		RelayFormat:  types.RelayFormatOpenAIResponses,
		Request:      &dto.OpenAIResponsesRequest{Model: "other", Input: json.RawMessage(`[]`)},
		SelectedWire: string(modelcatalog.WireResponses),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelSetting: dto.ChannelSettings{PassThroughBodyEnabled: true},
		},
	}

	apiErr := ResponsesHelper(c, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	var duplicateErr *common.DuplicateJSONKeyError
	assert.ErrorAs(t, apiErr, &duplicateErr)
	assert.Zero(t, info.UpstreamRequestBodySize)
}

func TestPrepareResponsesContinuationAlwaysExpandsBeforeTransportSelection(t *testing.T) {
	store := responsesstate.NewMemory(time.Hour, 1<<20)
	scope := responsesstate.Scope{
		TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3,
		TargetID: 4, ChannelID: 5, ClientTaskID: "resp_parent", Model: "model-a",
	}
	require.NoError(t, store.Save(context.Background(), responsesstate.Record{
		Version: responsesstate.CurrentVersion, Scope: scope, ResponseID: "resp_parent",
		Input: json.RawMessage(`[{"type":"message","role":"user","content":"resp_parent-input"}]`), Output: json.RawMessage(`[]`), ExpiresAt: time.Now().Add(time.Hour),
	}))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for _, tc := range []struct {
		name             string
		wire             string
		responsesViaChat bool
	}{
		{name: "native responses", wire: string(modelcatalog.WireResponses)},
		{name: "moonshot chat compatibility", wire: string(modelcatalog.WireOpenAIChat), responsesViaChat: true},
		{name: "zhipu chat compatibility", responsesViaChat: true},
		{name: "probed chat fallback", wire: string(modelcatalog.WireOpenAIChat)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4,
				ResolvedChannelId: 5, ResolvedModelName: "model-a", SelectedWire: tc.wire,
				ResponsesViaChat: tc.responsesViaChat,
				ChannelMeta:      &relaycommon.ChannelMeta{UpstreamModelName: "model-a"},
			}
			request := &dto.OpenAIResponsesRequest{Model: "model-a", PreviousResponseID: "resp_parent", Input: json.RawMessage(`[]`)}

			prepared, err := prepareResponsesContinuity(c, info, store, request)
			require.NoError(t, err)
			assert.Empty(t, prepared.PreviousResponseID)
			assert.Contains(t, string(prepared.Input), "resp_parent-input")
			if tc.responsesViaChat || tc.wire == string(modelcatalog.WireOpenAIChat) {
				converted, convertErr := responsesprobe.ConvertResponsesRequestToChat(*prepared)
				require.NoError(t, convertErr)
				convertedJSON, marshalErr := common.Marshal(converted)
				require.NoError(t, marshalErr)
				assert.NotContains(t, string(convertedJSON), "previous_response_id")
				assert.Contains(t, string(convertedJSON), "resp_parent-input")
			}
			raw, exists := c.Get(string(appconstant.ContextKeyResponsesContinuity))
			require.True(t, exists)
			continuity, ok := raw.(responsesstate.ContinuityContext)
			require.True(t, ok)
			assert.Equal(t, "resp_parent", continuity.Scope.ClientTaskID)
		})
	}
}

func TestNewResponsesPassthroughBodyUsesNormalizedPreparedRequest(t *testing.T) {
	for _, tt := range []struct {
		name      string
		store     json.RawMessage
		wantStore json.RawMessage
	}{
		{name: "omitted becomes false", wantStore: json.RawMessage(`false`)},
		{name: "explicit true", store: json.RawMessage(`true`), wantStore: json.RawMessage(`true`)},
		{name: "explicit null", store: json.RawMessage(`null`), wantStore: json.RawMessage(`null`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rawRequest := `{
				"model":"legacy-model-alias",
				"previous_response_id":"resp_parent",
				"input":[{"type":"message","role":"user","content":"unexpanded"}],
				"background":true,
				"vendor_extension":{"large":9007199254740993}`
			if len(tt.store) > 0 {
				rawRequest += `,"store":` + string(tt.store)
			}
			rawRequest += `}`
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(rawRequest))
			c.Request.Header.Set("Content-Type", "application/json")
			defer common.CleanupBodyStorage(c)

			request := &dto.OpenAIResponsesRequest{
				Model: "model-a", Input: json.RawMessage(`[{"type":"message","role":"user","content":"materialized","large":9007199254740993}]`),
				Store: tt.store,
			}
			normalizeResponsesStore(request)
			body, _, getBody, closer, err := newResponsesPassthroughBody(c, request, true, false)
			require.NoError(t, err)
			defer closer.Close()
			first, err := io.ReadAll(body)
			require.NoError(t, err)
			var captured map[string]json.RawMessage
			require.NoError(t, common.Unmarshal(first, &captured))
			assert.JSONEq(t, `"legacy-model-alias"`, string(captured["model"]), "unresolved legacy passthrough retains its raw model")
			assert.Contains(t, captured, "store")
			assert.JSONEq(t, string(tt.wantStore), string(captured["store"]))
			assert.NotContains(t, string(first), "previous_response_id")
			assert.Contains(t, string(first), "materialized")
			assert.NotContains(t, string(first), "unexpanded")
			assert.JSONEq(t, `true`, string(captured["background"]))
			assert.JSONEq(t, `{"large":9007199254740993}`, string(captured["vendor_extension"]))
			assert.Equal(t, 2, strings.Count(string(first), "9007199254740993"),
				"vendor and materialized input integers must remain exact")
			replay, err := getBody()
			require.NoError(t, err)
			defer replay.Close()
			second, err := io.ReadAll(replay)
			require.NoError(t, err)
			assert.Equal(t, first, second)

			originalStorage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			originalReader, err := originalStorage.NewReader()
			require.NoError(t, err)
			defer originalReader.Close()
			originalAfterPatch, err := io.ReadAll(originalReader)
			require.NoError(t, err)
			assert.JSONEq(t, rawRequest, string(originalAfterPatch),
				"building the outbound replay body must not consume or rewrite BodyStorage")
		})
	}
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	return encoded
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

func TestLegacyDistributorContinuityBindsSelectedChannelIdentityAcrossTurns(t *testing.T) {
	store := responsesstate.NewMemory(time.Hour, 1<<20)
	gin.SetMode(gin.TestMode)
	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId: 11, TokenId: 7, ResolvedChannelId: 9, ResolvedModelName: "model-a",
		RelayMode: relayconstant.RelayModeResponses,
	}
	firstRequest := &dto.OpenAIResponsesRequest{Model: "model-a", Input: json.RawMessage(`[{"type":"message","role":"user","content":"first"}]`)}
	_, err := prepareResponsesContinuity(firstContext, info, store, firstRequest)
	require.NoError(t, err)
	raw, exists := firstContext.Get(string(appconstant.ContextKeyResponsesContinuity))
	require.True(t, exists)
	first := raw.(responsesstate.ContinuityContext)
	require.Equal(t, "legacy_distributor", first.Scope.RouteKind)
	require.Equal(t, 9, first.Scope.RouteID)
	require.Equal(t, 9, first.Scope.TargetID)
	require.Equal(t, 9, first.Scope.ChannelID)
	require.NoError(t, responsesstate.SaveTurn(context.Background(), store, responsesstate.Record{
		Version: responsesstate.CurrentVersion, Scope: first.Scope, ResponseID: "resp_legacy_first",
		Input: first.CurrentInput, Output: json.RawMessage(`[{"type":"message","role":"assistant","content":"answer"}]`),
	}))

	secondContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	secondContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	secondRequest := &dto.OpenAIResponsesRequest{Model: "model-a", PreviousResponseID: "resp_legacy_first", Input: json.RawMessage(`[{"type":"message","role":"user","content":"second"}]`)}
	prepared, err := prepareResponsesContinuity(secondContext, info, store, secondRequest)
	require.NoError(t, err)
	require.Empty(t, prepared.PreviousResponseID)
	require.Contains(t, string(prepared.Input), "first")
	require.Contains(t, string(prepared.Input), "answer")
	require.Contains(t, string(prepared.Input), "second")

	explicit := first.Scope
	explicit.RouteKind = ""
	_, err = store.Load(context.Background(), explicit, "resp_legacy_first")
	require.ErrorIs(t, err, responsesstate.ErrScopeMismatch)
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
