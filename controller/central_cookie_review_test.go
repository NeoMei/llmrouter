package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLogoutClearsOnlySelectedRealmCookie(t *testing.T) {
	previousDB, previousRedis, previousSecret := model.DB, common.RedisEnabled, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CentralAuthState{}))
	model.DB, common.RedisEnabled, common.SessionSecret = db, false, "realm-cookie-test-secret"
	t.Cleanup(func() {
		model.DB, common.RedisEnabled, common.SessionSecret = previousDB, previousRedis, previousSecret
	})
	user := model.User{Username: "both-realms", AffCode: "both-realms", Status: common.UserStatusEnabled, AuthVersion: 1, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "bound"}).Error)
	native, err := service.CreateNativeLoginSessionAtAuthVersion(user.Id, 1, "password", "", "")
	require.NoError(t, err)
	central, err := service.CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
	c.Request.Header.Set("Authorization", "Bearer "+native.AccessToken)
	c.Request.Header.Set("X-Auth-Session", native.Session.SID)
	c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	c.Request.AddCookie(&http.Cookie{Name: service.CentralRefreshCookieName, Value: central.RefreshToken})
	AuthLogout(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	cookies := recorder.Result().Cookies()
	var tombstone *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == service.RefreshCookieName {
			require.Less(t, cookie.MaxAge, 0)
		}
		if cookie.Name == service.SelectionCookieName {
			tombstone = cookie
		}
	}
	require.NotNil(t, tombstone)
	reload := httptest.NewRecorder()
	reloadContext, _ := gin.CreateTestContext(reload)
	reloadContext.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/refresh", nil)
	reloadContext.Request.AddCookie(&http.Cookie{Name: service.CentralRefreshCookieName, Value: central.RefreshToken})
	reloadContext.Request.AddCookie(tombstone)
	RefreshAuth(reloadContext)
	require.Equal(t, http.StatusConflict, reload.Code)
	require.Contains(t, reload.Body.String(), "AUTH_SESSION_MISMATCH")
	explicit := httptest.NewRecorder()
	explicitContext, _ := gin.CreateTestContext(explicit)
	explicitContext.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/refresh", nil)
	explicitContext.Request.Header.Set("X-Auth-Session", central.Session.SID)
	explicitContext.Request.AddCookie(&http.Cookie{Name: service.CentralRefreshCookieName, Value: central.RefreshToken})
	explicitContext.Request.AddCookie(tombstone)
	RefreshAuth(explicitContext)
	require.NotEqual(t, http.StatusConflict, explicit.Code, "an explicit account choice must bypass the tombstone")
	centralRow, err := model.GetUserSessionBySID(central.Session.SID)
	require.NoError(t, err)
	require.Equal(t, model.UserSessionStatusActive, centralRow.Status)
}
