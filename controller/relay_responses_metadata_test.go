package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesDeclaredMetadataHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, fields string
		denied                 bool
	}{
		{"pool-background-5xx", `{"async":true}`, `,"background":true`, true},
		{"pool-background-network", `{"async":true}`, `,"background":true`, true},
		{"pool-background-auth", `{"async":true}`, `,"background":true`, false},
		{"pool-background-rate", `{"async":true}`, `,"background":true`, false},
		{"pool-sync-5xx", `{"async":false}`, `,"background":false`, false},
		{"hosted-image-output", `{"output_modalities":["text"],"media_limits":{"max_pixels":1}}`, `,"tools":[{"type":"image_generation"}]`, true},
		{"hosted-image-pixels", `{"output_modalities":["text","image"],"media_limits":{"max_pixels":1}}`, `,"tools":[{"type":"image_generation"}]`, true},
		{"background-true", `{"async":false}`, `,"background":true`, true},
		{"background-false", `{"async":false}`, `,"background":false`, false},
		{"background-absent", `{"async":false}`, ``, false},
		{"alias-passthrough", `{"async":false}`, `,"vendor_extension":{"large":9007199254740993}`, false},
		{"legacy-background", ``, `,"background":true`, false},
		{"opaque-function-schema", `{"output_modalities":["text"],"media_limits":{"max_pixels":1}}`, `,"tools":[{"type":"function","name":"describe_image_generation","parameters":{"type":"object","description":"image_generation","properties":{"image_generation":{"type":"string"}}}}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupTieredRelayBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}))
			previousLogDB, previousLog, previousCache, previousRetry := model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes
			model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = db, true, false, 0
			if strings.HasPrefix(tc.name, "pool-") {
				common.RetryTimes = 1
				require.NoError(t, db.AutoMigrate(&model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
			}
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			previousTranslator := common.TranslateMessage
			require.NoError(t, i18n.Init())
			t.Cleanup(func() {
				common.TranslateMessage = previousTranslator
				model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = previousLogDB, previousLog, previousCache, previousRetry
			})
			ratio_setting.InitRatioSettings()
			previousRatios, previousCompletion := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-responses-metadata":1}`))
			require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"gpt-responses-metadata":1}`))
			t.Cleanup(func() {
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
				require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(previousCompletion))
			})
			createTieredRelayWalletFixtures(t, db, 910, 911, 100000, 100000, "responses-metadata-token")
			service.InitHttpClient()
			service.InitTokenEncoders()
			var sends atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := sends.Add(1)
				if strings.HasPrefix(tc.name, "pool-") && attempt == 1 {
					switch tc.name {
					case "pool-background-network":
						connection, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						connection.Close()
						return
					case "pool-background-auth":
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"fixture"}}`))
						return
					case "pool-background-rate":
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"fixture"}}`))
						return
					default:
						w.WriteHeader(http.StatusBadGateway)
						return
					}
				}
				raw, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Contains(t, string(raw), `"model":"gpt-responses-metadata"`)
				switch tc.name {
				case "alias-passthrough":
					assert.Contains(t, string(raw), `"vendor_extension":{"large":9007199254740993}`)
				case "background-false":
					assert.Contains(t, string(raw), `"background":false`)
				case "background-absent":
					assert.NotContains(t, string(raw), `"background"`)
				case "legacy-background":
					assert.Contains(t, string(raw), `"background":true`)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_metadata","object":"response","status":"completed","model":"gpt-responses-metadata","output":[{"type":"message","id":"msg_metadata","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`))
			}))
			defer upstream.Close()
			require.NoError(t, db.Create(&model.Channel{Id: 921, Type: constant.ChannelTypeOpenAI, Name: "responses-metadata", Key: "fixture", BaseURL: &upstream.URL, Models: "gpt-responses-metadata", Group: "default", Status: common.ChannelStatusEnabled, Setting: common.GetPointer(`{"pass_through_body_enabled":true}`)}).Error)
			if strings.HasPrefix(tc.name, "pool-") {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 921).Updates(map[string]any{"key": "fixture-A\nfixture-B", "channel_info": model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling}}).Error)
			}
			require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: "gpt-responses-metadata", Enabled: true}).Error)
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 931, PublicId: "responses-public", Aliases: `["responses-alias"]`, Enabled: true, Metadata: tc.metadata}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 932, RouteId: 931, ChannelId: 921, UpstreamModel: "gpt-responses-metadata", BillingModel: "gpt-responses-metadata", PreferredWires: `["responses"]`, Weight: 1, Enabled: true}).Error)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserId, 910)
				common.SetContextKey(c, constant.ContextKeyTokenId, 911)
				common.SetContextKey(c, constant.ContextKeyTokenKey, "responses-metadata-token")
				common.SetContextKey(c, constant.ContextKeyUserQuota, 100000)
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
				c.Next()
				common.CleanupBodyStorage(c)
			})
			engine.POST("/v1/responses", middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAIResponses) })
			requestModel := "responses-public"
			if tc.name == "alias-passthrough" {
				requestModel = "responses-alias"
			}
			body := `{"model":"` + requestModel + `","input":"hello","max_output_tokens":10` + tc.fields + `}`
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if tc.denied {
				if strings.HasPrefix(tc.name, "pool-") {
					require.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
					assert.EqualValues(t, 1, sends.Load())
				} else {
					require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
					assert.Zero(t, sends.Load())
				}
				require.Eventually(t, func() bool {
					wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
					return wallet == 100000 && token == 100000 && used == 0
				}, time.Second, time.Millisecond)
				wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
				assert.Equal(t, 100000, wallet)
				assert.Equal(t, 100000, token)
				assert.Zero(t, used)
				var logs int64
				require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logs).Error)
				assert.Zero(t, logs)
			} else {
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				if strings.HasPrefix(tc.name, "pool-") {
					assert.EqualValues(t, 2, sends.Load())
				} else {
					assert.EqualValues(t, 1, sends.Load())
				}
				wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
				assert.Equal(t, 99985, wallet)
				assert.Equal(t, 99985, token)
				assert.Equal(t, 15, used)
			}
			require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
		})
	}
}
