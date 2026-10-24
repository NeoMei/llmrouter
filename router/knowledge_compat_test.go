package router

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
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Exact fixed SuperWagie request shapes cross the production Gin router,
// bearer authentication, catalog aliases, Ali HTTP conversion and settlement.
func TestKnowledgeCompatibleHTTP(t *testing.T) {
	for _, scenario := range []string{"native", "native-nil", "native-true", "native-top-one", "standard-nil", "standard", "embedding", "chat", "missing-token", "bad-token", "token-model", "token-group", "channel-group", "bad-input", "null-input", "bad-documents", "bad-top-n", "zero-top-n", "over-top-n", "bad-return-documents", "passthrough", "invalid-index", "duplicate-index", "invalid-score", "missing-index", "null-score", "negative-usage", "missing-usage", "global-passthrough", "chat-passthrough", "native-override"} {
		t.Run(scenario, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db, err := gorm.Open(sqlite.Open("file:knowledge_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}))
			oldDB, oldLogDB, oldType := model.DB, model.LOG_DB, common.MainDatabaseType()
			oldPassthrough := model_setting.GetGlobalSettings().PassThroughRequestEnabled
			model_setting.GetGlobalSettings().PassThroughRequestEnabled = scenario == "global-passthrough" || scenario == "chat-passthrough"
			t.Cleanup(func() { model_setting.GetGlobalSettings().PassThroughRequestEnabled = oldPassthrough })
			oldCache, oldRedis, oldLog, oldRetry, oldTranslate := common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes, common.TranslateMessage
			model.DB = db
			common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			common.MemoryCacheEnabled = false
			common.RedisEnabled = false
			common.LogConsumeEnabled = true
			common.RetryTimes = 0
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			require.NoError(t, i18n.Init())
			t.Cleanup(func() {
				require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
				model.DB = oldDB
				model.LOG_DB = oldLogDB
				common.SetMainDatabaseType(oldType)
				common.MemoryCacheEnabled = oldCache
				common.RedisEnabled = oldRedis
				common.LogConsumeEnabled = oldLog
				common.RetryTimes = oldRetry
				common.TranslateMessage = oldTranslate
				require.NoError(t, sqlDB.Close())
			})
			ratio_setting.InitRatioSettings()
			oldRatios := ratio_setting.ModelRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gte-rerank-v2":1,"text-embedding-v4":1,"qwen-plus":1}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios)) })
			require.NoError(t, db.Create(&model.User{Id: 901, Username: "knowledge", Group: "default", Status: common.UserStatusEnabled, Quota: 100000, Setting: `{"billing_preference":"wallet_only"}`}).Error)
			token := model.Token{Id: 902, UserId: 901, Key: "knowledgefixture", Name: "knowledge", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100000}
			if scenario == "token-model" {
				token.ModelLimitsEnabled = true
				token.ModelLimits = "unrelated"
			}
			if scenario == "token-group" {
				token.Group = "forbidden"
			}
			require.NoError(t, db.Create(&token).Error)
			modelName, metadata := "gte-rerank-v2", `{"operations":["rerank.create"]}`
			if scenario == "embedding" {
				modelName = "text-embedding-v4"

				metadata = `{"operations":["embeddings.create"],"input_modalities":["text"],"output_modalities":["embedding"],"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text/query-document-v1"}}`
			}
			if scenario == "chat" || scenario == "chat-passthrough" {
				modelName = "qwen-plus"

				metadata = `{"operations":["chat.completions.create"]}`
			}

			if scenario == "native-override" {
				metadata = ""
			}
			var sends atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				assert.Equal(t, "Bearer fixture-provider-key", r.Header.Get("Authorization"))
				raw, e := io.ReadAll(r.Body)
				assert.NoError(t, e)
				var body map[string]any
				assert.NoError(t, common.Unmarshal(raw, &body))
				assert.Equal(t, modelName, body["model"])
				if scenario == "chat-passthrough" {
					assert.Contains(t, string(raw), `"vendor_big":9007199254740993`)
				}
				w.Header().Set("Content-Type", "application/json")
				switch scenario {
				case "embedding":
					assert.Equal(t, "/compatible-mode/v1/embeddings", r.URL.Path)
					assert.EqualValues(t, 1024, body["dimensions"])
					assert.Equal(t, "float", body["encoding_format"])
					assert.Equal(t, []any{"hello", "world"}, body["input"])
					assert.NotContains(t, body, "text_type")
					assert.NotContains(t, body, "instruct")
					a, b := make([]float64, 1024), make([]float64, 1024)
					a[0] = 0.25
					b[0] = 0.75
					out, _ := common.Marshal(dto.EmbeddingResponse{Model: modelName, Data: []dto.EmbeddingResponseItem{{Index: 1, Embedding: b}, {Index: 0, Embedding: a}}, Usage: dto.Usage{PromptTokens: 10, TotalTokens: 10}})
					_, _ = w.Write(out)
				case "chat", "chat-passthrough":
					assert.Equal(t, "/compatible-mode/v1/chat/completions", r.URL.Path)
					_, _ = w.Write([]byte(`{"id":"chat","model":"qwen-plus","choices":[{"index":0,"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"total_tokens":10}}`))
				default:
					assert.Equal(t, "/api/v1/services/rerank/text-rerank/text-rerank", r.URL.Path)
					input, ok := body["input"].(map[string]any)
					require.True(t, ok)
					assert.Equal(t, "search", input["query"])
					assert.Equal(t, []any{"first", "second"}, input["documents"])
					params, ok := body["parameters"].(map[string]any)
					require.True(t, ok)
					if scenario == "native-nil" || scenario == "standard-nil" {
						assert.NotContains(t, params, "top_n")
						if scenario == "standard-nil" {
							assert.Equal(t, true, params["return_documents"])
						} else {
							assert.NotContains(t, params, "return_documents")
						}
					} else {
						wantTop := 2
						if scenario == "native-top-one" {
							wantTop = 1
						}
						assert.EqualValues(t, wantTop, params["top_n"])
						assert.Equal(t, scenario == "native-true", params["return_documents"])
					}
					response := `{"request_id":"rank-fixture","output":{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.2}]},"usage":{"total_tokens":10}}`
					if scenario == "invalid-index" {
						response = strings.Replace(response, `"index":1`, `"index":2`, 1)
					}
					if scenario == "duplicate-index" {
						response = strings.Replace(response, `"index":1`, `"index":0`, 1)
					}
					if scenario == "missing-index" {
						response = strings.Replace(response, `"index":0,`, "", 1)
					}
					if scenario == "null-score" {
						response = strings.Replace(response, `0.9`, `null`, 1)
					}
					if scenario == "negative-usage" {
						response = strings.Replace(response, `"total_tokens":10`, `"total_tokens":-10`, 1)
					}
					if scenario == "missing-usage" {
						response = strings.Replace(response, `,"usage":{"total_tokens":10}`, ``, 1)
					}
					if scenario == "native-top-one" {
						response = strings.Replace(response, `,{"index":0,"relevance_score":0.2}`, ``, 1)
					}
					if scenario == "native-true" || scenario == "native" {
						response = strings.Replace(response, `"index":1,`, `"index":1,"document":{"text":"second"},`, 1)
					}
					if scenario == "invalid-score" {
						response = strings.Replace(response, `0.9`, `1e999`, 1)
					}
					_, _ = w.Write([]byte(response))
				}
			}))
			defer upstream.Close()
			service.InitHttpClient()
			service.InitTokenEncoders()
			channel := model.Channel{Id: 903, Type: constant.ChannelTypeAli, Name: "ali-fixture", Key: "fixture-provider-key", BaseURL: &upstream.URL, Models: modelName, Group: "default", Status: common.ChannelStatusEnabled}
			if scenario == "passthrough" {
				channel.Setting = common.GetPointer(`{"pass_through_body_enabled":true}`)
			}
			if scenario == "native-override" {
				channel.ParamOverride = common.GetPointer(`{"model":"unrelated"}`)
			}
			require.NoError(t, db.Create(&channel).Error)
			group := "default"
			if scenario == "channel-group" {
				group = "private"
			}
			require.NoError(t, db.Create(&model.Ability{ChannelId: 903, Group: group, Model: modelName, Enabled: true}).Error)
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 904, PublicId: "knowledge-public", Aliases: `["knowledge-alias"]`, Enabled: true, Metadata: metadata}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 905, RouteId: 904, ChannelId: 903, UpstreamModel: modelName, BillingModel: modelName, PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}).Error)
			path := "/api/v1/services/rerank/text-rerank/text-rerank"
			body := `{"model":"knowledge-alias","input":{"query":"search","documents":["first","second"]},"parameters":{"top_n":2,"return_documents":false}}`
			if scenario == "native-nil" {
				body = `{"model":"knowledge-alias","input":{"query":"search","documents":["first","second"]}}`
			}
			switch scenario {
			case "standard", "standard-nil":
				path = "/v1/rerank"
				body = `{"model":"knowledge-alias","query":"search","documents":["first","second"],"top_n":2,"return_documents":false}`
			case "native-true":
				body = strings.Replace(body, `"return_documents":false`, `"return_documents":true`, 1)
			case "native-top-one":
				body = strings.Replace(body, `"top_n":2`, `"top_n":1`, 1)
			case "embedding":
				path = "/compatible-mode/v1/embeddings"
				body = `{"model":"knowledge-alias","input":["hello","world"],"dimensions":1024,"encoding_format":"float"}`
			case "chat", "chat-passthrough":
				path = "/compatible-mode/v1/chat/completions"
				body = `{"model":"knowledge-alias","messages":[{"role":"user","content":"hello"}]}`
			case "bad-input":
				body = strings.Replace(body, `"query":"search"`, `"query":false`, 1)
			case "null-input":
				body = `{"model":"knowledge-alias","input":null}`
			case "bad-documents":
				body = strings.Replace(body, `["first","second"]`, `["first",42]`, 1)
			case "bad-top-n":
				body = strings.Replace(body, `"top_n":2`, `"top_n":-1`, 1)
			case "zero-top-n":
				body = strings.Replace(body, `"top_n":2`, `"top_n":0`, 1)
			case "over-top-n":
				body = strings.Replace(body, `"top_n":2`, `"top_n":3`, 1)
			case "bad-return-documents":
				body = strings.Replace(body, `"return_documents":false`, `"return_documents":"false"`, 1)
			}
			if scenario == "chat-passthrough" {
				body = strings.TrimSuffix(body, "}") + `,"vendor_big":9007199254740993}`
			}
			if scenario == "standard-nil" {
				body = strings.Replace(body, `,"top_n":2,"return_documents":false`, "", 1)
			}
			engine := gin.New()
			SetRelayRouter(engine)
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "bearer knowledgefixture")
			if scenario == "missing-token" {
				req.Header.Del("Authorization")
			}
			if scenario == "bad-token" {
				req.Header.Set("Authorization", "Bearer invalidfixture")
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			valid := scenario == "native" || scenario == "native-true" || scenario == "native-top-one" || scenario == "standard-nil" || scenario == "native-nil" || scenario == "standard" || scenario == "embedding" || scenario == "chat" || scenario == "chat-passthrough"
			if valid {
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
				assert.EqualValues(t, 1, sends.Load())
				if scenario == "embedding" {
					var out dto.EmbeddingResponse
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &out))
					require.Len(t, out.Data, 2)
					require.Len(t, out.Data[0].Embedding, 1024)
					assert.Equal(t, 0.25, out.Data[0].Embedding[0])
					assert.Equal(t, 0.75, out.Data[1].Embedding[0])
					assert.Equal(t, []int{0, 1}, []int{out.Data[0].Index, out.Data[1].Index})
				} else if scenario == "chat" || scenario == "chat-passthrough" {
					assert.Contains(t, recorder.Body.String(), "ready")
				} else {
					var out struct {
						Output struct {
							Results []dto.RerankResponseResult `json:"results"`
						} `json:"output"`
						Results []dto.RerankResponseResult `json:"results"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &out))
					results := out.Output.Results
					if scenario == "standard" || scenario == "standard-nil" {
						assert.Empty(t, results)
						results = out.Results
					} else {
						assert.Empty(t, out.Results)
					}
					if scenario == "native-top-one" {
						require.Len(t, results, 1)
						assert.Equal(t, 1, results[0].Index)
					} else {
						require.Len(t, results, 2)
						assert.Equal(t, []int{1, 0}, []int{results[0].Index, results[1].Index})
					}
					assert.Equal(t, 0.9, results[0].RelevanceScore)
					if scenario == "native-true" {
						assert.Equal(t, map[string]any{"text": "second"}, results[0].Document)
					} else {
						assert.Nil(t, results[0].Document)
					}
				}
			} else {
				require.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
				assert.Contains(t, recorder.Body.String(), `"error"`)
				if !strings.HasPrefix(scenario, "invalid-") && scenario != "duplicate-index" && scenario != "missing-index" && scenario != "null-score" && scenario != "negative-usage" && scenario != "missing-usage" {
					assert.Zero(t, sends.Load())
				}
			}
			want := 100000
			if valid {
				want -= 10
			}
			require.Eventually(t, func() bool {
				var u model.User
				var tok model.Token
				if db.First(&u, 901).Error != nil || db.First(&tok, 902).Error != nil {
					return false
				}
				return u.Quota == want && tok.RemainQuota == want && tok.UsedQuota == 100000-want
			}, time.Second, time.Millisecond)
			if !valid {
				var count int64
				require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&count).Error)
				assert.Zero(t, count)
			}
		})
	}
}
