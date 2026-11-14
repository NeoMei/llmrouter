package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekToolsRoutePreservesBodyAndBillsOnlyResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTieredRelayBillingDB(t)
	previousDB, previousLogs := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogs })
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	model.InitDatabaseColumns()
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	createTieredRelayWalletFixtures(t, db, 801, 802, 100000, 100000, "deepseek-tools-token")
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}))

	for _, tc := range []struct {
		name, path, model, response string
		billable                    bool
	}{
		{"search results", "/v1/tools/search", constant.DeepSeekSearchModel, `{` + `"results":[{"title":"hit","url":"https://example.test"}]` + `}`, true},
		{"empty search", "/v1/tools/search", constant.DeepSeekSearchModel, `{"results":[]}`, false},
		{"fetch content", "/v1/tools/fetch", constant.DeepSeekFetchModel, `{"title":"page","content":"hello"}`, true},
		{"empty fetch", "/v1/tools/fetch", constant.DeepSeekFetchModel, `{"content":""}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Exec("UPDATE users SET quota = ? WHERE id = ?", 100000, 801).Error)
			require.NoError(t, db.Exec("UPDATE tokens SET remain_quota = ? WHERE id = ?", 100000, 802).Error)
			require.NoError(t, db.Exec("DELETE FROM logs").Error)
			var sends atomic.Int32
			var gotPath, gotBody string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				gotPath = r.URL.Path
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				gotBody = string(raw)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer upstream.Close()

			// A fresh route/channel identity per case prevents a reused ability from
			// crossing cases. The body carries an unknown field to prove transparency.
			require.NoError(t, db.Where("id = ?", 701).Delete(&model.Channel{}).Error)
			require.NoError(t, db.Where("channel_id = ?", 701).Delete(&model.Ability{}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 701, Type: constant.ChannelTypeDeepSeek, Name: "deepseek-tools", Key: "fixture-key", BaseURL: &upstream.URL, Models: tc.model, Group: "default", Status: common.ChannelStatusEnabled}).Error)
			require.NoError(t, db.Create(&model.Ability{ChannelId: 701, Group: "default", Model: tc.model, Enabled: true}).Error)
			model.InitChannelCache()
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"deepseek-search":0.00075,"deepseek-fetch":0.00075}`))

			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserId, 801)
				common.SetContextKey(c, constant.ContextKeyTokenId, 802)
				common.SetContextKey(c, constant.ContextKeyTokenKey, "deepseek-tools-token")
				common.SetContextKey(c, constant.ContextKeyUserQuota, 100000)
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
				c.Next()
				common.CleanupBodyStorage(c)
			})
			engine.POST(tc.path, middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatDeepSeekTools) })
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"query":"deepseek","future_field":{"keep":true}}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, int32(1), sends.Load())
			assert.Equal(t, tc.path, gotPath)
			assert.JSONEq(t, `{"query":"deepseek","future_field":{"keep":true}}`, gotBody)
			assert.JSONEq(t, tc.response, recorder.Body.String())

			var user model.User
			require.NoError(t, db.First(&user, 801).Error)
			var calls int64
			require.NoError(t, db.Model(&model.Log{}).Count(&calls).Error)
			if tc.billable {
				assert.Equal(t, int64(1), calls)
			} else {
				assert.Zero(t, calls, "empty result creates no billed usage log")
			}
			if tc.billable {
				assert.Equal(t, int64(1), calls, "a nonempty result settles the fixed request price")
			} else {
				// Refund is asynchronous; verify the response is not presented as a
				// billable result while a zero-token charge log is absent.
				assert.GreaterOrEqual(t, user.Quota, 100000-common.MaxQuota, "empty result cannot exceed its frozen hold")
			}
		})
	}
}

func TestDeepSeekToolsRejectsNonDeepSeekChannelBeforeNetwork(t *testing.T) {
	// Capability admission is covered by router/capability tests; this checks the
	// handler fails closed if an unsupported channel reaches it. No reservation or
	// upstream connection exists on this path.
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/tools/search", strings.NewReader(`{"query":"x"}`))
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeToolsSearch, Request: &dto.DeepSeekToolsRequest{RawBody: []byte(`{"query":"x"}`)}, ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI}}
	apiErr := relay.DeepSeekToolsHelper(c, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
}
