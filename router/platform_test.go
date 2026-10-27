package router

import (
	"bytes"
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This fixture crosses the real router, distributor, controller, OpenAI adaptor,
// UniHub client and SQLite lifecycle. Local free pricing must not bypass credits.
func TestPlatformChatUsesFixedPayerAndNeverDebitsLegacy(t *testing.T) {
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	oldStreaming := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	defer func() { constant.StreamingTimeout = oldStreaming }()
	scenarios := []string{"redirect", "retry_veto", "retry_authorization_changed", "settle_response_loss", "success", "provider_failure", "missing_usage", "partial_stream", "retry", "function_schema", "function_strict_true", "function_strict_false"}
	invalidUsage := map[string]string{
		"missing_prompt":     `{"completion_tokens":5,"total_tokens":5}`,
		"missing_completion": `{"prompt_tokens":10,"total_tokens":10}`,
		"missing_total":      `{"prompt_tokens":10,"completion_tokens":5}`,
		"null_prompt":        `{"prompt_tokens":null,"completion_tokens":5,"total_tokens":5}`,
		"fraction_prompt":    `{"prompt_tokens":10.5,"completion_tokens":5,"total_tokens":15}`,
		"overflow_prompt":    `{"prompt_tokens":9223372036854775808,"completion_tokens":5,"total_tokens":15}`,
		"exponent_prompt":    `{"prompt_tokens":1e1,"completion_tokens":5,"total_tokens":15}`,
		"negative_prompt":    `{"prompt_tokens":-1,"completion_tokens":5,"total_tokens":4}`,
		"duplicate_prompt":   `{"prompt_tokens":10,"prompt_tokens":1,"completion_tokens":5,"total_tokens":15}`,
		"alias_prompt":       `{"prompt_tokens":10,"PROMPT_TOKENS":1,"completion_tokens":5,"total_tokens":15}`,
		"null_usage":         `null`,
		"fraction_cache":     `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":1.5}}`,
	}
	invalidRequests := map[string]string{
		"enable_search":            `"max_tokens":50,"enable_search":true`,
		"enable_search_false":      `"max_tokens":50,"enable_search":false`,
		"web_search":               `"max_tokens":50,"web_search":true`,
		"search_parameters":        `"max_tokens":50,"search_parameters":{"mode":"on"}`,
		"search_mode":              `"max_tokens":50,"search_mode":"pro"`,
		"web_search_options":       `"max_tokens":50,"web_search_options":{"search_context_size":"high"}`,
		"search_domain_filter":     `"max_tokens":50,"search_domain_filter":["example.com"]`,
		"search_recency_filter":    `"max_tokens":50,"search_recency_filter":"day"`,
		"return_images":            `"max_tokens":50,"return_images":true`,
		"return_related_questions": `"max_tokens":50,"return_related_questions":true`,
		"service_tier":             `"max_tokens":50,"service_tier":"priority"`,
		"speed":                    `"max_tokens":50,"speed":"fast"`,
		"inference_geo":            `"max_tokens":50,"inference_geo":"us"`,
		"prediction":               `"max_tokens":50,"prediction":{"type":"content","content":"predicted"}`,
		"prompt_cache_retention":   `"max_tokens":50,"prompt_cache_retention":"24h"`,
		"max_alias":                `"max_tokens":50,"MAX_TOKENS":1000`,
		"max_completion_alias":     `"max_completion_tokens":50,"MAX_COMPLETION_TOKENS":1000`,
		"max_duplicate":            `"max_tokens":1000,"max_tokens":50`,
		"n_alias":                  `"max_tokens":50,"N":10`,
		"messages_alias":           `"max_tokens":50,"MESSAGES":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]`,
		"max_go_name":              `"max_tokens":50,"MaxTokens":1000`,
		"unknown":                  `"max_tokens":50,"new_untrusted_option":1000`,
		"max_fraction":             `"max_tokens":50.5`,
		"max_overflow":             `"max_tokens":18446744073709551616`,
		"max_nested_stream_alias":  `"max_tokens":50,"stream_options":{"include_usage":true,"IncludeUsage":false}`,
		"message_content_alias":    `"max_tokens":50,"messages":[{"role":"user","content":"text","Content":[{"type":"image_url"}]}]`,
		"tools_alias":              `"max_tokens":50,"TOOLS":[{"type":"web_search"}]`,
		"modalities_alias":         `"max_tokens":50,"MODALITIES":["audio"]`,
		"nested_content_alias":     `"max_tokens":50,"tools":[{"type":"function","function":{"name":"x","NAME":"y"}}]`,
		"nested_duplicate":         `"max_tokens":50,"tools":[{"type":"function","function":{"name":"x","parameters":{"type":"object","properties":{"x":{"type":"string","type":"number"}}}}}]`,
		"strict_string":            `"max_tokens":50,"tools":[{"type":"function","function":{"name":"x","strict":"false"}}]`,
		"strict_number":            `"max_tokens":50,"tools":[{"type":"function","function":{"name":"x","strict":0}}]`,
		"strict_null":              `"max_tokens":50,"tools":[{"type":"function","function":{"name":"x","strict":null}}]`,
		"strict_alias":             `"max_tokens":50,"tools":[{"type":"function","function":{"name":"x","Strict":false}}]`,
		"strict_duplicate":         `"max_tokens":50,"tools":[{"type":"function","function":{"name":"x","strict":true,"strict":false}}]`,
	}
	validOptional := map[string]string{"optional_null": `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":null,"completion_tokens_details":{"reasoning_tokens":null}}`, "optional_missing": `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`, "explicit_zero": `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`}
	for name := range validOptional {
		scenarios = append(scenarios, "valid_json_"+name, "valid_sse_"+name)
	}
	for name := range invalidUsage {
		scenarios = append(scenarios, "usage_json_"+name, "usage_sse_"+name)
	}
	for name := range invalidRequests {
		scenarios = append(scenarios, "request_"+name)
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			upstreamCalls := 0
			settled := 0
			released := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls++
				assert.Equal(t, "/v1/chat/completions", r.URL.Path)
				assert.Equal(t, "Bearer provider-key", r.Header.Get("Authorization"))
				var wire map[string]any
				require.NoError(t, common.DecodeJson(r.Body, &wire))
				max := wire["max_tokens"].(float64)
				assert.LessOrEqual(t, max, float64(100))
				assert.Equal(t, float64(50), max)
				if strings.HasPrefix(scenario, "function_strict_") {
					requestTools, ok := wire["tools"].([]any)
					require.True(t, ok)
					require.Len(t, requestTools, 1)
					function := requestTools[0].(map[string]any)["function"].(map[string]any)
					strict, exists := function["strict"]
					assert.True(t, exists, "explicit false must survive request conversion")
					assert.Equal(t, scenario == "function_strict_true", strict)
				}
				if scenario == "redirect" && upstreamCalls == 1 {
					w.Header().Set("Location", "/v1/chat/completions")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				if scenario == "retry_veto" {
					w.Header().Set("X-Should-Retry", "false")
				}
				if scenario == "retry_authorization_changed" && upstreamCalls == 1 {
					require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 1).Update("status", common.ChannelStatusManuallyDisabled).Error)
					model.InitChannelCache()
				}
				if scenario == "provider_failure" || scenario == "retry_veto" || scenario == "retry_authorization_changed" || scenario == "retry" && upstreamCalls == 1 {
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable","type":"server_error"}}`))
					return
				}
				if scenario == "partial_stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
					return
				}
				if strings.HasPrefix(scenario, "valid_sse_") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"x\",\"choices\":[],\"usage\":" + validOptional[strings.TrimPrefix(scenario, "valid_sse_")] + "}\n\ndata: [DONE]\n\n"))
					return
				}
				if strings.HasPrefix(scenario, "usage_sse_") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: {\"id\":\"x\",\"choices\":[],\"usage\":" + invalidUsage[strings.TrimPrefix(scenario, "usage_sse_")] + "}\n\ndata: [DONE]\n\n"))
					return
				}
				usage := `,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}`
				if strings.HasPrefix(scenario, "usage_json_") {
					usage = `,"usage":` + invalidUsage[strings.TrimPrefix(scenario, "usage_json_")]
				}
				if strings.HasPrefix(scenario, "valid_json_") {
					usage = `,"usage":` + validOptional[strings.TrimPrefix(scenario, "valid_json_")]
				}
				if scenario == "missing_usage" {
					usage = ""
				}
				_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]` + usage + `}`))
			}))
			defer up.Close()
			var reserved map[string]string
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var v any
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					v = platformbilling.Claims{ID: "cred", Audience: "llmrouter", ActorID: "actor", AccountID: "fixed-account", OriginProductID: "product", Models: []string{"platform-test"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
				case "/internal/v1/rates":
					v = []platformbilling.Rate{{ID: "rate", Model: "platform-test", Status: "published", Semantics: "exclusive_v1", Input: platformbilling.Rational{Numerator: "1", Denominator: "1"}, Output: platformbilling.Rational{Numerator: "1", Denominator: "1"}, CacheRead: platformbilling.Rational{Numerator: "1", Denominator: "1"}, CacheWrite: platformbilling.Rational{Numerator: "1", Denominator: "1"}, Reasoning: platformbilling.Rational{Numerator: "1", Denominator: "1"}}}
				case "/internal/v1/calls/reserve":
					require.NoError(t, common.DecodeJson(r.Body, &reserved))
					v = platformbilling.RemoteCall{CallID: "call", Status: "RESERVED"}
				case "/internal/v1/calls/call/settle":
					settled++
					var body struct {
						Usage        platformbilling.Usage `json:"usage"`
						ProviderCost string                `json:"provider_cost"`
					}
					require.NoError(t, common.DecodeJson(r.Body, &body))
					expectedUsage := platformbilling.Usage{Input: "8", Output: "4", CacheRead: "2", CacheWrite: "0", Reasoning: "1"}
					if strings.HasPrefix(scenario, "valid_") {
						expectedUsage = platformbilling.Usage{Input: "10", Output: "5", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
						if strings.HasSuffix(scenario, "explicit_zero") {
							expectedUsage.Input = "0"
							expectedUsage.Output = "0"
						}
					}
					assert.Equal(t, expectedUsage, body.Usage)
					assert.Empty(t, body.ProviderCost)
					if scenario == "settle_response_loss" && settled == 1 {
						w.Header().Set("Content-Length", "10000")
						w.WriteHeader(http.StatusOK)
						_, err := w.Write([]byte("{"))
						require.NoError(t, err)
						w.(http.Flusher).Flush()
						connection, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						require.NoError(t, connection.Close())
						return
					}
					v = platformbilling.RemoteCall{CallID: "call", Status: "SETTLED"}
				case "/internal/v1/calls/call/release":
					released++
					v = platformbilling.RemoteCall{CallID: "call", Status: "RELEASED"}
				default:
					t.Errorf("unexpected Hub path %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				if remote, ok := v.(platformbilling.RemoteCall); ok {
					remote.ActorID = "actor"
					remote.AccountID = "fixed-account"
					remote.OriginProductID = "product"
					remote.RequestDigest = reserved["request_digest"]
					remote.Rate = platformbilling.Rate{ID: "rate", Model: "platform-test"}
					v = remote
				}
				b, e := common.Marshal(v)
				require.NoError(t, e)
				_, _ = w.Write(b)
			}))
			defer hub.Close()
			db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
			require.NoError(t, e)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
			oldDB, oldLog, oldCache, oldRetry := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RetryTimes
			oldRedis := common.RedisEnabled
			common.RedisEnabled = false
			model.DB = db
			oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			t.Setenv("LOG_SQL_DSN", "")
			// InitLogDB initializes dialect quoting used by legacy token lookup;
			// opening a test GORM DB alone does not initialize model columns.
			require.NoError(t, model.InitLogDB())
			defer func() { common.SetMainDatabaseType(oldMainType); common.SetLogDatabaseType(oldLogType) }()
			model.LOG_DB = db
			common.MemoryCacheEnabled = true
			common.RetryTimes = 1
			defer func() {
				perfmetrics.DrainRelaySamples()
				model.DB = oldDB
				model.LOG_DB = oldLog
				common.MemoryCacheEnabled = oldCache
				common.RetryTimes = oldRetry
				common.RedisEnabled = oldRedis
			}()
			user := model.User{Id: 1, Username: "platform-principal", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, RequestCount: 3, Group: "platform"}
			require.NoError(t, db.Create(&user).Error)
			token := model.Token{Id: 1, UserId: 1, Key: "legacy-token", RemainQuota: 4567, UsedQuota: 9}
			require.NoError(t, db.Create(&token).Error)
			sub := model.UserSubscription{Id: 1, UserId: 1, PlanId: 1, AmountTotal: 1000, AmountUsed: 123, StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix(), Status: "active"}
			require.NoError(t, db.Create(&sub).Error)
			base := up.URL
			channel := model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "provider-key", Status: common.ChannelStatusEnabled, Name: "platform", BaseURL: &base, Models: "platform-test", Group: "platform"}
			if scenario == "retry_veto" || scenario == "retry_authorization_changed" {
				channel.Key = "provider-key\nprovider-backup"
				channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling}
			}
			require.NoError(t, db.Create(&channel).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "platform-test", ChannelId: 1, Enabled: true}).Error)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"platform-test":0}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
			model.InitChannelCache()
			service.InitHttpClient()
			if scenario == "redirect" {
				service.GetHttpClient().CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
			}
			service.InitTokenEncoders()
			a, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
			require.NoError(t, e)
			engine := gin.New()
			engine.Use(middlewareBodyCleanupForTest())
			SetPlatformRouter(engine, a)
			body := []byte(`{"model":"platform-test","messages":[{"role":"user","content":"hello"}],"max_tokens":50}`)
			if scenario == "partial_stream" || (strings.HasPrefix(scenario, "usage_sse_") || strings.HasPrefix(scenario, "valid_sse_")) {
				body = []byte(`{"model":"platform-test","messages":[{"role":"user","content":"hello"}],"max_tokens":50,"stream":true}`)
			}
			if scenario == "function_schema" {
				body = []byte(`{"model":"platform-test","messages":[{"role":"user","content":"hello"}],"max_tokens":50,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"CustomerID":{"type":"string"}}}}}]}`)
			}
			if strings.HasPrefix(scenario, "function_strict_") {
				strict := "false"
				if scenario == "function_strict_true" {
					strict = "true"
				}
				body = []byte(`{"model":"platform-test","messages":[{"role":"user","content":"hello"}],"max_tokens":50,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"CustomerID":{"type":"string"}}},"strict":` + strict + `}}]}`)
			}
			send := func(payload []byte) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/platform/v1/chat/completions", bytes.NewReader(payload))
				r.Header.Set("Authorization", "Bearer uh_model_token")
				r.Header.Set("Idempotency-Key", "call")
				r.Header.Set("X-UniHub-Task-ID", "task")
				r.Header.Set("X-Account-ID", "attacker")
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, r)
				return w
			}
			if fragment, ok := invalidRequests[strings.TrimPrefix(scenario, "request_")]; strings.HasPrefix(scenario, "request_") && ok {
				prefix := `{"model":"platform-test","messages":[{"role":"user","content":"hello"}],`
				if strings.Contains(fragment, `"messages":`) {
					prefix = `{"model":"platform-test",`
				}
				body = []byte(prefix + fragment + `}`)
				w := send(body)
				assert.Equal(t, 400, w.Code, w.Body.String())
				assert.Zero(t, upstreamCalls)
				assert.Zero(t, settled)
				assert.Zero(t, released)
				assert.Nil(t, reserved, "invalid fields must fail before finance")
				var n int64
				require.NoError(t, db.Model(&platformbilling.Call{}).Count(&n).Error)
				assert.Zero(t, n)
				var unchangedUser model.User
				var unchangedToken model.Token
				var unchangedSubscription model.UserSubscription
				require.NoError(t, db.First(&unchangedUser, user.Id).Error)
				require.NoError(t, db.First(&unchangedToken, token.Id).Error)
				require.NoError(t, db.First(&unchangedSubscription, sub.Id).Error)
				assert.Equal(t, user.Quota, unchangedUser.Quota)
				assert.Equal(t, user.UsedQuota, unchangedUser.UsedQuota)
				assert.Equal(t, user.RequestCount, unchangedUser.RequestCount)
				assert.Equal(t, token.RemainQuota, unchangedToken.RemainQuota)
				assert.Equal(t, token.UsedQuota, unchangedToken.UsedQuota)
				assert.Equal(t, sub.AmountUsed, unchangedSubscription.AmountUsed)
				assert.Equal(t, sub.AmountTotal, unchangedSubscription.AmountTotal)
				return
			}
			if scenario == "success" {
				legacy := gin.New()
				SetRelayRouter(legacy)
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer uh_model_token")
				req.Header.Set("Content-Type", "application/json")
				rejected := httptest.NewRecorder()
				legacy.ServeHTTP(rejected, req)
				assert.Equal(t, http.StatusForbidden, rejected.Code, rejected.Body.String())
				assert.Zero(t, upstreamCalls, "a model grant must not fall back to local quota")
				assert.Nil(t, reserved, "legacy authentication must not reserve platform funds")
			}
			w := send(body)
			assert.Equal(t, "call", w.Header().Get("X-UniHub-Call-ID"))
			if scenario == "provider_failure" || scenario == "redirect" || scenario == "retry_veto" || scenario == "retry_authorization_changed" {
				assert.GreaterOrEqual(t, w.Code, 400)
			} else if !strings.HasPrefix(scenario, "usage_") {
				assert.Equal(t, 200, w.Code, w.Body.String())
			}
			expected := 1
			if scenario == "retry" || scenario == "provider_failure" {
				expected = 2
			}
			assert.Equal(t, expected, upstreamCalls, w.Body.String())
			before := upstreamCalls
			assert.Equal(t, 409, send(body).Code)
			assert.Equal(t, before, upstreamCalls)
			var persisted platformbilling.Call
			require.NoError(t, db.First(&persisted).Error)
			assert.Equal(t, "fixed-account", persisted.AccountID)
			if scenario == "settle_response_loss" {
				assert.Equal(t, "SETTLE_PENDING", persisted.Status)
				assert.Equal(t, 1, settled)
				restarted, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
				require.NoError(t, err)
				require.NoError(t, restarted.Recover(context.Background()))
				require.NoError(t, db.First(&persisted).Error)
				assert.Equal(t, "SETTLED", persisted.Status)
				assert.Equal(t, 2, settled)
				assert.Equal(t, 409, send(body).Code)
				assert.Equal(t, before, upstreamCalls, "recovery must resend only the terminal intent")
			} else if scenario == "success" || scenario == "retry" || scenario == "function_schema" || strings.HasPrefix(scenario, "function_strict_") || strings.HasPrefix(scenario, "valid_") {
				assert.Equal(t, "SETTLED", persisted.Status)
				assert.Equal(t, 1, settled)
			} else if scenario == "provider_failure" || scenario == "redirect" || scenario == "retry_veto" || scenario == "retry_authorization_changed" {
				assert.Equal(t, "RELEASED", persisted.Status)
				assert.Equal(t, 1, released)
			} else {
				assert.Equal(t, "UNKNOWN", persisted.Status)
				assert.Zero(t, settled)
				assert.Zero(t, released)
				if strings.HasPrefix(scenario, "usage_sse_") {
					var attempt platformbilling.Attempt
					require.NoError(t, db.First(&attempt).Error)
					assert.NotEqual(t, "USAGE_CONFIRMED", attempt.Status)
				}
			}
			var afterUser model.User
			var afterToken model.Token
			require.NoError(t, db.First(&afterUser, 1).Error)
			require.NoError(t, db.First(&afterToken, 1).Error)
			assert.Equal(t, user.Quota, afterUser.Quota)
			assert.Equal(t, user.UsedQuota, afterUser.UsedQuota)
			assert.Equal(t, user.RequestCount, afterUser.RequestCount)
			assert.Equal(t, token.RemainQuota, afterToken.RemainQuota)
			assert.Equal(t, token.UsedQuota, afterToken.UsedQuota)
			var afterSub model.UserSubscription
			require.NoError(t, db.First(&afterSub, 1).Error)
			assert.Equal(t, sub.AmountUsed, afterSub.AmountUsed)
			assert.Equal(t, sub.AmountTotal, afterSub.AmountTotal)
		})
	}
}
func middlewareBodyCleanupForTest() gin.HandlerFunc {
	return func(c *gin.Context) { c.Next(); common.CleanupBodyStorage(c) }
}

func TestDisabledPlatformLeavesLegacyRouteUntouched(t *testing.T) {
	engine := gin.New()
	engine.POST("/v1/chat/completions", func(c *gin.Context) { c.JSON(200, gin.H{"legacy": true}) })
	adapter, e := platformbilling.New(nil, platformbilling.Config{})
	require.NoError(t, e)
	SetPlatformRouter(engine, adapter)
	for _, path := range []string{"/platform/v1/chat/completions", "/platform/v1/calls/call"} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		assert.Equal(t, 404, w.Code)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	assert.Equal(t, 200, w.Code)
	assert.JSONEq(t, `{"legacy":true}`, w.Body.String())
}
