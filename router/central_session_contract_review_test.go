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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCentralSessionResponseUsesCentralProfileForSharedOwner(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.ProductAuthLifecycle{}, &model.UniHubSessionBinding{}))
	user := model.User{Username: "private-native-name", DisplayName: "private-native-display", AffCode: "central-contract", Password: "unused", Role: 100, Status: 1, AuthVersion: 1, Quota: 1234, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, f.db.Create(&user).Error)
	var hub *httptest.Server
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access := userkit.Access{Issuer: hub.URL, Subject: "central-subject", User: userkit.User{ID: "central-subject", Name: "Central Profile"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		binding := userkit.RuntimeBinding{ID: "bound", LocalUserID: strconv.Itoa(user.Id), UserID: "central-subject", ProductID: "llmrouter", InstanceID: "default"}
		var value any
		switch r.URL.Path {
		case "/v1/capabilities":
			value = gin.H{"kind": "product", "product_id": "llmrouter", "client_id": "client", "instance_id": "default", "scopes": []string{"access:read"}}
		case "/v1/me/product-access":
			value = access
		case "/v1/me":
			value = access.User
		case "/internal/v1/product-runtime/bindings":
			value = binding
		case "/internal/v1/product-runtime/identity/" + strconv.Itoa(user.Id):
			value = userkit.ProductIdentity{Binding: binding, Access: access, AuthVersion: 7, ProductRole: 1}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, err := common.Marshal(value)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}))
	t.Cleanup(hub.Close)
	for key, value := range map[string]string{"UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_API_ORIGIN": hub.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": hub.URL, "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_PRODUCT_CLIENT_ID": "client", "UNIHUB_PRODUCT_REDIRECT_URI": "https://dashboard.test/api/unihub/callback"} {
		t.Setenv(key, value)
	}
	_, err := model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "central-subject"}}, user.Id)
	require.NoError(t, err)
	config, err := platformaccount.FromEnv()
	require.NoError(t, err)
	config.Enabled = true
	r := gin.New()
	SetUniHubRouter(r, config)
	request := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/unihub/session", nil)
	request.Header.Set("Authorization", "Bearer fixture")
	request.Header.Set("Origin", "https://dashboard.test")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			User struct {
				Username string `json:"username"`
				Role     int    `json:"role"`
				Quota    *int   `json:"quota"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	require.Equal(t, "Central Profile", result.Data.User.Username)
	require.Equal(t, 1, result.Data.User.Role)
	require.Nil(t, result.Data.User.Quota)
}
