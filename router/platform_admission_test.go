package router

import (
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

func enableUniHubBoundary(t *testing.T) {
	t.Helper()
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "127.0.0.1:3001")
}

func TestPlatformAdmissionRejectsWithoutNativeSideEffects(t *testing.T) {
	enableUniHubBoundary(t)
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"missing_grant", 401}, {"native_credential", 401}, {"duplicate_authorization", 401},
		{"unavailable", 503}, {"revoked", 403}, {"wrong_audience", 400}, {"expired", 400}, {"missing_claims", 400},
		{"wrong_model", 400}, {"wrong_task", 400}, {"missing_account", 400},
		{"missing_rate", 400}, {"ambiguous_rate", 400}, {"wrong_reserved_account", 503}, {"wrong_reserved_model", 503},
		{"platform_policy_disabled", 503}, {"invalid_policy", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var providerCalls, nativeWrites, verifyCalls atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { providerCalls.Add(1); w.WriteHeader(502) }))
			defer provider.Close()
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var value any
				rate := platformbilling.Rate{ID: "rate", Model: "platform-test", Status: "published", Semantics: "exclusive_v1", Input: platformbilling.Rational{Numerator: "1", Denominator: "1"}, Output: platformbilling.Rational{Numerator: "1", Denominator: "1"}, CacheRead: platformbilling.Rational{Numerator: "1", Denominator: "1"}, CacheWrite: platformbilling.Rational{Numerator: "1", Denominator: "1"}, Reasoning: platformbilling.Rational{Numerator: "1", Denominator: "1"}}
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					verifyCalls.Add(1)
					if tc.name == "unavailable" {
						w.WriteHeader(503)
						return
					}
					if tc.name == "revoked" {
						w.WriteHeader(403)
						return
					}
					grant := platformbilling.Claims{ID: "grant", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "product", Models: []string{"platform-test"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
					switch tc.name {
					case "wrong_audience":
						grant.Audience = "other"
					case "expired":
						grant.ExpiresAt = time.Now().Add(-time.Hour)
					case "missing_claims", "platform_policy_disabled", "invalid_policy":
						grant = platformbilling.Claims{}
					case "wrong_model":
						grant.Models = []string{"other"}
					case "wrong_task":
						grant.TaskID = "other"
					case "missing_account":
						grant.AccountID = ""
					}
					value = grant
				case "/internal/v1/rates":
					rates := []platformbilling.Rate{rate}
					if tc.name == "missing_rate" {
						rates = nil
					}
					if tc.name == "ambiguous_rate" {
						rates = append(rates, rate)
					}
					value = rates
				case "/internal/v1/calls/reserve":
					var body map[string]string
					require.NoError(t, common.DecodeJson(r.Body, &body))
					remote := platformbilling.RemoteCall{CallID: body["call_id"], Status: "RESERVED", ActorID: "actor", AccountID: "account", OriginProductID: "product", RequestDigest: body["request_digest"], Rate: rate}
					if tc.name == "wrong_reserved_account" {
						remote.AccountID = "attacker"
					}
					if tc.name == "wrong_reserved_model" {
						remote.Rate.Model = "other"
					}
					value = remote
				default:
					t.Errorf("unexpected UniHub operation %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				data, err := common.Marshal(value)
				require.NoError(t, err)
				_, _ = w.Write(data)
			}))
			defer hub.Close()
			db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}, &model.TopUp{}, &model.Channel{}, &model.Ability{}))
			previousDB, previousLog := model.DB, model.LOG_DB
			model.DB, model.LOG_DB = db, db
			defer func() { model.DB, model.LOG_DB = previousDB, previousLog }()
			user := model.User{Id: 1, Username: "native-funded-user", Status: common.UserStatusEnabled, Group: "platform", Quota: 100000, UsedQuota: 9, RequestCount: 3}
			token := model.Token{Id: 1, UserId: 1, Key: "native-funded-token", Status: common.TokenStatusEnabled, RemainQuota: 100000, UsedQuota: 7}
			subscription := model.UserSubscription{Id: 1, UserId: 1, AmountTotal: 10000, AmountUsed: 5, Status: "active"}
			topup := model.TopUp{Id: 1, UserId: 1, Amount: 10, Money: 10, TradeNo: "unchanged-wallet"}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&token).Error)
			require.NoError(t, db.Create(&subscription).Error)
			require.NoError(t, db.Create(&topup).Error)
			base := provider.URL
			require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "provider-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: "platform-test", Group: "platform"}).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "platform-test", ChannelId: 1, Enabled: true}).Error)
			a, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
			require.NoError(t, err)
			observe := func(tx *gorm.DB) {
				switch tx.Statement.Table {
				case "users", "tokens", "user_subscriptions", "top_ups":
					nativeWrites.Add(1)
				}
			}
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:native-write", observe))
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:native-write", observe))
			require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:native-write", observe))
			if tc.name == "platform_policy_disabled" {
				t.Setenv("UNIHUB_PLATFORM_ENABLED", "false")
			}
			if tc.name == "invalid_policy" {
				t.Setenv("UNIHUB_PRODUCT_ENABLED", "bad")
			}
			engine := gin.New()
			SetRelayRouter(engine)
			SetPlatformRouter(engine, a)
			req := httptest.NewRequest("POST", "/platform/v1/chat/completions", strings.NewReader(`{"model":"platform-test","messages":[{"role":"user","content":"test"}],"max_tokens":50}`))
			req.Header.Set("Authorization", "Bearer uh_model_grant")
			req.Header.Set("X-UniHub-Task-ID", "task")
			req.Header.Set("Idempotency-Key", "call")
			req.Header.Set("X-Account-ID", "native-account")
			req.Header.Set("New-Api-User", "1")
			req.Header.Set("X-Forwarded-Host", "127.0.0.1:3001")
			switch tc.name {
			case "missing_grant":
				req.Header.Del("Authorization")
			case "native_credential":
				req.Header.Set("Authorization", "Bearer native-funded-token")
			case "duplicate_authorization":
				req.Header.Add("Authorization", "Bearer native-funded-token")
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			assert.Equal(t, tc.status, w.Code, w.Body.String())
			assert.Zero(t, providerCalls.Load())
			assert.Zero(t, nativeWrites.Load(), "rejection must never reach native quota/wallet mutation")
			var attempts int64
			require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
			assert.Zero(t, attempts)
			var afterUser model.User
			var afterToken model.Token
			var afterSub model.UserSubscription
			var afterTopup model.TopUp
			require.NoError(t, db.First(&afterUser, 1).Error)
			require.NoError(t, db.First(&afterToken, 1).Error)
			require.NoError(t, db.First(&afterSub, 1).Error)
			require.NoError(t, db.First(&afterTopup, 1).Error)
			assert.Equal(t, user.Quota, afterUser.Quota)
			assert.Equal(t, user.UsedQuota, afterUser.UsedQuota)
			assert.Equal(t, user.RequestCount, afterUser.RequestCount)
			assert.Equal(t, token.RemainQuota, afterToken.RemainQuota)
			assert.Equal(t, token.UsedQuota, afterToken.UsedQuota)
			assert.Equal(t, subscription.AmountUsed, afterSub.AmountUsed)
			assert.Equal(t, topup, afterTopup)
			if strings.Contains(tc.name, "policy") {
				assert.Zero(t, verifyCalls.Load(), "policy admission must precede remote authorization")
			}
		})
	}
}

func TestPlatformOperationsAndTaskReadsRespectDisabledPolicy(t *testing.T) {
	enableUniHubBoundary(t)
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "false")
	var remoteCalls atomic.Int64
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer hub.Close()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	r := gin.New()
	SetPlatformRouter(r, a)
	for _, route := range []struct{ method, path string }{
		{"POST", "/chat/completions"}, {"POST", "/responses"}, {"POST", "/embeddings"},
		{"POST", "/audio/speech"}, {"POST", "/audio/transcriptions"},
		{"POST", "/images/generations"}, {"POST", "/images/edits"},
		{"POST", "/videos"}, {"POST", "/ocr/jobs"}, {"GET", "/calls/call"},
		{"GET", "/videos/call"}, {"GET", "/videos/call/result"}, {"POST", "/videos/call/cancel"},
		{"GET", "/ocr/jobs/call"}, {"GET", "/ocr/jobs/call/result"}, {"POST", "/ocr/jobs/call/cancel"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, "/platform/v1"+route.path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer uh_model_grant")
			req.Header.Set("X-UniHub-Task-ID", "task")
			req.Header.Set("Idempotency-Key", "call")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
			assert.Contains(t, w.Body.String(), "platform_billing_unavailable")
		})
	}
	assert.Zero(t, remoteCalls.Load(), "policy rejection must precede remote authorization")
	var calls int64
	require.NoError(t, db.Model(&platformbilling.Call{}).Count(&calls).Error)
	assert.Zero(t, calls, "disabled policy cannot create reservations")
}
