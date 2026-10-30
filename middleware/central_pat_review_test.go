package middleware

import (
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCentralPATUsesOwnProvenanceAndLiveAuthority(t *testing.T) {
	oldDB, oldRedis := model.DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/central-pat.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.NativeLoginCredential{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = oldDB, oldRedis })
	u := model.User{Username: "central-pat", AffCode: "central-pat", Status: common.UserStatusEnabled, AuthVersion: 1, Role: common.RoleCommonUser, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, db.Create(&u).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://identity.test", User: platformaccount.User{ID: "subject"}}, u.Id)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.CentralAuthState{UserID: u.Id, AuthVersion: 7, Active: true, BindingID: "binding", Issuer: "https://identity.test", Subject: "subject", ProductID: "llmrouter", InstanceID: "default", AccessVersion: 1}).Error)
	require.NoError(t, db.Create(&model.UserSession{SID: "central-pat-session", UserID: u.Id, AuthRealm: model.CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", ExpiresAt: time.Now().Unix() + 600}).Error)
	pat := strings.Repeat("p", 30)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.SetCentralLoginCredentialTx(tx, u.Id, "central-pat-session", "pat", pat, func(tx *gorm.DB) error {
			return tx.Model(&model.User{}).Where("id = ?", u.Id).Update("access_token", pat).Error
		})
	}))
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", u.Id).Updates(map[string]any{"status": common.UserStatusDisabled, "auth_version": 2}).Error)
	active := true
	runtimeStatus := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if runtimeStatus != http.StatusOK {
			w.WriteHeader(runtimeStatus)
			return
		}
		access := userkit.Access{Issuer: "https://identity.test", Subject: "subject", User: userkit.User{ID: "subject"}, ProductID: "llmrouter", InstanceID: "default", Active: active, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(u.Id), UserID: "subject", ProductID: "llmrouter", InstanceID: "default"}, Access: access, ProductRole: 1, AuthVersion: 7}
		data, e := common.Marshal(identity)
		require.NoError(t, e)
		_, e = w.Write(data)
		require.NoError(t, e)
	}))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	r := gin.New()
	r.GET("/api/user/self", UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := func(value string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/user/self", nil)
		req.Header.Set("Authorization", "Bearer "+value)
		r.ServeHTTP(response, req)
		return response
	}
	require.Equal(t, http.StatusNoContent, request(pat).Code)
	newPAT := strings.Repeat("q", 30)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.SetCentralLoginCredentialTx(tx, u.Id, "central-pat-session", "pat", newPAT, func(tx *gorm.DB) error {
			return tx.Model(&model.User{}).Where("id = ?", u.Id).Update("access_token", newPAT).Error
		})
	}))
	require.NotEqual(t, http.StatusNoContent, request(pat).Code, "the replaced PAT must stop authenticating")
	require.Equal(t, http.StatusNoContent, request(newPAT).Code)
	// Exercise the same dashboard bearer-session path used by the self API.
	require.NoError(t, db.Create(&model.UserSession{SID: "central-browser-session", UserID: u.Id, AuthRealm: model.CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "central_credential", ExpiresAt: time.Now().Unix() + 600}).Error)
	digest := sha256.Sum256([]byte(newPAT))
	require.NoError(t, db.Create(&model.CentralSessionCredential{SID: "central-browser-session", UserID: u.Id, Kind: "pat", Digest: hex.EncodeToString(digest[:])}).Error)
	browserToken, _, err := service.IssueAccessToken(service.AuthIdentity{UserID: u.Id, SessionID: "central-browser-session", UserAuthVersion: 7, SessionVersion: 1})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, request(browserToken).Code)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		runtimeStatus = status
		for _, credential := range []string{newPAT, browserToken} {
			response := request(credential)
			require.Equal(t, http.StatusServiceUnavailable, response.Code, "runtime HTTP %d must fail closed without claiming user revocation", status)
			require.JSONEq(t, `{"success":false,"code":"UNIHUB_AUTHORITY_UNAVAILABLE"}`, response.Body.String())
		}
	}
	runtimeStatus = http.StatusOK
	require.Equal(t, http.StatusNoContent, request(newPAT).Code, "the same credential must work after runtime recovery")
	require.Equal(t, http.StatusNoContent, request(browserToken).Code, "the same session must work after runtime recovery")
	active = false
	require.NotEqual(t, http.StatusNoContent, request(newPAT).Code)
	require.NotEqual(t, http.StatusNoContent, request(strings.Repeat("x", 30)).Code)
}
