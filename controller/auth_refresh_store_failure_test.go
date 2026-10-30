package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRefreshStoreFailureKeepsCookieAndRecovers(t *testing.T) {
	oldDB, oldRedis, oldSecret := model.DB, common.RedisEnabled, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UniHubIdentityMapping{}))
	model.DB, common.RedisEnabled, common.SessionSecret = db, false, "test-refresh-store-secret-32-bytes"
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	t.Cleanup(func() {
		model.DB, common.RedisEnabled, common.SessionSecret = oldDB, oldRedis, oldSecret
		conn, e := db.DB()
		if e == nil {
			_ = conn.Close()
		}
	})
	user := model.User{Username: "store-recovery", Password: "unused", Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	bundle, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	call := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/refresh", nil)
		c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: bundle.RefreshToken})
		RefreshAuth(c)
		return w
	}
	callback := "test:refresh_store_unavailable"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_sessions" {
			tx.AddError(errors.New("database unavailable"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	failure := call()
	require.Equal(t, http.StatusServiceUnavailable, failure.Code, failure.Body.String())
	for _, cookie := range failure.Result().Cookies() {
		require.NotEqual(t, service.RefreshCookieName, cookie.Name, "must not clear refresh cookie")
	}
	require.NoError(t, db.Callback().Query().Remove(callback))
	success := call()
	require.Equal(t, http.StatusOK, success.Code, success.Body.String())
}
