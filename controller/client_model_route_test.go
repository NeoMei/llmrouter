package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupClientModelRouteControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	db, err := gorm.Open(sqlite.Open("file:client_model_routes_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	ratio_setting.InitRatioSettings()
	require.NoError(t, db.AutoMigrate(&model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Channel{}))
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return db
}

func clientModelRouteRequestContext(t *testing.T, method, path string, payload any) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var body *strings.Reader
	if payload == nil {
		body = strings.NewReader("")
	} else {
		encoded, err := common.Marshal(payload)
		require.NoError(t, err)
		body = strings.NewReader(string(encoded))
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, body)
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx, recorder
}

func TestCreateClientModelRouteRejectsAliasCollisionWithConflict(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	require.NoError(t, db.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Models: "a,b", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "deepseek/a", Aliases: `["shared"]`, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 7, UpstreamModel: "a", BillingModel: "a", PreferredWires: `["responses"]`, Weight: 1, Enabled: true}).Error)
	ctx, recorder := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", clientModelRoutePayload{
		PublicID: "kimi/b", Aliases: []string{"shared"}, Enabled: true,
		Targets: []model.ClientModelTarget{{ChannelId: 7, UpstreamModel: "b", BillingModel: "gpt-4o-mini", PreferredWires: `["responses"]`, Weight: 1, Enabled: true}},
	})
	CreateClientModelRoute(ctx)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), "shared")
}

func TestCreateClientModelRouteRejectsMissingOrDisabledChannel(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	require.NoError(t, db.Create(&model.Channel{Id: 8, Status: common.ChannelStatusManuallyDisabled}).Error)

	for name, channelID := range map[string]int{"missing": 7, "disabled": 8} {
		t.Run(name, func(t *testing.T) {
			ctx, recorder := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", clientModelRoutePayload{
				PublicID: "deepseek/" + name, Enabled: true,
				Targets: []model.ClientModelTarget{{ChannelId: channelID, UpstreamModel: "a", BillingModel: "gpt-4o-mini", PreferredWires: `["responses"]`, Weight: 1, Enabled: true}},
			})

			CreateClientModelRoute(ctx)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			if name == "missing" {
				require.Contains(t, recorder.Body.String(), "nonexistent channel")
			} else {
				require.Contains(t, recorder.Body.String(), "disabled channel")
			}
			var count int64
			require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("public_id = ?", "deepseek/"+name).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestClientModelRouteCRUDIsTransactionalAndDoesNotExposeKeys(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	require.NoError(t, db.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Name: "deepseek", Key: "secret-key", Models: "a", Status: common.ChannelStatusEnabled}).Error)
	ctx, recorder := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", clientModelRoutePayload{
		PublicID: "deepseek/a", DisplayName: "DeepSeek", Client: "codex", Aliases: []string{"gpt-5.6-sol"}, Enabled: true,
		Targets: []model.ClientModelTarget{{ChannelId: 7, UpstreamModel: "a", BillingModel: "gpt-4o-mini", PreferredWires: `["responses","openai_chat"]`, Priority: 4, Weight: 2, Enabled: true}},
	})
	CreateClientModelRoute(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)

	listCtx, listRecorder := clientModelRouteRequestContext(t, http.MethodGet, "/api/client_model_routes/", nil)
	ListClientModelRoutes(listCtx)
	require.Equal(t, http.StatusOK, listRecorder.Code)
	require.Contains(t, listRecorder.Body.String(), "gpt-5.6-sol")
	require.NotContains(t, listRecorder.Body.String(), "secret-key")

	var route model.ClientModelRoute
	require.NoError(t, db.First(&route).Error)
	updateCtx, updateRecorder := clientModelRouteRequestContext(t, http.MethodPut, "/api/client_model_routes/", clientModelRoutePayload{
		ID: route.Id, PublicID: route.PublicId, DisplayName: "Updated", Client: "codex", Enabled: true,
		Targets: []model.ClientModelTarget{{ChannelId: 7, UpstreamModel: "a", BillingModel: "gpt-4o-mini", PreferredWires: `["responses"]`, Priority: 8, Weight: 1, Enabled: true}},
	})
	UpdateClientModelRoute(updateCtx)
	require.Equal(t, http.StatusOK, updateRecorder.Code)
	var target model.ClientModelTarget
	require.NoError(t, db.Where("route_id = ?", route.Id).First(&target).Error)
	require.Equal(t, 8, target.Priority)
}

func TestClientModelRouteOptionsExposeOnlyEnabledNonSensitiveChoices(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	baseURL := "https://provider.example/v1"
	headerOverride := `{"Authorization":"Bearer secret-header"}`
	require.NoError(t, db.Create(&model.Channel{
		Id: 7, Type: constant.ChannelTypeOpenAI, Name: "enabled", Key: "secret-key",
		BaseURL: &baseURL, HeaderOverride: &headerOverride,
		Models: " model-b,model-a,MODEL-A, ", Status: common.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id: 8, Type: constant.ChannelTypeOpenAI, Name: "disabled", Key: "other-secret",
		Models: "hidden", Status: common.ChannelStatusManuallyDisabled,
	}).Error)

	ctx, recorder := clientModelRouteRequestContext(t, http.MethodGet, "/api/client_model_routes/options", nil)
	ListClientModelRouteOptions(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "secret-key")
	require.NotContains(t, recorder.Body.String(), "secret-header")
	require.NotContains(t, recorder.Body.String(), "provider.example")
	require.NotContains(t, recorder.Body.String(), "disabled")
	var response struct {
		Success bool                            `json:"success"`
		Data    clientModelRouteOptionsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Len(t, response.Data.Channels, 1)
	require.Equal(t, []string{"model-a", "model-b"}, response.Data.Channels[0].Models)
	require.NotEmpty(t, response.Data.Channels[0].Wires)
	require.Contains(t, response.Data.BillingModels, "gpt-4o-mini")
}

func TestCreateClientModelRouteRejectsInvalidTargetConfiguration(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 7, Type: constant.ChannelTypeOpenAI, Name: "openai", Models: "allowed",
		Status: common.ChannelStatusEnabled,
	}).Error)

	tests := []struct {
		name   string
		target model.ClientModelTarget
		want   string
	}{
		{
			name:   "upstream model is absent from channel",
			target: model.ClientModelTarget{ChannelId: 7, UpstreamModel: "missing", BillingModel: "gpt-4o-mini", PreferredWires: `["responses"]`, Weight: 1, Enabled: true},
			want:   "is not enabled on channel",
		},
		{
			name:   "billing model has no price",
			target: model.ClientModelTarget{ChannelId: 7, UpstreamModel: "allowed", BillingModel: "definitely-unpriced-model", PreferredWires: `["responses"]`, Weight: 1, Enabled: true},
			want:   "has no billing configuration",
		},
		{
			name:   "disabled target still rejects unknown wire",
			target: model.ClientModelTarget{ChannelId: 7, UpstreamModel: "allowed", BillingModel: "gpt-4o-mini", PreferredWires: `["future_wire"]`, Weight: 1, Enabled: false},
			want:   "unknown wire protocol",
		},
		{
			name:   "enabled route needs an enabled target",
			target: model.ClientModelTarget{ChannelId: 7, UpstreamModel: "allowed", BillingModel: "gpt-4o-mini", PreferredWires: `["responses"]`, Weight: 1, Enabled: false},
			want:   "requires at least one enabled target",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, recorder := clientModelRouteRequestContext(t, http.MethodPost, "/api/client_model_routes/", clientModelRoutePayload{
				PublicID: "test/" + strings.ReplaceAll(test.name, " ", "-"), Enabled: true,
				Targets: []model.ClientModelTarget{test.target},
			})
			CreateClientModelRoute(ctx)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), test.want)
		})
	}
}

func TestDeleteClientModelRouteRejectsMissingRoute(t *testing.T) {
	setupClientModelRouteControllerTestDB(t)
	ctx, recorder := clientModelRouteRequestContext(t, http.MethodDelete, "/api/client_model_routes/999", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "999"}}

	DeleteClientModelRoute(ctx)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), gorm.ErrRecordNotFound.Error())
}
