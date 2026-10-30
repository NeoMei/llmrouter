package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCentralRealmCannotAccessLegacyTwoFASlot(t *testing.T) {
	oldDB, oldLogDB, oldRedis, oldSecret := model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/central-twofa.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.NativeLoginCredential{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.Log{}))
	model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret = db, db, false, "twofa-central-review-secret"
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret = oldDB, oldLogDB, oldRedis, oldSecret
	})
	user := model.User{Username: "central-twofa", AffCode: "central-twofa", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 7}
	require.NoError(t, db.Create(&user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://identity.test", User: platformaccount.User{ID: "subject"}}, user.Id)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "binding", Issuer: "https://identity.test", Subject: "subject", ProductID: "llmrouter", InstanceID: "default", AccessVersion: 1}).Error)
	pat := strings.Repeat("p", 30)
	require.NoError(t, db.Create(&model.UserSession{SID: "source", UserID: user.Id, AuthRealm: model.CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusActive, RefreshHash: "source", LoginMethod: "product_broker", ExpiresAt: time.Now().Unix() + 600}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.SetCentralLoginCredentialTx(tx, user.Id, "source", "pat", pat, func(tx *gorm.DB) error {
			return tx.Model(&model.User{}).Where("id = ?", user.Id).Update("access_token", pat).Error
		})
	}))
	require.NoError(t, db.Create(&model.UserSession{SID: "central", UserID: user.Id, AuthRealm: model.CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusActive, RefreshHash: "central", LoginMethod: "central_credential", ExpiresAt: time.Now().Unix() + 600}).Error)
	digest := sha256.Sum256([]byte(pat))
	require.NoError(t, db.Create(&model.CentralSessionCredential{SID: "central", UserID: user.Id, Kind: "pat", Digest: hex.EncodeToString(digest[:])}).Error)
	require.NoError(t, db.Create(&model.UserSession{SID: "legacy", UserID: user.Id, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusActive, RefreshHash: "legacy", LoginMethod: "password", ExpiresAt: time.Now().Unix() + 600}).Error)
	require.NoError(t, db.Create(&model.TwoFA{UserId: user.Id, Secret: "legacy-pending"}).Error)
	hash, err := common.HashBackupCode("ABCD-1234")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.TwoFABackupCode{UserId: user.Id, CodeHash: hash}).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(user.Id), UserID: "subject", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://identity.test", Subject: "subject", User: userkit.User{ID: "subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}, ProductRole: 1, AuthVersion: 7}
		data, e := common.Marshal(identity)
		require.NoError(t, e)
		_, e = w.Write(data)
		require.NoError(t, e)
	}))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	r := gin.New()
	for _, route := range []struct {
		method, path string
		handler      gin.HandlerFunc
	}{
		{"GET", "/api/user/2fa/status", Get2FAStatus}, {"POST", "/api/user/2fa/setup", Setup2FA}, {"POST", "/api/user/2fa/enable", Enable2FA}, {"POST", "/api/user/2fa/disable", Disable2FA}, {"POST", "/api/user/2fa/backup_codes", RegenerateBackupCodes},
		{"GET", "/api/user/2fa/stats", Admin2FAStats}, {"DELETE", "/api/user/:id/2fa", AdminDisable2FA},
		{"POST", "/api/verify", UniversalVerify},
		{"GET", "/api/user/self", func(c *gin.Context) { c.Status(http.StatusNoContent) }}, {"GET", "/api/user/passkey", func(c *gin.Context) { c.Status(http.StatusNoContent) }},
	} {
		r.Handle(route.method, route.path, middleware.UserAuth(), route.handler)
	}
	central, _, err := service.IssueAccessToken(service.AuthIdentity{UserID: user.Id, SessionID: "central", UserAuthVersion: 7, SessionVersion: 1})
	require.NoError(t, err)
	legacy, _, err := service.IssueAccessToken(service.AuthIdentity{UserID: user.Id, SessionID: "legacy", UserAuthVersion: 7, SessionVersion: 1})
	require.NoError(t, err)
	request := func(method, path, credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{"code":"000000"}`))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, credential := range []string{central, pat} {
		for _, route := range []struct{ method, path string }{{"GET", "/api/user/2fa/status"}, {"POST", "/api/user/2fa/setup"}, {"POST", "/api/user/2fa/enable"}, {"POST", "/api/user/2fa/disable"}, {"POST", "/api/user/2fa/backup_codes"}, {"GET", "/api/user/2fa/stats"}, {"DELETE", "/api/user/1/2fa"}, {"POST", "/api/verify"}} {
			response := request(route.method, route.path, credential)
			require.Equal(t, http.StatusForbidden, response.Code, route.path+": "+response.Body.String())
		}
	}
	for _, path := range []string{"/api/user/self", "/api/user/passkey"} {
		require.Equal(t, http.StatusNoContent, request("GET", path, central).Code)
	}
	require.Equal(t, http.StatusNoContent, request("GET", "/api/user/self", pat).Code, "central PAT still authenticates against live authority")
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "false")
	response := request("GET", "/api/user/self", pat)
	require.NotEqual(t, http.StatusNoContent, response.Code, "disabling central realm must never downgrade a central PAT to legacy")
	response = request("POST", "/api/user/2fa/setup", pat)
	require.NotEqual(t, http.StatusOK, response.Code, "disabled central realm must not permit access to native 2FA")
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	var centralPATProof model.CentralLoginCredential
	require.NoError(t, db.Where("user_id = ? AND kind = ?", user.Id, "pat").First(&centralPATProof).Error)
	require.NoError(t, db.Delete(&centralPATProof).Error)
	require.Equal(t, http.StatusUnauthorized, request("GET", "/api/user/self", pat).Code, "missing central PAT proof must not become a legacy credential")
	require.Equal(t, http.StatusUnauthorized, request("POST", "/api/user/2fa/setup", pat).Code, "missing proof must not reach local 2FA")
	require.NoError(t, db.Create(&centralPATProof).Error)
	require.NoError(t, db.Create(&model.NativeLoginCredential{UserID: user.Id, Kind: "pat", Digest: hex.EncodeToString(digest[:])}).Error)
	require.Equal(t, http.StatusUnauthorized, request("GET", "/api/user/self", pat).Code, "conflicting native and central proofs must deny")
	require.NoError(t, db.Where("user_id = ? AND kind = ?", user.Id, "pat").Delete(&model.NativeLoginCredential{}).Error)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:central_proof_unavailable", func(tx *gorm.DB) {
		if tx.Statement.Table == "central_login_credentials" {
			tx.AddError(errors.New("proof store unavailable"))
		}
	}))
	response = request("GET", "/api/user/self", pat)
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "AUTH_STORE_UNAVAILABLE")
	require.NoError(t, db.Callback().Query().Remove("test:central_proof_unavailable"))
	require.Equal(t, http.StatusNoContent, request("GET", "/api/user/self", pat).Code)
	var factor model.TwoFA
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&factor).Error)
	require.Equal(t, "legacy-pending", factor.Secret)
	var codes []model.TwoFABackupCode
	require.NoError(t, db.Where("user_id = ?", user.Id).Find(&codes).Error)
	require.Len(t, codes, 1)
	require.Equal(t, hash, codes[0].CodeHash)
	require.Equal(t, http.StatusOK, request("GET", "/api/user/2fa/status", legacy).Code)
	response = request("POST", "/api/user/2fa/setup", legacy)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"success":true`)
}
