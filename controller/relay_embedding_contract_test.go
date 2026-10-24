package controller

import (
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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture crosses the real controller, HTTP adaptor, response writer and
// wallet/token settlement, so invalid vectors cannot pass by validating a DTO
// after the ready response or the charge has already escaped.
func TestEmbeddingFixedSpaceHTTP(t *testing.T) {
	for _, scenario := range []string{"numeric", "override-input", "override-safe-scalar", "ordered-batch", "missing-index", "duplicate-index", "missing-item", "nonfinite", "dimension-mismatch", "declared-space-mismatch", "invalid-dimensions", "requested-space-mismatch", "wrong-model", "null-vector", "alias", "default-dimensions", "alias-token-denied", "alias-group-denied", "alias-route-deleted", "legacy-numeric", "legacy-base64"} {
		t.Run(scenario, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupTieredRelayBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}))
			oldLogDB, oldLog, oldCache, oldRetry := model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes
			model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = db, true, false, 0
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			oldTranslator := common.TranslateMessage
			require.NoError(t, i18n.Init())
			t.Cleanup(func() {
				common.TranslateMessage = oldTranslator
				model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = oldLogDB, oldLog, oldCache, oldRetry
			})
			ratio_setting.InitRatioSettings()
			oldRatios := ratio_setting.ModelRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"text-embedding-v4":1}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios)) })
			createTieredRelayWalletFixtures(t, db, 910, 911, 100000, 100000, "embedding-test-token")
			var sends atomic.Int32
			vector := make([]float64, 1024)
			vector[0] = 0.25
			second := make([]float64, 1024)
			second[0] = 0.75
			data := []dto.EmbeddingResponseItem{{Object: "embedding", Index: 0, Embedding: vector}}
			if scenario == "ordered-batch" || scenario == "duplicate-index" || scenario == "missing-item" {
				data = append(data, dto.EmbeddingResponseItem{Object: "embedding", Index: 1, Embedding: second})
			}
			if scenario == "duplicate-index" {
				data[1].Index = 0
			}
			if scenario == "missing-item" {
				data = data[:1]
			}
			if scenario == "dimension-mismatch" {
				data[0].Embedding = vector[:1000]
			}
			raw, err := common.Marshal(dto.EmbeddingResponse{Object: "list", Model: "text-embedding-v4", Data: data, Usage: dto.Usage{PromptTokens: 10, TotalTokens: 10}})
			require.NoError(t, err)
			if scenario == "missing-index" {
				raw = []byte(strings.Replace(string(raw), `"index":0,`, "", 1))
			}
			if scenario == "wrong-model" {
				raw = []byte(strings.Replace(string(raw), `"model":"text-embedding-v4"`, `"model":"other-model"`, 1))
			}
			if scenario == "null-vector" {
				raw = []byte(strings.Replace(string(raw), "0.25", "null", 1))
			}
			if scenario == "nonfinite" {
				raw = []byte(strings.Replace(string(raw), "0.25", "1e999", 1))
			}
			if scenario == "legacy-base64" {
				raw = []byte(`{"object":"list","model":"text-embedding-v4","data":[{"index":0,"embedding":"AAAAAA=="}],"usage":{"prompt_tokens":10,"total_tokens":10}}`)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				requestRaw, readErr := io.ReadAll(r.Body)
				assert.NoError(t, readErr)
				if scenario != "legacy-base64" {
					assert.Contains(t, string(requestRaw), `"dimensions":1024`)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
			}))
			defer upstream.Close()
			service.InitHttpClient()
			service.InitTokenEncoders()
			require.NoError(t, db.Create(&model.Channel{Id: 921, Type: constant.ChannelTypeOpenAI, Name: "embedding", Key: "fixture-key", BaseURL: &upstream.URL, Models: "text-embedding-v4", Group: "default", Status: common.ChannelStatusEnabled}).Error)
			if scenario == "override-input" {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 921).Update("param_override", `{"input":[{"type":"image_url","image_url":{"url":"https://example.test/i.png"}}]}`).Error)
			}
			if scenario == "override-safe-scalar" {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 921).Update("param_override", `{"temperature":0}`).Error)
			}

			require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: "text-embedding-v4", Enabled: true}).Error)
			metadata := `{"input_modalities":["text"],"output_modalities":["embedding"],"operations":["embeddings.create"],"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text/query-document-v1"}}`
			if scenario == "legacy-base64" || scenario == "legacy-numeric" {
				metadata = ""
			}
			publicID := "text-embedding-v4"
			if strings.HasPrefix(scenario, "alias") {
				publicID = "superwagie-fixed-vector"
			}
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 931, PublicId: publicID, Aliases: `["vector-alias"]`, Enabled: true, Metadata: metadata}).Error)
			target := model.ClientModelTarget{Id: 932, RouteId: 931, ChannelId: 921, UpstreamModel: "text-embedding-v4", BillingModel: "text-embedding-v4", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}
			if scenario == "declared-space-mismatch" {
				target.Metadata = strings.Replace(metadata, `"version":"v4"`, `"version":"v5"`, 1)
			}
			require.NoError(t, db.Create(&target).Error)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserId, 910)
				common.SetContextKey(c, constant.ContextKeyTokenId, 911)
				common.SetContextKey(c, constant.ContextKeyTokenKey, "embedding-test-token")
				common.SetContextKey(c, constant.ContextKeyUserQuota, 100000)
				if scenario == "alias-token-denied" {
					common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
					common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"unrelated": true})
				}
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
				c.Next()
				common.CleanupBodyStorage(c)
			})
			if scenario == "alias-group-denied" {
				require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 921).Update("group", "private-group").Error)
			}
			engine.POST("/v1/embeddings", middleware.Distribute(), func(c *gin.Context) {
				if scenario == "alias-route-deleted" {
					require.NoError(t, db.Where("id = ?", 931).Delete(&model.ClientModelRoute{}).Error)
				}
				Relay(c, types.RelayFormatEmbedding)
			})
			body := `{"model":"text-embedding-v4","input":"hello","dimensions":1024}`
			if scenario == "ordered-batch" || scenario == "duplicate-index" || scenario == "missing-item" {
				body = `{"model":"text-embedding-v4","input":["hello","world"],"dimensions":1024}`
			}
			if scenario == "default-dimensions" {
				body = strings.Replace(body, `,"dimensions":1024`, "", 1)
			}
			if strings.HasPrefix(scenario, "alias") {
				body = strings.Replace(body, `"model":"text-embedding-v4"`, `"model":"vector-alias"`, 1)
			}
			if scenario == "requested-space-mismatch" {
				body = strings.Replace(body, "1024", "2048", 1)
			}
			if scenario == "invalid-dimensions" {
				body = strings.Replace(body, "1024", "0", 1)
			}
			if scenario == "legacy-base64" {
				body = `{"model":"text-embedding-v4","input":"hello","encoding_format":"base64"}`
			}
			request := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			valid := scenario == "numeric" || scenario == "ordered-batch" || scenario == "legacy-base64" || scenario == "legacy-numeric" || scenario == "alias" || scenario == "default-dimensions" || scenario == "override-safe-scalar"
			if valid {
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
				assert.EqualValues(t, 1, sends.Load())
				if scenario == "legacy-base64" {
					assert.Contains(t, recorder.Body.String(), `"embedding":"AAAAAA=="`)
				} else {
					var response dto.EmbeddingResponse
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
					require.Len(t, response.Data[0].Embedding, 1024)
					assert.Equal(t, 0.25, response.Data[0].Embedding[0])
					if scenario == "ordered-batch" {
						require.Len(t, response.Data, 2)
						assert.Equal(t, []int{0, 1}, []int{response.Data[0].Index, response.Data[1].Index})
						assert.Equal(t, 0.75, response.Data[1].Embedding[0])
					}
				}
				wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
				assert.Equal(t, 99990, wallet)
				assert.Equal(t, 99990, token)
				assert.Equal(t, 10, used)
			} else {
				require.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
				assert.NotContains(t, recorder.Body.String(), `"embedding":[`)
				require.Eventually(t, func() bool {
					wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
					return wallet == 100000 && token == 100000 && used == 0
				}, time.Second, time.Millisecond)
				var count int64
				require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&count).Error)
				assert.Zero(t, count)
				if scenario == "invalid-dimensions" || scenario == "declared-space-mismatch" || scenario == "requested-space-mismatch" || strings.HasPrefix(scenario, "alias-") || scenario == "override-input" {
					assert.Zero(t, sends.Load())
				}
			}
			require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
		})
	}
}

func TestConfiguredPublicModelCannotBypassMetadataThroughTaskHandler(t *testing.T) {
	db := setupClientModelRouteControllerTestDB(t)
	previousLogDB := model.LOG_DB
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() { model.LOG_DB = previousLogDB })
	previousTranslator := common.TranslateMessage
	require.NoError(t, i18n.Init())
	t.Cleanup(func() { common.TranslateMessage = previousTranslator })
	require.NoError(t, db.AutoMigrate(&model.Ability{}))
	oldCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = oldCache })
	require.NoError(t, db.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeSora, Name: "task", Key: "fixture", Models: "fixed-public", Group: "default", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 7, Group: "default", Model: "fixed-public", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "fixed-public", Enabled: true, Metadata: `{"operations":["embeddings.create"],"input_modalities":["text"]}`}).Error)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		c.Next()
		common.CleanupBodyStorage(c)
	})
	called := false
	engine.POST("/v1/videos", middleware.Distribute(), func(c *gin.Context) { called = true; c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"fixed-public","prompt":"fixture"}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	assert.False(t, called, "unsupported handler must not reserve quota or create a task")
	assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
}
