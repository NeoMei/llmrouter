package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyPasswordChangeReturnsUsableRotatedSession(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}))
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	user := model.User{Username: "legacy-password", AffCode: "legacy-password", Password: hash, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1}
	require.NoError(t, f.db.Create(&user).Error)
	current, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	other, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "https://dashboard.test/api/user/self", strings.NewReader(`{"username":"legacy-password","password":"new-password","original_password":"old-password"}`))
	req.Header.Set("Authorization", "Bearer "+current.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://dashboard.test")
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(out.Body.Bytes(), &body))
	require.True(t, body.Success, out.Body.String())
	require.NotEmpty(t, body.Data.AccessToken)
	newIdentity, err := service.ParseAccessToken(body.Data.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(newIdentity)
	require.NoError(t, err)
	assert.Equal(t, current.Session.SID, newIdentity.SessionID)
	oldIdentity, err := service.ParseAccessToken(other.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(oldIdentity)
	require.Error(t, err)
	var stored model.User
	require.NoError(t, f.db.First(&stored, user.Id).Error)
	assert.True(t, common.ValidatePasswordAndHash("new-password", stored.Password))
}
