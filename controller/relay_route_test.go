package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/modelcatalog"
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
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("id", 99)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
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

func TestSelectRouteCandidateForAttemptRefreshesAuthoritativeIdentity(t *testing.T) {
	info := &relaycommon.RelayInfo{RouteCandidates: []relaycommon.RouteCandidate{
		{RouteID: 7, TargetID: 70, ChannelID: 700, UpstreamModel: "wire-model-a", BillingModel: "price-a", SelectedWire: "responses", ContextWindow: 8000},
		{RouteID: 7, TargetID: 71, ChannelID: 701, UpstreamModel: "wire-model-b", BillingModel: "price-b", SelectedWire: "openai_chat", ContextWindow: 128000},
	}}

	require.NoError(t, selectRouteCandidateForAttempt(info, 1))
	assert.Equal(t, 1, info.RouteCandidateIndex)
	assert.Equal(t, 7, info.ModelRouteId)
	assert.Equal(t, 71, info.ModelTargetId)
	assert.Equal(t, 701, info.ResolvedChannelId)
	assert.Equal(t, "wire-model-b", info.ResolvedModelName)
	assert.Equal(t, "price-b", info.BillingModelName)
	assert.Equal(t, "openai_chat", info.SelectedWire)
	assert.Equal(t, 128000, info.RouteContextWindow)
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

	require.NoError(t, selectRouteCandidateForAttempt(info, 0))
	require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
	firstTarget := info.PriceData.QuotaToPreConsume
	require.Positive(t, firstTarget)
	assert.Equal(t, "route-paid-a", info.BillingModelName)

	require.NoError(t, selectRouteCandidateForAttempt(info, 1))
	require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
	secondTarget := info.PriceData.QuotaToPreConsume
	assert.Greater(t, secondTarget, firstTarget)
	assert.Equal(t, "route-paid-b", info.BillingModelName)
	assert.Equal(t, secondTarget, info.FinalPreConsumedQuota)

	require.NoError(t, selectRouteCandidateForAttempt(info, 2))
	require.Nil(t, prepareAttemptBilling(c, info, 1000, &types.TokenCountMeta{}))
	assert.Equal(t, "route-free", info.BillingModelName)
	assert.True(t, info.PriceData.FreeModel)
	assert.Equal(t, []int{firstTarget, secondTarget}, spy.calls, "free fallback must retain the session without reserving again")
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

type relayRouteBillingSpy struct {
	reserved int
	calls    []int
}

func (s *relayRouteBillingSpy) Reserve(target int) error {
	s.calls = append(s.calls, target)
	if target > s.reserved {
		s.reserved = target
	}
	return nil
}

func (s *relayRouteBillingSpy) GetPreConsumedQuota() int { return s.reserved }
func (s *relayRouteBillingSpy) Settle(int) error         { return nil }
func (s *relayRouteBillingSpy) Refund(*gin.Context)      {}
func (s *relayRouteBillingSpy) NeedsRefund() bool        { return false }
