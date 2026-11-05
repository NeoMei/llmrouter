package router

import (
	"context"
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
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlatformMeteredStreamReserveFailureNeverDispatches(t *testing.T) {
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	const taskID = "uh_task_00000000-0000-4000-8000-000000000001"
	const callID = "00000000-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		name          string
		reserveStatus int
		reserveBody   string
		wantStatus    int
		wantCode      string
		wantCallState string
	}{
		{name: "insufficient_funds", reserveStatus: 402, reserveBody: `{"error":"insufficient_funds"}`, wantStatus: 402, wantCode: "platform_insufficient_funds", wantCallState: "REJECTED"},
		{name: "insufficient_credits", reserveStatus: 402, reserveBody: `{"error":"insufficient_credits"}`, wantStatus: 402, wantCode: "platform_insufficient_funds", wantCallState: "REJECTED"},
		{name: "empty_402", reserveStatus: 402, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "html_402", reserveStatus: 402, reserveBody: `<html>Payment required</html>`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "unknown_code_402", reserveStatus: 402, reserveBody: `{"error":"payment_required"}`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "nested_error_402", reserveStatus: 402, reserveBody: `{"error":{"code":"insufficient_funds"}}`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "duplicate_error_402", reserveStatus: 402, reserveBody: `{"error":"insufficient_funds","error":"insufficient_funds"}`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "trailing_json_402", reserveStatus: 402, reserveBody: `{"error":"insufficient_funds"}{}`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "wrong_key_402", reserveStatus: 402, reserveBody: `{"Error":"insufficient_funds"}`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "extra_field_402", reserveStatus: 402, reserveBody: `{"error":"insufficient_funds","status":"REJECTED"}`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "service_unavailable", reserveStatus: 503, reserveBody: `{"error":"service_unavailable"}`, wantStatus: 503, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
		{name: "timeout", wantStatus: http.StatusServiceUnavailable, wantCode: "platform_billing_unavailable", wantCallState: "RESERVE_UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var providerCalls, reserves, lookups, receiptVerifies atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				providerCalls.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			t.Cleanup(provider.Close)
			rate := platformbilling.Rate{
				ID: "chat-rate", Model: "platform-test", Asset: "CNY/6", Status: "published", Semantics: "metered_v2",
				Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []metering.Term{
					{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
					{Key: "output", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
				}},
			}
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var out any
				w.Header().Set("Content-Type", "application/json")
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
					if in["purpose"] == "receipt" {
						receiptVerifies.Add(1)
					} else {
						assert.Empty(t, in["purpose"])
					}
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
					if tc.name == "timeout" {
						<-r.Context().Done()
						return
					}
					w.WriteHeader(tc.reserveStatus)
					_, _ = io.WriteString(w, tc.reserveBody)
					return
				case "/internal/v1/calls/" + callID:
					lookups.Add(1)
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{}`)
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
			require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "provider-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: "platform-test", Group: "platform"}).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "platform-test", ChannelId: 1, Enabled: true}).Error)
			adapter, err := platformbilling.New(db, platformbilling.Config{
				Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "default",
				PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192, Timeout: time.Second,
				OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CNY/6": {"platform-test": "chat-rate"}}},
			})
			require.NoError(t, err)
			engine := gin.New()
			SetPlatformRouter(engine, adapter)
			gateway := httptest.NewServer(engine)
			t.Cleanup(gateway.Close)
			send := func() (*http.Response, string) {
				req, err := http.NewRequest(http.MethodPost, gateway.URL+"/platform/v1/chat/completions", strings.NewReader(`{"model":"platform-test","messages":[{"role":"user","content":"hello"}],"max_tokens":50,"stream":true}`))
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer uh_model_task_credential")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-UniHub-Task-ID", taskID)
				req.Header.Set("Idempotency-Key", callID)
				response, err := gateway.Client().Do(req)
				require.NoError(t, err)
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				return response, string(body)
			}
			response, body := send()
			assert.Equal(t, tc.wantStatus, response.StatusCode, body)
			if tc.name == "insufficient_funds" {
				t.Logf("contract_rejection_json=%s", body)
			}
			var problem struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, common.UnmarshalJsonStr(body, &problem))
			assert.Equal(t, tc.wantCode, problem.Error.Code)
			assert.Contains(t, response.Header.Get("Content-Type"), "application/json", "rejection must happen before SSE headers")
			assert.Empty(t, response.Header.Get("X-UniHub-Call-ID"))
			var row platformbilling.Call
			require.NoError(t, db.First(&row, "call_id = ?", callID).Error)
			assert.Equal(t, tc.wantCallState, row.Status)
			assert.Equal(t, "task", row.CredentialKind)
			assert.Equal(t, "quukk", row.AccountID)
			assert.Equal(t, taskID, row.TaskID)
			assert.Equal(t, "chat", row.Operation)
			assert.Equal(t, "chat-rate", row.RateID)
			assert.Equal(t, "prepaid_cash", row.FundingMode)
			duplicate, body := send()
			assert.Equal(t, http.StatusConflict, duplicate.StatusCode, body)
			assert.Contains(t, body, "logical_call_already_exists")
			require.NoError(t, adapter.Recover(context.Background()))
			require.NoError(t, db.First(&row, "call_id = ?", callID).Error)
			assert.Equal(t, tc.wantCallState, row.Status)
			receiptRequest, err := http.NewRequest(http.MethodGet, gateway.URL+"/platform/v1/calls/"+callID, nil)
			require.NoError(t, err)
			receiptRequest.Header.Set("Authorization", "Bearer uh_model_task_credential")
			receiptRequest.Header.Set("X-UniHub-Task-ID", taskID)
			receiptResponse, err := gateway.Client().Do(receiptRequest)
			require.NoError(t, err)
			defer receiptResponse.Body.Close()
			assert.Equal(t, http.StatusOK, receiptResponse.StatusCode)
			var receipt platformbilling.Receipt
			receiptBody, err := io.ReadAll(receiptResponse.Body)
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(receiptBody, &receipt))
			if tc.name == "insufficient_funds" {
				t.Logf("contract_receipt_json=%s", receiptBody)
			}
			assert.Equal(t, platformbilling.Receipt{CallID: callID, Status: tc.wantCallState, Asset: "CNY/6"}, receipt)
			assert.EqualValues(t, 1, receiptVerifies.Load(), "receipt must revalidate the task credential")
			assert.EqualValues(t, 1, reserves.Load(), "duplicate admission and recovery must not reserve again")
			assert.Zero(t, providerCalls.Load())
			var attempts int64
			require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
			assert.Zero(t, attempts)
			if tc.wantCallState == "REJECTED" {
				assert.Zero(t, lookups.Load())
			} else {
				assert.EqualValues(t, 1, lookups.Load())
			}
		})
	}
}
