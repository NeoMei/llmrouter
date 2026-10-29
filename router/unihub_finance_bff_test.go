package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrokerFinanceBFFUsesFreshActorAndScopedRoutes(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductAuthLifecycle{}, &model.ProductFundingAccount{}, &model.Token{}, &model.Log{}))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_FINANCE_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	cfg, err := platformaccount.Current()
	require.NoError(t, err)
	user := model.User{Username: "broker-finance", AffCode: "broker-finance", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleRootUser}
	require.NoError(t, f.db.Create(&user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: "member"}}, user.Id)
	require.NoError(t, err)
	nativeKey := model.Token{UserId: user.Id, Key: "native-fixture-key", Name: "native-fixture", Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1}
	require.NoError(t, f.db.Create(&nativeKey).Error)
	history := model.Log{UserId: user.Id, RequestId: "original-history", Type: model.LogTypeSystem, CreatedAt: time.Now().Unix()}
	require.NoError(t, f.db.Create(&history).Error)
	bundle, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	actorCalls, financeCalls := 0, 0
	token := "upa_" + strings.Repeat("a", 64)
	revoked := false
	revokeDuringFinance := false
	var sentKey string
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/internal/v1/product-runtime/identity/"+strconv.Itoa(user.Id) || r.URL.Path == "/internal/v1/product-runtime/identity/authenticate" {
			if revoked {
				w.WriteHeader(403)
				return
			}
			if strings.HasSuffix(r.URL.Path, "authenticate") {
				actorCalls++
			}
			out := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(user.Id), UserID: "member", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: cfg.PublicOrigin, Subject: "member", User: userkit.User{ID: "member"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Permissions: []string{"product:use"}, ProductRole: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: 1, AuthVersion: 1, ActorToken: token, ActorExpiresAt: time.Now().Add(time.Minute).Unix()}
			raw, e := common.Marshal(out)
			if e != nil {
				w.WriteHeader(500)
				return
			}
			_, _ = w.Write(raw)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/internal/v1/product-finance/") || r.Header.Get("X-UniHub-Actor-Authorization") != "Bearer "+token || r.Header.Get("Origin") != "" {
			w.WriteHeader(403)
			return
		}
		financeCalls++
		sentKey = r.Header.Get("Idempotency-Key")
		if revokeDuringFinance {
			if _, revokeErr := model.RevokeUserSession(user.Id, bundle.Session.SID, "test"); revokeErr != nil {
				w.WriteHeader(500)
				return
			}
		}
		if r.URL.Path == "/internal/v1/product-finance/me" {
			_, _ = w.Write([]byte(`{"id":"member"}`))
			return
		}
		if r.URL.Path == "/internal/v1/product-finance/accounts" {
			_, _ = w.Write([]byte(`[{"id":"payer","role":"owner"}]`))
			return
		}
		if r.URL.Path == "/internal/v1/product-finance/accounts/payer/recharges" && r.Method == http.MethodPost {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"id":"order"}`))
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(center.Close)
	t.Setenv("UNIHUB_ORIGIN", center.URL)
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", center.URL)
	cfg.APIOrigin = center.URL
	r := gin.New()
	SetUniHubRouter(r, cfg)
	SetApiRouter(r)
	f.router = r
	request := func(method, path, body, csrf, origin string, extra http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if csrf != "" {
			req.Header.Set("X-UniHub-CSRF", csrf)
		}
		for k, v := range extra {
			for _, x := range v {
				req.Header.Add(k, x)
			}
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	access := request("GET", "/api/unihub/access", "", "", "", nil)
	require.Equal(t, 200, access.Code, access.Body.String())
	var data struct {
		Finance struct {
			Available bool   `json:"available"`
			CentralID string `json:"central_user_id"`
			Expires   int64  `json:"expires_at"`
		} `json:"finance_access"`
		CSRF string `json:"csrf_token"`
	}
	require.NoError(t, common.Unmarshal(access.Body.Bytes(), &data))
	require.True(t, data.Finance.Available)
	assert.Equal(t, "member", data.Finance.CentralID)
	assert.Greater(t, data.Finance.Expires, time.Now().Unix())
	assert.NotContains(t, access.Body.String(), token)
	get := request("GET", "/api/unihub/bff/v1/accounts", "", "", "", nil)
	require.Equal(t, 200, get.Code, get.Body.String())
	assert.Contains(t, get.Body.String(), "payer")
	nativeBundle := bundle
	brokerBundle, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	bundle = brokerBundle
	beforeBroker := financeCalls
	brokerAccess := request("GET", "/api/unihub/access", "", "", "", nil)
	require.Equal(t, 200, brokerAccess.Code)
	assert.Contains(t, brokerAccess.Body.String(), `"available":false`)
	assert.NotEqual(t, 200, request("GET", "/api/unihub/bff/v1/accounts", "", "", "", nil).Code)
	assert.Equal(t, beforeBroker, financeCalls, "unproven broker source cannot reach finance")
	bundle = nativeBundle
	before := financeCalls
	assert.Equal(t, 403, request("POST", "/api/unihub/bff/v1/accounts/payer/recharges", `{}`, "wrong", "https://dashboard.test", nil).Code)
	assert.Equal(t, 403, request("POST", "/api/unihub/bff/v1/accounts/payer/recharges", `{}`, data.CSRF, "https://evil.test", nil).Code)
	assert.Equal(t, 403, request("GET", "/api/unihub/bff/v1/accounts/payer/recharges", "", "", "", nil).Code)
	assert.Equal(t, 403, request("GET", "/api/unihub/bff/v1/accounts", "", "", "", http.Header{"X-Unihub-Actor-Authorization": []string{"Bearer forged"}}).Code)
	assert.Equal(t, before, financeCalls)
	post := request("POST", "/api/unihub/bff/v1/accounts/payer/recharges", `{"amount":"100"}`, data.CSRF, "https://dashboard.test", http.Header{"Idempotency-Key": []string{"same-key"}})
	require.Equal(t, 200, post.Code, fmt.Sprintf("%s", post.Body.String()))
	assert.Equal(t, "same-key", sentKey)
	assert.GreaterOrEqual(t, actorCalls, 3)
	var unchanged model.User
	require.NoError(t, f.db.First(&unchanged, user.Id).Error)
	assert.Equal(t, common.RoleRootUser, unchanged.Role)
	assert.Equal(t, user.Quota, unchanged.Quota)
	var sameKey model.Token
	require.NoError(t, f.db.First(&sameKey, nativeKey.Id).Error)
	assert.Equal(t, nativeKey.Key, sameKey.Key)
	var sameLog model.Log
	require.NoError(t, f.db.First(&sameLog, history.Id).Error)
	assert.Equal(t, "original-history", sameLog.RequestId)
	revokeDuringFinance = true
	before = financeCalls
	uncertain := request("POST", "/api/unihub/bff/v1/accounts/payer/recharges", `{"amount":"100"}`, data.CSRF, "https://dashboard.test", http.Header{"Idempotency-Key": []string{"uncertain-key"}})
	assert.Equal(t, http.StatusServiceUnavailable, uncertain.Code)
	assert.Contains(t, uncertain.Body.String(), "finance_result_unavailable")
	assert.NotContains(t, uncertain.Body.String(), "order")
	assert.Equal(t, before+1, financeCalls)
	revokeDuringFinance = false
	assert.NotEqual(t, 200, request("GET", "/api/unihub/bff/v1/accounts", "", "", "", nil).Code)
	assert.Equal(t, before+1, financeCalls, "a revoked session must not dispatch another financial request")
	revoked = true
	assert.NotEqual(t, 200, request("GET", "/api/unihub/bff/v1/accounts", "", "", "", nil).Code)
	t.Setenv("UNIHUB_PRODUCT_FINANCE_ENABLED", "false")
	assert.NotEqual(t, 200, request("GET", "/api/unihub/bff/v1/accounts", "", "", "", nil).Code)
}
