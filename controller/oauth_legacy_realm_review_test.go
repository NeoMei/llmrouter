package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type legacyGitHubProvider struct{ authFlowTestOAuthProvider }

func (*legacyGitHubProvider) IsUserIDTaken(id string) bool { return id == "old-login" }
func (*legacyGitHubProvider) FillUserByProviderID(user *model.User, id string) error {
	return model.DB.Where("github_id = ?", id).First(user).Error
}
func (*legacyGitHubProvider) ProviderUserIDColumn() string { return "github_id" }

func TestUnknownLegacyOAuthCannotReplaceNativeBinding(t *testing.T) {
	setupAuthFlowControllerTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 42).Updates(map[string]any{
		"github_id": "old-login", "funding_realm": model.NativeFundingLocalOnly,
		"funding_realm_evidence": "reviewed", "native_password": true,
	}).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/oauth/github", nil)
	_, err := findOrCreateOAuthUser(c, &legacyGitHubProvider{}, &oauth.OAuthUser{
		ProviderUserID: "new-numeric-id", Extra: map[string]any{"legacy_id": "old-login"},
	}, "")
	require.ErrorIs(t, err, model.ErrNativeCredentialUnverified)
	var user model.User
	require.NoError(t, model.DB.First(&user, 42).Error)
	require.Equal(t, "old-login", user.GitHubId)
}

func TestLegacyOAuthMigrationPreservesOrdinaryAccount(t *testing.T) {
	setupAuthFlowControllerTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.UserSession{}, &model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 42).Update("github_id", "old-login").Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/oauth/github", nil)
	user, err := findOrCreateOAuthUser(c, &legacyGitHubProvider{}, &oauth.OAuthUser{
		ProviderUserID: "new-numeric-id", Extra: map[string]any{"legacy_id": "old-login"},
	}, "")
	require.NoError(t, err)
	require.Equal(t, 42, user.Id)
	var current model.User
	require.NoError(t, model.DB.First(&current, 42).Error)
	require.Equal(t, "new-numeric-id", current.GitHubId)
}
