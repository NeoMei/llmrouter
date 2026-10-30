package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestCentralTelemetryExcludesSharedOwnerNativeHistory(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.ProductBrokerSessionProof{}, &model.ProductAuthLifecycle{}, &model.NativeLoginCredential{}, &model.Log{}, &model.QuotaData{}, &platformbilling.Call{}, &platformbilling.Attempt{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() { model.LOG_DB = oldLog })
	user := model.User{Username: "shared-private", AffCode: "shared-private", AuthVersion: 1, Status: 1, Role: 1, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "fixture", NativePassword: true}
	require.NoError(t, f.db.Create(&user).Error)
	var hub *httptest.Server
	admin := false
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/product-runtime/identity/browser/source/verify" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		access := userkit.Access{Issuer: hub.URL, Subject: "central-actor", User: userkit.User{ID: "central-actor"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		if admin {
			access.ProductRole = 10
			access.Permissions = append(access.Permissions, "log:read")
		}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "bound", LocalUserID: strconv.Itoa(user.Id), UserID: "central-actor", ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 7, ProductRole: 1}
		body, err := common.Marshal(identity)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(hub.Close)
	for key, value := range map[string]string{"UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_API_ORIGIN": hub.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": hub.URL, "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_PRODUCT_CLIENT_ID": "client", "UNIHUB_PRODUCT_REDIRECT_URI": "https://dashboard.test/api/unihub/callback", "NATIVE_PUBLIC_ACCESS": "true"} {
		t.Setenv(key, value)
	}
	_, err := model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "central-actor"}}, user.Id)
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, AccessVersion: 1, Active: true, BindingID: "bound", Issuer: hub.URL, Subject: "central-actor", ProductID: "llmrouter", InstanceID: "default"}).Error)
	central, err := service.CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	config := platformaccount.Config{PublicOrigin: hub.URL, ProductID: "llmrouter", InstanceID: "default", ClientID: "client"}
	prior, err := platformaccount.Current()
	require.NoError(t, err)
	platformaccount.Configure(config)
	t.Cleanup(func() { platformaccount.Configure(prior) })
	access := userkit.Access{Issuer: hub.URL, Subject: "central-actor", User: userkit.User{ID: "central-actor"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ExpiresAt: time.Now().Unix() + 600}
	require.NoError(t, model.BindBrokerSourceProof(central.Session.SID, user.Id, access, config, "ubsp_"+strings.Repeat("a", 64), access.ExpiresAt))
	native, err := service.CreateNativeLoginSessionAtAuthVersion(user.Id, 1, "password", "", "")
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, f.db.Create(&model.Log{UserId: user.Id, Username: user.Username, RequestId: "local-private-request", Type: model.LogTypeConsume, ModelName: "local-private-model", PromptTokens: 42, CreatedAt: now.Unix()}).Error)
	require.NoError(t, f.db.Create(&model.QuotaData{UserID: user.Id, Username: user.Username, ModelName: "local-private-model", CreatedAt: now.Unix(), Count: 3, TokenUsed: 42}).Error)
	require.NoError(t, f.db.Create(&platformbilling.Call{CallID: "central-own-call", ActorID: "central-actor", InstanceID: "default", FundingProductID: "llmrouter", Model: "central-own-model", Status: "SETTLED", TelemetryOutcome: "success", CreatedAt: now}).Error)
	query := fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d", now.Unix()-60, now.Unix()+60)
	for _, path := range []string{"/api/log/self", "/api/data/self", "/api/data/flow/self"} {
		response := f.request("GET", path+query, central.AccessToken, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"success":true`)
		require.Contains(t, response.Body.String(), "central-own-model", path)
		require.NotContains(t, response.Body.String(), "local-private-model", path)
		require.NotContains(t, response.Body.String(), "local-private-request", path)
	}
	admin = true
	for _, path := range []string{"/api/log/", "/api/data/", "/api/data/users", "/api/data/flow"} {
		response := f.request("GET", path+query, central.AccessToken, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"success":true`)
		require.NotContains(t, response.Body.String(), "local-private-model", path)
		require.NotContains(t, response.Body.String(), "local-private-request", path)
	}
	admin = false
	for _, path := range []string{"/api/log/self", "/api/data/self"} {
		response := f.request("GET", path+query, native.AccessToken, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "local-private-model", path)
		require.NotContains(t, response.Body.String(), "central-own-model", path)
	}
}
