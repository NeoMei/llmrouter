package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageRouteCanRoundTripThroughAdmin(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	require.NoError(t, db.Create(&model.Channel{Id: 25, Type: constant.ChannelTypeVolcEngine, Status: common.ChannelStatusEnabled, Models: "doubao-seedream-5-0-260128"}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 25, Model: "doubao-seedream-5-0-260128", Group: "default", Enabled: true}).Error)
	payload := clientModelRoutePayload{PublicID: "doubao-seedream-5-0-260128", Client: "llmrouter", Enabled: true, Metadata: `{"operations":["images.generations.create"]}`,
		Targets: []model.ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 25, UpstreamModel: "doubao-seedream-5-0-260128", BillingModel: "gpt-4o-mini", PreferredWires: "[]", Weight: 1, Enabled: true}}}
	route := payload.routeRecord()
	route.Id = 1
	_, err := modelcatalog.NewCatalog([]model.ClientModelRoute{route}, payload.Targets)
	require.NoError(t, err)
	ctx, rejected := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", payload)
	CreateClientModelRoute(ctx)
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	require.Contains(t, rejected.Body.String(), "per-image ModelPrice")
	oldPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ark-image-fixture-price":0.03}`))
	payload.Targets[0].BillingModel = "ark-image-fixture-price"
	ctx, w := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", payload)
	CreateClientModelRoute(ctx)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"preferred_wires":"[]"`)

	var stored model.ClientModelRoute
	require.NoError(t, db.First(&stored).Error)
	payload.ID = stored.Id
	ctx, w = clientModelRouteRequestContext(t, http.MethodPut, "/api/client_model_routes/", payload)
	UpdateClientModelRoute(ctx)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var savedTarget model.ClientModelTarget
	require.NoError(t, db.Where("route_id = ?", stored.Id).First(&savedTarget).Error)
	require.JSONEq(t, "[]", savedTarget.PreferredWires)
	require.Equal(t, "ark-image-fixture-price", savedTarget.BillingModel)
}

func TestRouteSaveRequiresEnabledGroupAbilityForMappedModel(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	const upstream = "gpt-4o-mini"
	const mappedFrom = "provider-alias"
	require.NoError(t, db.Create(&model.Channel{Id: 25, Type: constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled, Models: upstream,
		ModelMapping: common.GetPointer(`{"provider-alias":"gpt-4o-mini"}`)}).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{ChannelId: 25, Model: mappedFrom, Group: "default", Enabled: true},
		{ChannelId: 25, Model: upstream, Group: "default", Enabled: false},
		{ChannelId: 25, Model: upstream, Group: "", Enabled: true},
	}).Error)
	payload := clientModelRoutePayload{PublicID: "mapped-chat", Client: "llmrouter", Enabled: true,
		Targets: []model.ClientModelTarget{{ChannelId: 25, UpstreamModel: mappedFrom,
			BillingModel: upstream, PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}}}
	ctx, w := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", payload)
	CreateClientModelRoute(ctx)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), upstream)
	var count int64
	require.NoError(t, db.Model(&model.ClientModelRoute{}).Count(&count).Error)
	require.Zero(t, count)

	ability := db.Model(&model.Ability{ChannelId: 25, Model: upstream, Group: "default"})
	require.NoError(t, ability.Update("enabled", true).Error)
	ctx, w = clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", payload)
	CreateClientModelRoute(ctx)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var saved model.ClientModelRoute
	require.NoError(t, db.First(&saved).Error)
	payload.ID = saved.Id
	require.NoError(t, ability.Update("enabled", false).Error)
	ctx, w = clientModelRouteRequestContext(t, http.MethodPut, "/api/client_model_routes/", payload)
	UpdateClientModelRoute(ctx)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	payload.Enabled = false
	ctx, w = clientModelRouteRequestContext(t, http.MethodPut, "/api/client_model_routes/", payload)
	UpdateClientModelRoute(ctx)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, db.First(&saved, saved.Id).Error)
	require.False(t, saved.Enabled)
}

func TestDisabledRouteAndTargetStayDisabled(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	for _, id := range []int{25, 26} {
		require.NoError(t, db.Create(&model.Channel{Id: id, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: "gpt-4o-mini"}).Error)
	}
	require.NoError(t, db.Create(&model.Ability{ChannelId: 25, Model: "gpt-4o-mini", Group: "default", Enabled: true}).Error)
	payload := clientModelRoutePayload{PublicID: "audit-chat", Enabled: true, Targets: []model.ClientModelTarget{
		{ChannelId: 25, UpstreamModel: "gpt-4o-mini", BillingModel: "gpt-4o-mini", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true},
		{ChannelId: 26, UpstreamModel: "gpt-4o-mini", BillingModel: "gpt-4o-mini", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: false},
	}}
	ctx, w := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", payload)
	CreateClientModelRoute(ctx)
	require.Equal(t, 200, w.Code, w.Body.String())
	var saved model.ClientModelTarget
	require.NoError(t, db.Where("channel_id = ?", 26).First(&saved).Error)
	require.False(t, saved.Enabled)
	payload.PublicID = "audit-disabled-chat"
	payload.Enabled = false
	payload.Targets = payload.Targets[:1]
	payload.Targets[0].Enabled = false
	ctx, w = clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", payload)
	CreateClientModelRoute(ctx)
	require.Equal(t, 200, w.Code, w.Body.String())
	var route model.ClientModelRoute
	require.NoError(t, db.Where("public_id = ?", payload.PublicID).First(&route).Error)
	require.False(t, route.Enabled)
}

func TestDefaultClientDispatchesLegacyModelWhenRouteBelongsToAnotherClient(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	model.InitDatabaseColumns()
	redisWasEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = redisWasEnabled })
	require.NoError(t, db.AutoMigrate(&model.Ability{}, &model.User{}))
	name := "gpt-4o-mini"
	require.NoError(t, db.Create(&model.Channel{Id: 25, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: name, Group: "default", Key: "fixture-key"}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 25, Model: name, Group: "default", Enabled: true}).Error)
	route := model.ClientModelRoute{PublicId: name, Client: "llmrouter", Enabled: true, Metadata: `{"operations":["chat.completions.create"]}`}
	require.NoError(t, db.Create(&route).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{RouteId: route.Id, ChannelId: 25, UpstreamModel: name, BillingModel: name, PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}).Error)
	identity := func(c *gin.Context) {
		c.Set("id", 11)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	}
	r := gin.New()
	r.GET("/v1/models", identity, func(c *gin.Context) { ListModels(c, constant.ChannelTypeOpenAI) })
	r.POST("/v1/chat/completions", identity, middleware.Distribute(), func(c *gin.Context) {
		require.False(t, common.GetContextKeyBool(c, constant.ContextKeyDeferredModelRoute))
		require.Equal(t, 25, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
		c.Status(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"id":"gpt-4o-mini"`)
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"audit fixture"}]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 204, w.Code, w.Body.String())
}

func TestBadVideoTargetKeepsHealthyChatCatalog(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Ability{}))
	for _, id := range []int{25, 26} {
		require.NoError(t, db.Create(&model.Channel{Id: id, Type: constant.ChannelTypeVolcEngine, Status: common.ChannelStatusEnabled, Models: "gpt-4o-mini"}).Error)
		require.NoError(t, db.Create(&model.Ability{ChannelId: id, Model: "gpt-4o-mini", Group: "default", Enabled: true}).Error)
	}
	chat := model.ClientModelRoute{PublicId: "audit-chat", Client: "codex", Enabled: true, Metadata: `{"operations":["chat.completions.create"]}`}
	require.NoError(t, db.Create(&chat).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{RouteId: chat.Id, ChannelId: 26, UpstreamModel: "gpt-4o-mini", BillingModel: "gpt-4o-mini", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}).Error)
	ctx, _ := clientModelRouteRequestContext(t, http.MethodGet, "/v1/models?client=codex", nil)
	ctx.Set("id", 11)
	require.Len(t, listRuntimeClientModels(ctx, []string{"default"}, constant.ChannelTypeOpenAI), 1)
	video := model.ClientModelRoute{PublicId: "audit-bad-video", Client: "llmrouter", Enabled: true, Metadata: `{"operations":["videos.create"]}`}
	require.NoError(t, db.Create(&video).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{RouteId: video.Id, ChannelId: 25, UpstreamModel: "gpt-4o-mini", BillingModel: "gpt-4o-mini", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}).Error)
	listed := listRuntimeClientModels(ctx, []string{"default"}, constant.ChannelTypeOpenAI)
	require.Len(t, listed, 1)
	require.Equal(t, "audit-chat", listed[0].PublicID)
}

func TestExplicitModelStopBlocksCatalogAndDispatch(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	model.InitDatabaseColumns()
	redisWasEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = redisWasEnabled })
	require.NoError(t, db.AutoMigrate(&model.Ability{}, &model.Model{}, &model.User{}))
	name := "gpt-4o-mini"
	require.NoError(t, db.Create(&model.Channel{Id: 25, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: name, Group: "default", Key: "fixture-key"}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 25, Model: name, Group: "default", Enabled: true}).Error)
	require.NoError(t, (&model.Model{ModelName: name, Status: 0, NameRule: model.NameRuleExact}).Insert())

	identity := func(c *gin.Context) {
		c.Set("id", 11)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	}
	r := gin.New()
	r.GET("/v1/models", identity, func(c *gin.Context) { ListModels(c, constant.ChannelTypeOpenAI) })
	r.POST("/v1/chat/completions", identity, middleware.Distribute(), func(c *gin.Context) {
		t.Error("disabled model reached the relay")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), `"id":"gpt-4o-mini"`)
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"fixture"}]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}
