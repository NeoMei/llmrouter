package controller

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func configureRouteBillingRatios(t *testing.T) {
	t.Helper()
	previousRatios := ratio_setting.ModelRatio2JSONString()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	previousFreePreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"route-paid-a":1,"route-paid-b":2,"route-free":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previousFreePreConsume
	})
}

func configureAutoGroupRouteBilling(t *testing.T) {
	t.Helper()
	previousRatios := ratio_setting.ModelRatio2JSONString()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	previousFreePreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"route-paid":1}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"cheap":1,"higher":2,"lower":0.5,"free":0}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["cheap","higher","lower","free"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"cheap":"cheap","higher":"higher","lower":"lower","free":"free"}`))
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previousFreePreConsume
	})
}

func setupRelayRouteRuntimeDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	dsn := "file:relay_route_runtime_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Channel{}, &model.Ability{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	ratio_setting.InitRatioSettings()
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		sqlDB, sqlErr := db.DB()
		require.NoError(t, sqlErr)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func relayRouteResolutionContext(t *testing.T) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"coder","input":[]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 99)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	return c
}

func seedRelayRouteTargets(t *testing.T, db *gorm.DB, capacities ...int) {
	t.Helper()
	require.NoError(t, db.Create(&model.ClientModelRoute{
		Id: 1, PublicId: "public-coder", Client: "codex", Aliases: `["coder"]`, Enabled: true,
	}).Error)
	for i, capacity := range capacities {
		channelID := 101 + i
		targetID := 201 + i
		upstream := "upstream-" + string(rune('a'+i))
		require.NoError(t, db.Create(&model.Channel{
			Id: channelID, Name: upstream, Type: constant.ChannelTypeOpenAI, Key: "test-key",
			Models: upstream, Group: "default", Status: common.ChannelStatusEnabled,
		}).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group: "default", Model: upstream, ChannelId: channelID, Enabled: true, Weight: 1,
		}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{
			Id: targetID, RouteId: 1, ChannelId: channelID, UpstreamModel: upstream,
			BillingModel: "gpt-4o-mini", PreferredWires: `["responses"]`, Priority: i, Weight: 1,
			Enabled: true, ContextWindow: capacity, ContextFitRequired: true,
		}).Error)
	}
}

func seedGroupedRelayRouteTargets(t *testing.T, db *gorm.DB, groups ...string) {
	t.Helper()
	require.NoError(t, db.Create(&model.ClientModelRoute{
		Id: 1, PublicId: "public-coder", Client: "codex", Aliases: `["coder"]`, Enabled: true,
	}).Error)
	for i, group := range groups {
		channelID := 301 + i
		targetID := 401 + i
		upstream := "grouped-upstream-" + string(rune('a'+i))
		require.NoError(t, db.Create(&model.Channel{
			Id: channelID, Name: upstream, Type: constant.ChannelTypeOpenAI, Key: "test-key",
			Models: upstream, Group: group, Status: common.ChannelStatusEnabled,
		}).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group: group, Model: upstream, ChannelId: channelID, Enabled: true, Weight: 1,
		}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{
			Id: targetID, RouteId: 1, ChannelId: channelID, UpstreamModel: upstream,
			BillingModel: "route-paid", PreferredWires: `["responses"]`, Priority: 0, Weight: 1,
			Enabled: true, ContextWindow: 128000, ContextFitRequired: true,
		}).Error)
	}
}

func TestFilterRouteCandidatesSkipsKnownUndersizedTargets(t *testing.T) {
	candidates := []modelcatalog.ResolvedModel{
		{TargetID: 1, ChannelID: 11, ContextWindow: 8000, RequiresKnownContext: true},
		{TargetID: 2, ChannelID: 12, ContextWindow: 128000, RequiresKnownContext: true},
	}

	got, rejected := filterRouteCandidatesByContext(candidates, 12000)

	require.Len(t, got, 1)
	assert.Equal(t, 2, got[0].TargetID)
	require.Len(t, rejected, 1)
	assert.Equal(t, 1, rejected[0].TargetID)
}

func TestFilterRouteCandidatesRejectsRequiredUnknownCapacity(t *testing.T) {
	candidates := []modelcatalog.ResolvedModel{{TargetID: 1, ChannelID: 11, ContextWindow: 0, RequiresKnownContext: true}}

	got, rejected := filterRouteCandidatesByContext(candidates, 12000)

	assert.Empty(t, got)
	require.Len(t, rejected, 1)
	assert.Equal(t, 1, rejected[0].TargetID)
}

func TestFilterRouteCandidatesPreservesLegacyUnknownCapacity(t *testing.T) {
	candidates := []modelcatalog.ResolvedModel{{TargetID: 1, ChannelID: 11, ContextWindow: 0, RequiresKnownContext: false}}

	got, rejected := filterRouteCandidatesByContext(candidates, 12000)

	require.Len(t, got, 1)
	assert.Empty(t, rejected)
}

func TestResolveClientModelRouteSelectsLaterEligibleTarget(t *testing.T) {
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 8000, 128000)
	c := relayRouteResolutionContext(t)
	info := &relaycommon.RelayInfo{OriginModelName: "coder", RelayFormat: types.RelayFormatOpenAIResponses}

	apiErr := resolveClientModelRoute(c, info, 12000)

	require.Nil(t, apiErr)
	require.Len(t, info.RouteCandidates, 1)
	require.Len(t, info.RejectedRouteCandidates, 1)
	assert.Equal(t, 202, info.ModelTargetId)
	assert.Equal(t, 102, info.ResolvedChannelId)
	assert.Equal(t, "upstream-b", info.ResolvedModelName)
	assert.Equal(t, "gpt-4o-mini", info.BillingModelName)
	assert.Equal(t, "responses", info.SelectedWire)
}

func TestResolveClientModelRouteRejectsWhenNoCandidateFitsWithoutBilling(t *testing.T) {
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 8000)
	c := relayRouteResolutionContext(t)
	spy := &relayRouteBillingSpy{reserved: 500}
	info := &relaycommon.RelayInfo{
		OriginModelName: "coder", RelayFormat: types.RelayFormatOpenAIResponses, Billing: spy,
	}

	apiErr := resolveClientModelRoute(c, info, 12000)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeContextCapacityExceeded, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Empty(t, info.RouteCandidates)
	require.Len(t, info.RejectedRouteCandidates, 1)
	assert.Empty(t, spy.calls, "context admission must happen before any reservation")
}

func TestResolveClientModelRouteCountsAdmissionWhenAccountingCountIsDisabled(t *testing.T) {
	previousCountToken := constant.CountToken
	constant.CountToken = false
	t.Cleanup(func() { constant.CountToken = previousCountToken })
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 1)
	c := relayRouteResolutionContext(t)
	info := &relaycommon.RelayInfo{
		OriginModelName: "coder", RelayFormat: types.RelayFormatOpenAIResponses,
		Request: &dto.OpenAIResponsesRequest{Model: "coder", Input: []byte(`"this request is definitely longer than one token"`)},
	}

	apiErr := resolveClientModelRoute(c, info, 0)

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeContextCapacityExceeded, apiErr.GetErrorCode())
	assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens), "admission counting must not change accounting provenance")
}

func TestRecordRouteAdmissionDecisionTracesRejectedTargetIdentity(t *testing.T) {
	const requestID = "route-context-rejection"
	info := &relaycommon.RelayInfo{
		OriginModelName: "coder", PublicModelName: "public-coder", ResolvedModelName: "upstream-b", ModelRouteId: 1,
		RejectedRouteCandidates: []relaycommon.RouteCandidate{{
			RouteID: 1, TargetID: 201, ChannelID: 101, SelectedWire: "responses",
		}},
	}

	recordRouteAdmissionDecision(requestID, info)

	trace, ok := GetRouteDecision(requestID)
	require.True(t, ok)
	require.Len(t, trace.Attempts, 1)
	assert.Equal(t, 1, trace.Attempts[0].RouteID)
	assert.Equal(t, 201, trace.Attempts[0].TargetID)
	assert.Equal(t, 101, trace.Attempts[0].ChannelID)
	assert.Equal(t, "context_capacity_exceeded", trace.Attempts[0].RejectedReason)
}

func TestResolveClientModelRouteFailsClosedWhenConfiguredChannelIsUnavailable(t *testing.T) {
	db := setupRelayRouteRuntimeDB(t)
	require.NoError(t, db.Create(&model.ClientModelRoute{
		Id: 1, PublicId: "public-coder", Client: "codex", Aliases: `["coder"]`, Enabled: true,
	}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{
		Id: 201, RouteId: 1, ChannelId: 999, UpstreamModel: "missing", BillingModel: "gpt-4o-mini",
		PreferredWires: `["responses"]`, Weight: 1, Enabled: true, ContextWindow: 128000,
	}).Error)
	c := relayRouteResolutionContext(t)
	info := &relaycommon.RelayInfo{OriginModelName: "coder", RelayFormat: types.RelayFormatOpenAIResponses}

	apiErr := resolveClientModelRoute(c, info, 12000)

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeGetChannelFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Empty(t, info.RouteCandidates)
}

func TestResolveClientModelRouteRejectsUnsupportedWireBeforeBilling(t *testing.T) {
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 128000)
	require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 201).Update("preferred_wires", `["anthropic"]`).Error)
	c := relayRouteResolutionContext(t)
	spy := &relayRouteBillingSpy{reserved: 500}
	info := &relaycommon.RelayInfo{OriginModelName: "coder", RelayFormat: types.RelayFormatOpenAIResponses, Billing: spy}

	apiErr := resolveClientModelRoute(c, info, 12000)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeNoCapableChannel, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Empty(t, info.RouteCandidates)
	require.Len(t, info.RejectedRouteCandidates, 1)
	assert.Equal(t, "unsupported_wire", info.RejectedRouteCandidates[0].RejectedReason)
	assert.Empty(t, spy.calls)
}

func TestPrepareRouteCandidateRejectsContinuityScopeBeforeBillingOrSend(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 128000)
	store, err := responsesstate.Default()
	require.NoError(t, err)
	require.NoError(t, store.Save(t.Context(), responsesstate.Record{
		Version: responsesstate.CurrentVersion,
		Scope: responsesstate.Scope{
			TenantID: 99, UserID: 99, TokenID: 7, RouteID: 1,
			TargetID: 999, ChannelID: 999, ClientTaskID: "task-a", Model: "upstream-a",
		},
		ResponseID: "resp_wrong_scope", Input: []byte(`[]`), Output: []byte(`[]`), ExpiresAt: time.Now().Add(time.Hour),
	}))
	c := relayRouteResolutionContext(t)
	spy := &relayRouteBillingSpy{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses, RelayFormat: types.RelayFormatOpenAIResponses,
		Request:         &dto.OpenAIResponsesRequest{Model: "coder", PreviousResponseID: "resp_wrong_scope", Input: []byte(`[]`)},
		OriginModelName: "coder", UserId: 99, TokenId: 7, UserGroup: "default", UsingGroup: "default", Billing: spy,
		RouteCandidates: []relaycommon.RouteCandidate{{
			RouteID: 1, TargetID: 201, ChannelID: 101, UpstreamModel: "upstream-a", BillingModel: "gpt-4o-mini",
			SelectedWire: "responses", SelectedGroup: "default", ContextWindow: 128000,
		}},
	}

	apiErr := prepareRouteCandidateForAttempt(c, info, 0, 10, &types.TokenCountMeta{})

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeResponsesStateScopeInvalid, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusConflict, apiErr.StatusCode)
	assert.Empty(t, spy.calls, "scope validation must fail before quota reservation")
	assert.Zero(t, info.UpstreamRequestBodySize, "scope validation must fail before an upstream body is prepared or sent")
}

func TestPrepareRouteCandidateExpandsContinuityBeforeReserve(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	configureRouteBillingRatios(t)
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 128000)
	store, err := responsesstate.Default()
	require.NoError(t, err)
	scope := responsesstate.Scope{
		TenantID: 99, UserID: 99, TokenID: 7, RouteID: 1,
		TargetID: 201, ChannelID: 101, ClientTaskID: "task-a", Model: "upstream-a",
	}
	require.NoError(t, store.Save(t.Context(), responsesstate.Record{
		Version: responsesstate.CurrentVersion, Scope: scope, ResponseID: "resp_parent",
		Input: []byte(`[{"type":"message","role":"user","content":"parent"}]`), Output: []byte(`[]`), ExpiresAt: time.Now().Add(time.Hour),
	}))
	c := relayRouteResolutionContext(t)
	spy := &relayRouteBillingSpy{}
	spy.beforeReserve = func() {
		raw, exists := c.Get(string(constant.ContextKeyResponsesPreparedRequest))
		require.True(t, exists)
		prepared, ok := raw.(*dto.OpenAIResponsesRequest)
		require.True(t, ok)
		assert.Empty(t, prepared.PreviousResponseID)
		assert.Contains(t, string(prepared.Input), "parent")
	}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses, RelayFormat: types.RelayFormatOpenAIResponses,
		Request:         &dto.OpenAIResponsesRequest{Model: "coder", PreviousResponseID: "resp_parent", Input: []byte(`[]`)},
		OriginModelName: "coder", UserId: 99, TokenId: 7, UserGroup: "default", UsingGroup: "default", Billing: spy,
		RouteCandidates: []relaycommon.RouteCandidate{{
			RouteID: 1, TargetID: 201, ChannelID: 101, UpstreamModel: "upstream-a", BillingModel: "route-paid-a",
			SelectedWire: "openai_chat", SelectedGroup: "default", ContextWindow: 128000,
		}},
	}

	apiErr := prepareRouteCandidateForAttempt(c, info, 0, 10, &types.TokenCountMeta{})

	require.Nil(t, apiErr)
	require.Len(t, spy.calls, 1)
}

func TestPrepareRouteCandidateRejectsDuplicateResponsesJSONBeforeReserveOrSend(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "0")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 128000)

	for _, test := range []struct {
		name        string
		mode        int
		format      types.RelayFormat
		path        string
		request     dto.Request
		requestBody string
	}{
		{
			name: "responses", mode: relayconstant.RelayModeResponses, format: types.RelayFormatOpenAIResponses,
			path: "/v1/responses", request: &dto.OpenAIResponsesRequest{Model: "other", Input: []byte(`[]`)},
			requestBody: `{"model":"authorized","model":"other","input":[]}`,
		},
		{
			name: "responses compact", mode: relayconstant.RelayModeResponsesCompact, format: types.RelayFormatOpenAIResponsesCompaction,
			path: "/v1/responses/compact", request: &dto.OpenAIResponsesCompactionRequest{Model: "other", Input: []byte(`[]`)},
			requestBody: `{"model":"authorized","model":"other","input":[]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := relayRouteResolutionContext(t)
			c.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.requestBody))
			c.Request.Header.Set("Content-Type", "application/json")
			defer common.CleanupBodyStorage(c)
			spy := &relayRouteBillingSpy{}
			info := &relaycommon.RelayInfo{
				RelayMode: test.mode, RelayFormat: test.format, Request: test.request,
				OriginModelName: "other", UserId: 99, TokenId: 7, UserGroup: "default", UsingGroup: "default", Billing: spy,
				RouteCandidates: []relaycommon.RouteCandidate{{
					RouteID: 1, TargetID: 201, ChannelID: 101, UpstreamModel: "upstream-a", BillingModel: "gpt-4o-mini",
					SelectedWire: "responses", SelectedGroup: "default", ContextWindow: 128000,
				}},
			}

			apiErr := prepareRouteCandidateForAttempt(c, info, 0, 10, &types.TokenCountMeta{})

			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
			assert.True(t, types.IsSkipRetryError(apiErr))
			assert.Empty(t, spy.calls, "duplicate-key validation must fail before quota reservation")
			assert.Zero(t, info.UpstreamRequestBodySize, "duplicate-key validation must fail before upstream body preparation or send")
		})
	}
}

func TestSelectRouteCandidateForAttemptRefreshesAuthoritativeIdentity(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{RouteCandidates: []relaycommon.RouteCandidate{
		{RouteID: 7, TargetID: 70, ChannelID: 700, UpstreamModel: "wire-model-a", BillingModel: "price-a", SelectedWire: "responses", SelectedGroup: "cheap", ContextWindow: 8000},
		{RouteID: 7, TargetID: 71, ChannelID: 701, UpstreamModel: "wire-model-b", BillingModel: "price-b", SelectedWire: "openai_chat", SelectedGroup: "expensive", ContextWindow: 128000},
	}}

	require.NoError(t, selectRouteCandidateForAttempt(c, info, 1))
	assert.Equal(t, 1, info.RouteCandidateIndex)
	assert.Equal(t, 7, info.ModelRouteId)
	assert.Equal(t, 71, info.ModelTargetId)
	assert.Equal(t, 701, info.ResolvedChannelId)
	assert.Equal(t, "wire-model-b", info.ResolvedModelName)
	assert.Equal(t, "price-b", info.BillingModelName)
	assert.Equal(t, "openai_chat", info.SelectedWire)
	assert.Equal(t, 128000, info.RouteContextWindow)
	assert.Equal(t, "expensive", info.UsingGroup)
	assert.Equal(t, "expensive", common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
	assert.Equal(t, "expensive", common.GetContextKeyString(c, constant.ContextKeyAutoGroup))
}

func TestSelectRouteCandidateResetsChannelDerivedStateButPreservesBodyReplay(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	bodyGetter := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("body")), nil }
	info := &relaycommon.RelayInfo{
		RelayFormat:               types.RelayFormatOpenAIResponses,
		ResponsesViaChat:          true,
		FinalRequestRelayFormat:   types.RelayFormatClaude,
		RequestConversionChain:    []types.RelayFormat{types.RelayFormatOpenAIResponses, types.RelayFormatClaude},
		RuntimeHeadersOverride:    map[string]interface{}{"x-moonshot": "stale"},
		UseRuntimeHeadersOverride: true,
		ParamOverrideAudit:        []string{"stale override"},
		PriceData:                 types.PriceData{FreeModel: true, QuotaToPreConsume: 999},
		UpstreamRequestBodySize:   4,
		UpstreamRequestGetBody:    bodyGetter,
		RouteCandidates: []relaycommon.RouteCandidate{
			{TargetID: 1, ChannelID: 11, UpstreamModel: "moonshot", BillingModel: "price-a", SelectedWire: "openai_chat", SelectedGroup: "cheap"},
			{TargetID: 2, ChannelID: 12, UpstreamModel: "azure", BillingModel: "price-b", SelectedWire: "responses", SelectedGroup: "expensive"},
		},
	}

	require.NoError(t, selectRouteCandidateForAttempt(c, info, 1))

	assert.False(t, info.ResponsesViaChat)
	assert.Empty(t, info.FinalRequestRelayFormat)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAIResponses}, info.RequestConversionChain)
	assert.Nil(t, info.RuntimeHeadersOverride)
	assert.False(t, info.UseRuntimeHeadersOverride)
	assert.Nil(t, info.ParamOverrideAudit)
	assert.False(t, info.PriceData.FreeModel)
	assert.Zero(t, info.PriceData.QuotaToPreConsume)
	assert.Equal(t, int64(4), info.UpstreamRequestBodySize)
	require.NotNil(t, info.UpstreamRequestGetBody)
	reader, err := info.UpstreamRequestGetBody()
	require.NoError(t, err)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "body", string(data))
}

func TestOrderedFallbackAdvancesOnceAndStopsAtExplicitCandidateExhaustion(t *testing.T) {
	info := &relaycommon.RelayInfo{RouteCandidates: []relaycommon.RouteCandidate{
		{TargetID: 1, ChannelID: 11},
		{TargetID: 2, ChannelID: 12},
	}}

	assert.True(t, advanceRouteCandidate(info))
	assert.Equal(t, 1, info.RouteCandidateIndex)
	assert.False(t, advanceRouteCandidate(info))
	assert.Equal(t, 1, info.RouteCandidateIndex)
}

func TestPrepareAttemptBillingUsesEachCandidateBillingIdentity(t *testing.T) {
	configureRouteBillingRatios(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	spy := &relayRouteBillingSpy{}
	info := &relaycommon.RelayInfo{
		OriginModelName: "coder", UserGroup: "default", UsingGroup: "default", Billing: spy,
		RouteCandidates: []relaycommon.RouteCandidate{
			{RouteID: 1, TargetID: 1, ChannelID: 11, UpstreamModel: "upstream-a", BillingModel: "route-paid-a", SelectedWire: "responses"},
			{RouteID: 1, TargetID: 2, ChannelID: 12, UpstreamModel: "upstream-b", BillingModel: "route-paid-b", SelectedWire: "responses"},
			{RouteID: 1, TargetID: 3, ChannelID: 13, UpstreamModel: "upstream-free", BillingModel: "route-free", SelectedWire: "responses"},
		},
	}

	require.NoError(t, selectRouteCandidateForAttempt(c, info, 0))
	require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
	firstTarget := info.PriceData.QuotaToPreConsume
	require.Positive(t, firstTarget)
	assert.Equal(t, "route-paid-a", info.BillingModelName)

	require.NoError(t, selectRouteCandidateForAttempt(c, info, 1))
	require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
	secondTarget := info.PriceData.QuotaToPreConsume
	assert.Greater(t, secondTarget, firstTarget)
	assert.Equal(t, "route-paid-b", info.BillingModelName)
	assert.Equal(t, secondTarget, info.FinalPreConsumedQuota)

	require.NoError(t, selectRouteCandidateForAttempt(c, info, 2))
	require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
	assert.Equal(t, "route-free", info.BillingModelName)
	assert.True(t, info.PriceData.FreeModel)
	assert.Equal(t, []int{firstTarget, secondTarget}, spy.calls, "free fallback must retain the session without reserving again")
}

func TestPrepareAttemptBillingPreservesInsufficientQuotaAPIError(t *testing.T) {
	configureRouteBillingRatios(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	insufficient := types.NewErrorWithStatusCode(errors.New("insufficient"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	spy := &relayRouteBillingSpy{reserveErr: insufficient}
	info := &relaycommon.RelayInfo{OriginModelName: "coder", BillingModelName: "route-paid-a", UserGroup: "default", UsingGroup: "default", Billing: spy}

	apiErr := prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
}

func TestExpectedWireMapsResponsesCompactAndNativeGemini(t *testing.T) {
	assert.Equal(t, modelcatalog.WireResponses, expectedWireForFormat(types.RelayFormatOpenAIResponsesCompaction))
	assert.Equal(t, modelcatalog.WireGemini, expectedWireForFormat(types.RelayFormatGemini))
	assert.Empty(t, expectedWireForFormat(types.RelayFormat("unknown")))
}

func TestRouteCandidateFromResolvedReturnsUnsupportedWireError(t *testing.T) {
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIResponses}
	resolved := &modelcatalog.ResolvedModel{TargetID: 1, PreferredWires: []modelcatalog.WireProtocol{modelcatalog.WireAnthropic}}

	_, err := routeCandidateFromResolved(info, resolved)

	require.Error(t, err)
}

func TestPrepareAttemptBillingCreatesSessionForFirstPaidCandidate(t *testing.T) {
	configureRouteBillingRatios(t)
	db := setupTieredRelayBillingDB(t)
	createTieredRelayWalletFixtures(t, db, 801, 802, 10000, 10000, "route-paid-session")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId: 801, TokenId: 802, TokenKey: "route-paid-session", UserGroup: "default", UsingGroup: "default",
		OriginModelName: "coder", BillingModelName: "route-paid-a", ForcePreConsume: true,
		UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
	}

	apiErr := prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{})

	require.Nil(t, apiErr)
	require.NotNil(t, info.Billing)
	require.Positive(t, info.PriceData.QuotaToPreConsume)
	assert.Equal(t, info.PriceData.QuotaToPreConsume, info.FinalPreConsumedQuota)
	userQuota, tokenRemain, tokenUsed := tieredRelayQuotaState(t, db, 801, 802)
	assert.Equal(t, 10000-info.PriceData.QuotaToPreConsume, userQuota)
	assert.Equal(t, 10000-info.PriceData.QuotaToPreConsume, tokenRemain)
	assert.Equal(t, info.PriceData.QuotaToPreConsume, tokenUsed)
	require.NoError(t, info.Billing.Settle(info.PriceData.QuotaToPreConsume))
}

func TestAutoGroupRouteHigherCandidateCannotUseCheaperReservation(t *testing.T) {
	configureAutoGroupRouteBilling(t)
	db := setupTieredRelayBillingDB(t)
	createTieredRelayWalletFixtures(t, db, 821, 822, 1500, 5000, "route-auto-insufficient")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, "auto")
	info := &relaycommon.RelayInfo{
		UserId: 821, TokenId: 822, TokenKey: "route-auto-insufficient", UserGroup: "default", TokenGroup: "auto",
		OriginModelName: "coder", ForcePreConsume: true, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
		RouteCandidates: []relaycommon.RouteCandidate{
			{TargetID: 1, ChannelID: 11, UpstreamModel: "a", BillingModel: "route-paid", SelectedWire: "responses", SelectedGroup: "cheap"},
			{TargetID: 2, ChannelID: 12, UpstreamModel: "b", BillingModel: "route-paid", SelectedWire: "responses", SelectedGroup: "higher"},
		},
	}

	require.NoError(t, selectRouteCandidateForAttempt(c, info, 0))
	require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
	assert.Equal(t, 1000, info.FinalPreConsumedQuota)
	require.NoError(t, selectRouteCandidateForAttempt(c, info, 1))
	apiErr := prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	assert.Equal(t, "higher", info.UsingGroup)
	assert.Equal(t, 2.0, info.PriceData.GroupRatioInfo.GroupRatio)
	userQuota, tokenRemain, tokenUsed := tieredRelayQuotaState(t, db, 821, 822)
	assert.Equal(t, 500, userQuota)
	assert.Equal(t, 4000, tokenRemain)
	assert.Equal(t, 1000, tokenUsed)
	require.NoError(t, info.Billing.Settle(1000))
}

func TestAutoGroupRoutePaidHigherLowerFreeKeepsOneBillingSession(t *testing.T) {
	configureAutoGroupRouteBilling(t)
	db := setupTieredRelayBillingDB(t)
	createTieredRelayWalletFixtures(t, db, 831, 832, 10000, 10000, "route-auto-transitions")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, "auto")
	info := &relaycommon.RelayInfo{
		UserId: 831, TokenId: 832, TokenKey: "route-auto-transitions", UserGroup: "default", TokenGroup: "auto",
		OriginModelName: "coder", ForcePreConsume: true, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
		RouteCandidates: []relaycommon.RouteCandidate{
			{TargetID: 1, ChannelID: 11, UpstreamModel: "a", BillingModel: "route-paid", SelectedWire: "responses", SelectedGroup: "cheap"},
			{TargetID: 2, ChannelID: 12, UpstreamModel: "b", BillingModel: "route-paid", SelectedWire: "responses", SelectedGroup: "higher"},
			{TargetID: 3, ChannelID: 13, UpstreamModel: "c", BillingModel: "route-paid", SelectedWire: "responses", SelectedGroup: "lower"},
			{TargetID: 4, ChannelID: 14, UpstreamModel: "d", BillingModel: "route-paid", SelectedWire: "responses", SelectedGroup: "free"},
		},
	}

	for index, wantGroup := range []string{"cheap", "higher", "lower", "free"} {
		require.NoError(t, selectRouteCandidateForAttempt(c, info, index))
		require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
		assert.Equal(t, wantGroup, info.UsingGroup)
	}
	assert.Equal(t, 2000, info.Billing.GetPreConsumedQuota())
	assert.True(t, info.PriceData.FreeModel)
	userQuota, tokenRemain, tokenUsed := tieredRelayQuotaState(t, db, 831, 832)
	assert.Equal(t, 8000, userQuota)
	assert.Equal(t, 8000, tokenRemain)
	assert.Equal(t, 2000, tokenUsed)
	// The final free PriceData is intentionally left for the normal relay
	// settlement path; no pre-refund occurs between candidate attempts.
}

func TestPrepareRouteCandidateRevalidatesAbilityBeforeBillingAndBoundedFallback(t *testing.T) {
	for _, mutation := range []struct {
		name string
		run  func(*testing.T, *gorm.DB, relaycommon.RouteCandidate)
	}{
		{
			name: "disabled",
			run: func(t *testing.T, db *gorm.DB, candidate relaycommon.RouteCandidate) {
				require.NoError(t, db.Model(&model.Ability{}).
					Where("channel_id = ? AND model = ? AND `group` = ?", candidate.ChannelID, candidate.UpstreamModel, candidate.SelectedGroup).
					Update("enabled", false).Error)
			},
		},
		{
			name: "deleted",
			run: func(t *testing.T, db *gorm.DB, candidate relaycommon.RouteCandidate) {
				require.NoError(t, db.Where("channel_id = ? AND model = ? AND `group` = ?", candidate.ChannelID, candidate.UpstreamModel, candidate.SelectedGroup).
					Delete(&model.Ability{}).Error)
			},
		},
		{
			name: "regrouped",
			run: func(t *testing.T, db *gorm.DB, candidate relaycommon.RouteCandidate) {
				require.NoError(t, db.Model(&model.Ability{}).
					Where("channel_id = ? AND model = ? AND `group` = ?", candidate.ChannelID, candidate.UpstreamModel, candidate.SelectedGroup).
					Update("group", "other").Error)
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			db := setupRelayRouteRuntimeDB(t)
			configureAutoGroupRouteBilling(t)
			seedGroupedRelayRouteTargets(t, db, "cheap", "higher")
			c := relayRouteResolutionContext(t)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "auto")
			spy := &relayRouteBillingSpy{}
			info := &relaycommon.RelayInfo{
				OriginModelName: "coder", RelayFormat: types.RelayFormatOpenAIResponses,
				UserGroup: "default", TokenGroup: "auto", Billing: spy,
			}

			require.Nil(t, resolveClientModelRoute(c, info, 1000))
			require.Len(t, info.RouteCandidates, 2)
			assert.Equal(t, "cheap", info.RouteCandidates[0].SelectedGroup)
			assert.Equal(t, "higher", info.RouteCandidates[1].SelectedGroup)
			mutation.run(t, db, info.RouteCandidates[0])

			firstErr := prepareRouteCandidateForAttempt(c, info, 0, 1000, &types.TokenCountMeta{})

			require.NotNil(t, firstErr)
			assert.Equal(t, types.ErrorCodeGetChannelFailed, firstErr.GetErrorCode())
			assert.Empty(t, spy.calls, "a stale ability must be rejected before quota reservation")
			assert.Empty(t, c.GetStringSlice("use_channel"), "a stale ability must be rejected before any upstream send")
			require.True(t, shouldRetry(c, firstErr, 1))
			require.True(t, advanceRouteCandidate(info))

			require.Nil(t, prepareRouteCandidateForAttempt(c, info, info.RouteCandidateIndex, 1000, &types.TokenCountMeta{}))
			assert.Equal(t, "higher", info.UsingGroup)
			assert.Equal(t, 2.0, info.PriceData.GroupRatioInfo.GroupRatio)
			require.Len(t, spy.calls, 1, "only the fresh fallback candidate may reserve quota")
			assert.Equal(t, info.PriceData.QuotaToPreConsume, spy.calls[0])
			assert.False(t, advanceRouteCandidate(info), "explicit candidate exhaustion must remain bounded")
		})
	}
}

func TestGetChannelPinsExplicitTargetBeforeLegacyDistributor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open("file:explicit_route_target?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		sqlDB, sqlErr := db.DB()
		require.NoError(t, sqlErr)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.Create(&model.Channel{Id: 701, Name: "pinned", Type: constant.ChannelTypeOpenAI, Key: "test-key", Models: "wire-model-b", Status: common.ChannelStatusEnabled}).Error)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("channel_organization", "stale-org")
	c.Set("api_version", "stale-version")
	c.Set("region", "stale-region")
	c.Set("plugin", "stale-plugin")
	info := &relaycommon.RelayInfo{
		ResolvedModelName: "wire-model-b",
		RouteCandidates: []relaycommon.RouteCandidate{{
			RouteID: 7, TargetID: 71, ChannelID: 701, UpstreamModel: "wire-model-b", BillingModel: "price-b", SelectedWire: "responses",
		}},
	}
	retryParam := &service.RetryParam{Ctx: c, ModelName: "legacy-random-model", Retry: common.GetPointer(0)}

	channel, apiErr := getChannel(c, info, retryParam)

	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, 701, channel.Id)
	assert.Equal(t, 701, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
	assert.Equal(t, "wire-model-b", common.GetContextKeyString(c, constant.ContextKeyOriginalModel))
	assert.Empty(t, c.GetString("channel_organization"))
	assert.Empty(t, c.GetString("api_version"))
	assert.Empty(t, c.GetString("region"))
	assert.Empty(t, c.GetString("plugin"))
}

func TestGetChannelDoesNotFallBackWhenExplicitTargetIsUnavailable(t *testing.T) {
	setupRelayRouteRuntimeDB(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{RouteCandidates: []relaycommon.RouteCandidate{{TargetID: 71, ChannelID: 999, UpstreamModel: "missing"}}}

	channel, apiErr := getChannel(c, info, &service.RetryParam{Ctx: c, Retry: common.GetPointer(0)})

	assert.Nil(t, channel)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeGetChannelFailed, apiErr.GetErrorCode())
	assert.True(t, shouldRetry(c, apiErr, 1))
	info.RouteCandidates = append(info.RouteCandidates, relaycommon.RouteCandidate{TargetID: 72, ChannelID: 1000})
	assert.True(t, advanceRouteCandidate(info), "a retryable unavailable channel advances to the next explicit target")
}

func TestGetChannelRejectsDisabledExplicitTarget(t *testing.T) {
	db := setupRelayRouteRuntimeDB(t)
	require.NoError(t, db.Create(&model.Channel{Id: 711, Name: "disabled", Type: constant.ChannelTypeOpenAI, Key: "test", Status: common.ChannelStatusManuallyDisabled}).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{RouteCandidates: []relaycommon.RouteCandidate{{TargetID: 81, ChannelID: 711, UpstreamModel: "disabled"}}}

	channel, apiErr := getChannel(c, info, &service.RetryParam{Ctx: c, Retry: common.GetPointer(0)})

	assert.Nil(t, channel)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeGetChannelFailed, apiErr.GetErrorCode())
}

func TestUpdateRouteDecisionSelectionTracksFinalFallbackIdentity(t *testing.T) {
	const requestID = "route-final-fallback"
	first := &relaycommon.RelayInfo{OriginModelName: "coder", ResolvedModelName: "first", SelectedWire: "responses", ModelRouteId: 1, ModelTargetId: 10, ResolvedChannelId: 100}
	StartRouteDecision(requestID, first)
	final := &relaycommon.RelayInfo{OriginModelName: "coder", ResolvedModelName: "final", SelectedWire: "openai_chat", ModelRouteId: 1, ModelTargetId: 20, ResolvedChannelId: 200}

	UpdateRouteDecisionSelection(requestID, final)

	trace, ok := GetRouteDecision(requestID)
	require.True(t, ok)
	assert.Equal(t, "final", trace.ResolvedModel)
	assert.Equal(t, "openai_chat", trace.SelectedWire)
	assert.Equal(t, 20, trace.TargetID)
	assert.Equal(t, 200, trace.ChannelID)
}

type relayRouteBillingSpy struct {
	reserved      int
	calls         []int
	reserveErr    error
	beforeReserve func()
}

func (s *relayRouteBillingSpy) Reserve(target int) error {
	if s.beforeReserve != nil {
		s.beforeReserve()
	}
	s.calls = append(s.calls, target)
	if s.reserveErr != nil {
		return s.reserveErr
	}
	if target > s.reserved {
		s.reserved = target
	}
	return nil
}

func (s *relayRouteBillingSpy) GetPreConsumedQuota() int { return s.reserved }
func (s *relayRouteBillingSpy) Settle(int) error         { return nil }
func (s *relayRouteBillingSpy) Refund(*gin.Context)      {}
func (s *relayRouteBillingSpy) NeedsRefund() bool        { return false }
