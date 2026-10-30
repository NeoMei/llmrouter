package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNativeRefreshAfterCommittedRotationSignsCurrentSecurityVersion(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, model.DB.AutoMigrate(&model.ProductAuthLifecycle{}, &model.UniHubIdentityMapping{}))
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": hash, "funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	bundle, err := CreateNativeLoginSessionAtAuthVersion(user.Id, 1, "password", "", "")
	require.NoError(t, err)
	var mutationErr error
	fired := false
	callback := "test:refresh_committed_before_security_change"
	require.NoError(t, model.DB.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(callback, func(tx *gorm.DB) {
		changes, ok := tx.Statement.Dest.(map[string]interface{})
		if !fired && tx.Statement.Table == "user_sessions" && ok && changes["refresh_hash"] != nil && tx.Error == nil && tx.RowsAffected == 1 {
			fired = true
			_, mutationErr = model.ChangePasswordForSession(&model.User{Id: user.Id, Password: "new-password"}, "old-password", bundle.Session.SID, 1, 1, model.NativeFundingLocalOnly)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })
	refreshed, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "", "")
	require.True(t, fired)
	require.NoError(t, mutationErr)
	require.NoError(t, err)
	require.NotEmpty(t, refreshed.RefreshToken)
	identity, err := ParseAccessToken(refreshed.AccessToken)
	require.NoError(t, err)
	assert.EqualValues(t, 2, identity.SessionVersion)
	assert.EqualValues(t, 2, identity.UserAuthVersion)
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err, "the returned access token must use the retained session's current security version")
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", bundle.Session.SID).Update("previous_valid_until", time.Now().Unix()-1).Error)
	continued, _, err := RefreshLoginSession(refreshed.RefreshToken, refreshed.Session.SID, "", "")
	require.NoError(t, err, "the cookie returned to the browser must remain usable after grace")
	assert.Equal(t, bundle.Session.SID, continued.Session.SID)
}
