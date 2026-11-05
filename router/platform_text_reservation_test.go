package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlatformLegacyChatTextReservationHTTP(t *testing.T) {
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	const taskID = "uh_task_00000000-0000-4000-8000-000000000001"
	const callID = "00000000-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		name        string
		bodyBytes   int
		policy      bool
		wantStatus  int
		wantCode    string
		wantReserve int64
	}{
		{name: "large_tool_schema_reaches_reserve", bodyBytes: 99750, policy: true, wantStatus: http.StatusPaymentRequired, wantCode: "platform_insufficient_funds", wantReserve: 1},
		{name: "default_input_limit_remains", bodyBytes: 99750, wantStatus: http.StatusBadRequest, wantCode: "platform_billing_unavailable"},
		{name: "certified_input_limit_exceeded", bodyBytes: 131073, policy: true, wantStatus: http.StatusBadRequest, wantCode: "platform_billing_unavailable"},
		{name: "transport_limit_exceeded", bodyBytes: (1 << 20) + 1, policy: true, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "platform_request_body_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var providerCalls, reserves atomic.Int64
			reserveAmounts := make(chan string, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				providerCalls.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			t.Cleanup(provider.Close)
			rate := platformbilling.Rate{
				ID: "chat-rate", Model: "platform-test", Asset: "CNY/6", Status: "published", Semantics: "exclusive_v1",
				Input: platformbilling.Rational{Numerator: "1", Denominator: "1"}, Output: platformbilling.Rational{Numerator: "1", Denominator: "1"},
				CacheRead: platformbilling.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: platformbilling.Rational{Numerator: "0", Denominator: "1"},
				Reasoning: platformbilling.Rational{Numerator: "0", Denominator: "1"},
			}
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var out any
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					var in map[string]string
					if !assert.NoError(t, common.DecodeJson(r.Body, &in)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, "uh_model_task_credential", in["token"])
					assert.Equal(t, "chat.completions.create", in["wire_operation"])
					assert.Equal(t, "default", in["instance_id"])
					out = platformbilling.Claims{
						CredentialKind: "task", ID: "task-credential", InstanceID: "default", Audience: "llmrouter",
						ActorID: "member", AccountID: "quukk", OriginProductID: "superwagie", TaskID: taskID,
						Models: []string{"platform-test"}, Operations: map[string][]string{"platform-test": {"chat.completions.create"}},
						Asset: "CNY/6", FundingMode: "prepaid_cash", BillingProfileVersion: 1, ExpiresAt: time.Now().Add(time.Hour),
					}
				case "/internal/v1/rates":
					out = []platformbilling.Rate{rate}
				case "/internal/v1/calls/reserve":
					reserves.Add(1)
					var in map[string]string
					if !assert.NoError(t, common.DecodeJson(r.Body, &in)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, "uh_model_task_credential", in["credential"])
					assert.Equal(t, taskID, in["task_id"])
					assert.Equal(t, callID, in["call_id"])
					assert.Equal(t, callID, r.Header.Get("Idempotency-Key"))
					assert.Equal(t, "chat-rate", in["rate_id"])
					assert.Equal(t, "chat.completions.create", in["wire_operation"])
					select {
					case reserveAmounts <- in["amount"]:
					default:
						t.Error("duplicate reserve request")
					}
					w.WriteHeader(http.StatusPaymentRequired)
					_, _ = io.WriteString(w, `{"error":"insufficient_funds"}`)
					return
				default:
					t.Errorf("unexpected UniHub operation %s", r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				body, err := common.Marshal(out)
				if assert.NoError(t, err) {
					_, _ = w.Write(body)
				}
			}))
			t.Cleanup(hub.Close)
			db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}))
			previousDB, previousLog := model.DB, model.LOG_DB
			model.DB, model.LOG_DB = db, db
			t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLog })
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "platform", Status: common.UserStatusEnabled, Group: "platform"}).Error)
			base := provider.URL
			require.NoError(t, db.Create(&model.Channel{Id: 3, Type: constant.ChannelTypeDeepSeek, Key: "provider-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: "platform-test", Group: "platform"}).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "platform-test", ChannelId: 3, Enabled: true}).Error)
			config := platformbilling.Config{
				Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "default",
				PrincipalID: 1, Group: "platform", MaxInputTokens: 65536, MaxOutputTokens: 4096, MaxBodyBytes: 1 << 20, Timeout: time.Second,
				AssetRateIDs: map[string]map[string]string{"CNY/6": {"platform-test": "chat-rate"}},
			}
			if tc.policy {
				config.TextInputPolicies = map[string]map[string]platformbilling.TextInputPolicy{
					"chat.completions.create": {"platform-test": {
						Version: "full_body_bytes_v1", ChannelID: 3, ChannelType: constant.ChannelTypeDeepSeek, UpstreamModel: "platform-test",
						Endpoint: "https://api.deepseek.com/v1/chat/completions", MaxInputTokens: 262144, ContextTokens: 1048576, MaxOutputTokens: 4096,
						ProviderReference: "https://api-docs.deepseek.com/quick_start/pricing",
					}},
				}
			}
			adapter, err := platformbilling.New(db, config)
			require.NoError(t, err)
			engine := gin.New()
			SetPlatformRouter(engine, adapter)
			gateway := httptest.NewServer(engine)
			t.Cleanup(gateway.Close)

			property := map[string]any{"type": "string", "description": ""}
			payload := map[string]any{
				"model": "platform-test", "max_tokens": 4096, "stream": true,
				"messages": []any{map[string]any{"role": "system", "content": strings.Repeat("s", 4096)}, map[string]any{"role": "user", "content": "Use the supplied tool."}},
				"tools": []any{map[string]any{"type": "function", "function": map[string]any{
					"name": "lookup", "description": "Look up a document", "parameters": map[string]any{
						"type": "object", "properties": map[string]any{"query": property}, "required": []string{"query"},
					},
				}}},
			}
			body, err := common.Marshal(payload)
			require.NoError(t, err)
			require.Greater(t, tc.bodyBytes, len(body))
			property["description"] = strings.Repeat("x", tc.bodyBytes-len(body))
			body, err = common.Marshal(payload)
			require.NoError(t, err)
			require.Len(t, body, tc.bodyBytes)
			req, err := http.NewRequest(http.MethodPost, gateway.URL+"/platform/v1/chat/completions", bytes.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer uh_model_task_credential")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-UniHub-Task-ID", taskID)
			req.Header.Set("Idempotency-Key", callID)
			response, err := gateway.Client().Do(req)
			require.NoError(t, err)
			defer response.Body.Close()
			responseBody, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, response.StatusCode, string(responseBody))
			var problem struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, common.Unmarshal(responseBody, &problem))
			assert.Equal(t, tc.wantCode, problem.Error.Code)
			assert.Contains(t, response.Header.Get("Content-Type"), "application/json")
			assert.Empty(t, response.Header.Get("X-UniHub-Call-ID"))
			assert.Equal(t, tc.wantReserve, reserves.Load())
			assert.Zero(t, providerCalls.Load())
			var attempts int64
			require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
			assert.Zero(t, attempts)
			var calls []platformbilling.Call
			require.NoError(t, db.Find(&calls).Error)
			if tc.wantReserve == 0 {
				assert.Empty(t, calls, "input rejection must precede the journal and reservation")
				return
			}
			require.Len(t, calls, 1)
			select {
			case amount := <-reserveAmounts:
				assert.Equal(t, "203596", amount)
			default:
				t.Fatal("central reserve amount was not captured")
			}
			assert.Equal(t, "REJECTED", calls[0].Status)
			assert.Equal(t, "203596", calls[0].Amount)
			assert.EqualValues(t, 199500, calls[0].ReservationQuantity)
			assert.EqualValues(t, 4096, calls[0].ReservationOutput)
			assert.NotEmpty(t, calls[0].TextInputSnapshot)
		})
	}
}
