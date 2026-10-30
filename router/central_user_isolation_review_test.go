package router

import (
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
	"github.com/stretchr/testify/require"
)

func TestCentralUserPagesExcludeNativeOwnersAndTopups(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.ProductBrokerSessionProof{}, &model.ProductAuthLifecycle{}, &model.NativeLoginCredential{}, &model.TopUp{}))
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	users := []model.User{
		{Username: "central-list-admin", AffCode: "central-list-admin", Password: "private-hash-must-not-leak", Status: 1, Role: 100, AuthVersion: 1},
		{Username: "native-private-one", AffCode: "native-private-one", Status: 1, Role: 100, AuthVersion: 1, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "test", NativePassword: true, Quota: 98765},
		{Username: "visible-one", AffCode: "visible-one", Status: 1, Role: 1, AuthVersion: 1},
		{Username: "native-private-two", AffCode: "native-private-two", Status: 1, Role: 1, AuthVersion: 1, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "test", NativePassword: true, Quota: 45678},
		{Username: "visible-two", AffCode: "visible-two", Status: 1, Role: 1, AuthVersion: 1},
	}
	for i := range users {
		require.NoError(t, f.db.Create(&users[i]).Error)
	}
	var hub *httptest.Server
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/product-runtime/identity/browser/source/verify" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		access := userkit.Access{Issuer: hub.URL, Subject: "central-subject", User: userkit.User{ID: "central-subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ProductRole: 100, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use", "user:read", "billing:read"}}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "bound", LocalUserID: strconv.Itoa(users[0].Id), UserID: "central-subject", ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 1, ProductRole: 100}
		b, err := common.Marshal(identity)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(hub.Close)
	for key, value := range map[string]string{"UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_API_ORIGIN": hub.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": hub.URL, "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_PRODUCT_CLIENT_ID": "client", "UNIHUB_PRODUCT_REDIRECT_URI": "https://dashboard.test/api/unihub/callback", "NATIVE_PUBLIC_ACCESS": "true"} {
		t.Setenv(key, value)
	}
	_, err := model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "central-subject"}}, users[0].Id)
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: users[0].Id, AuthVersion: 1, AccessVersion: 1, Active: true, BindingID: "bound", Issuer: hub.URL, Subject: "central-subject", ProductID: "llmrouter", InstanceID: "default"}).Error)
	central, err := service.CreateVerifiedCentralLoginSession(users[0].Id, 1, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	config := platformaccount.Config{PublicOrigin: hub.URL, ProductID: "llmrouter", InstanceID: "default", ClientID: "client"}
	oldConfig, err := platformaccount.Current()
	require.NoError(t, err)
	platformaccount.Configure(config)
	t.Cleanup(func() { platformaccount.Configure(oldConfig) })
	access := userkit.Access{Issuer: hub.URL, Subject: "central-subject", User: userkit.User{ID: "central-subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ExpiresAt: time.Now().Unix() + 600}
	require.NoError(t, model.BindBrokerSourceProof(central.Session.SID, users[0].Id, access, config, "ubsp_"+strings.Repeat("a", 64), access.ExpiresAt))
	native, err := service.CreateNativeLoginSessionAtAuthVersion(users[1].Id, 1, "password", "", "")
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.TopUp{UserId: users[1].Id, TradeNo: "private-native-topup", Amount: 10, Money: 10, CreateTime: time.Now().Unix()}).Error)
	require.NoError(t, f.db.Create(&model.TopUp{UserId: users[0].Id, TradeNo: "central-shared-legacy-topup", Amount: 10, Money: 10, CreateTime: time.Now().Unix()}).Error)
	ownHistory := f.request(http.MethodGet, "/api/user/topup/self", central.AccessToken, nil)
	require.Equal(t, http.StatusForbidden, ownHistory.Code, ownHistory.Body.String())
	require.NotContains(t, ownHistory.Body.String(), "central-shared-legacy-topup")
	check := func(path string, expectedTotal int, expected []string, token string) {
		got := f.request(http.MethodGet, path, token, nil)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
		require.NotContains(t, got.Body.String(), "private-hash-must-not-leak")
		var body struct {
			Success bool `json:"success"`
			Data    struct {
				Total int `json:"total"`
				Items []struct {
					Username string `json:"username"`
				} `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(got.Body.Bytes(), &body))
		require.True(t, body.Success, got.Body.String())
		require.Equal(t, expectedTotal, body.Data.Total)
		names := make([]string, 0, len(body.Data.Items))
		for _, u := range body.Data.Items {
			names = append(names, u.Username)
		}
		require.Equal(t, expected, names)
	}
	// An existing, still-unenrolled native user can already have a verified
	// central identity mapping. Neither an empty realm nor mapping proves the
	// shared row's native metadata is safe to disclose.
	for _, path := range []string{
		"/api/user/?sort_by=id&sort_order=asc&p=1&page_size=2",
		"/api/user/?sort_by=id&sort_order=asc&p=2&page_size=2",
		"/api/user/search?keyword=visible&sort_by=id&sort_order=asc&p=1&page_size=1",
		"/api/user/search?keyword=native-private&sort_by=id&sort_order=asc",
	} {
		got := f.request(http.MethodGet, path, central.AccessToken, nil)
		require.Equal(t, http.StatusServiceUnavailable, got.Code, got.Body.String())
		require.Contains(t, got.Body.String(), "USER_REALM_UNAVAILABLE")
		for _, secret := range []string{"native-private", "visible-one", "private-hash-must-not-leak", "98765"} {
			require.NotContains(t, got.Body.String(), secret)
		}
	}
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "ambiguous-subject"}}, users[2].Id)
	require.NoError(t, err)
	mapped := f.request(http.MethodGet, "/api/user/search?keyword=visible-one", central.AccessToken, nil)
	require.Equal(t, http.StatusServiceUnavailable, mapped.Code, mapped.Body.String())
	require.Contains(t, mapped.Body.String(), "USER_REALM_UNAVAILABLE")
	// Changing the candidate to LOCAL_ONLY after initial authentication cannot
	// create a query-to-enrollment exposure while the operation is gated.
	require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", users[2].Id).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "test"}).Error)
	got := f.request(http.MethodGet, "/api/user/search?keyword=visible-one", central.AccessToken, nil)
	require.Equal(t, http.StatusServiceUnavailable, got.Code, got.Body.String())
	require.NotContains(t, got.Body.String(), "visible-one")
	topups := f.request(http.MethodGet, "/api/user/topup", central.AccessToken, nil)
	require.Equal(t, http.StatusForbidden, topups.Code, topups.Body.String())
	require.NotContains(t, topups.Body.String(), "private-native-topup")
	searchedTopups := f.request(http.MethodGet, "/api/user/topup?keyword=private", central.AccessToken, nil)
	require.Equal(t, http.StatusForbidden, searchedTopups.Code, searchedTopups.Body.String())
	require.NotContains(t, searchedTopups.Body.String(), "private-native-topup")
	// Native administrator's existing list and financial read remain available.
	check("/api/user/?sort_by=id&sort_order=asc&p=1&page_size=2", 5, []string{"central-list-admin", "native-private-one"}, native.AccessToken)
	check("/api/user/?sort_by=id&sort_order=asc&p=2&page_size=2", 5, []string{"visible-one", "native-private-two"}, native.AccessToken)
	check("/api/user/search?keyword=native-private&sort_by=id&sort_order=asc&p=1&page_size=1", 2, []string{"native-private-one"}, native.AccessToken)
	check("/api/user/search?keyword=native-private&sort_by=id&sort_order=asc&p=2&page_size=1", 2, []string{"native-private-two"}, native.AccessToken)
	localHistory := f.request(http.MethodGet, "/api/user/topup/self", native.AccessToken, nil)
	require.Equal(t, http.StatusOK, localHistory.Code, localHistory.Body.String())
	require.Contains(t, localHistory.Body.String(), "private-native-topup")
	require.NotContains(t, localHistory.Body.String(), "central-shared-legacy-topup")
	topups = f.request(http.MethodGet, "/api/user/topup", native.AccessToken, nil)
	require.Equal(t, http.StatusOK, topups.Code, topups.Body.String())
	require.Contains(t, topups.Body.String(), "private-native-topup")
}
