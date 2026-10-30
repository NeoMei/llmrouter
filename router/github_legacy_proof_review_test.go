package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type githubLegacyProofProvider struct{ nativeRealmOAuthProvider }

func (*githubLegacyProofProvider) GetName() string { return "GitHub legacy fixture" }
func (*githubLegacyProofProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	return &oauth.OAuthUser{ProviderUserID: "numeric-github", Extra: map[string]interface{}{"legacy_id": "legacy-github"}}, nil
}
func (*githubLegacyProofProvider) IsUserIDTaken(id string) bool {
	var count int64
	model.DB.Model(&model.User{}).Where("github_id = ?", id).Count(&count)
	return count > 0
}
func (*githubLegacyProofProvider) FillUserByProviderID(user *model.User, id string) error {
	return model.DB.Where("github_id = ?", id).First(user).Error
}
func (*githubLegacyProofProvider) SetProviderUserID(user *model.User, id string) { user.GitHubId = id }
func (*githubLegacyProofProvider) ProviderUserIDColumn() string                  { return "github_id" }

func TestGitHubLegacyOAuthMigrationProtectsCentralCredential(t *testing.T) {
	for _, central := range []bool{false, true} {
		t.Run(map[bool]string{false: "pure-legacy", true: "central-proof"}[central], func(t *testing.T) {
			f := newUniHubRegressionFixture(t)
			t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
			require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.NativeLoginCredential{}, &model.ProductAuthLifecycle{}, &model.AuthFlow{}, &model.Log{}))
			oldLog := model.LOG_DB
			model.LOG_DB = f.db
			t.Cleanup(func() { model.LOG_DB = oldLog })
			user := model.User{Username: "github-legacy", AffCode: "github-legacy", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1}
			require.NoError(t, f.db.Create(&user).Error)
			var centralID service.AuthIdentity
			if central {
				require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "central"}).Error)
				now := time.Now().Unix()
				bootstrap := model.UserSession{SID: "bootstrap", UserID: user.Id, AuthRealm: model.CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", ExpiresAt: now + 600}
				require.NoError(t, f.db.Create(&bootstrap).Error)
				require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
					return model.SetCentralLoginCredentialTx(tx, user.Id, bootstrap.SID, "github_id", "legacy-github", func(tx *gorm.DB) error {
						return tx.Model(&model.User{}).Where("id = ?", user.Id).Update("github_id", "legacy-github").Error
					})
				}))
				var proof model.CentralLoginCredential
				require.NoError(t, f.db.First(&proof, "user_id = ? AND kind = ?", user.Id, "github_id").Error)
				session := model.UserSession{SID: "central-login", UserID: user.Id, AuthRealm: model.CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "central_credential", ExpiresAt: now + 600}
				require.NoError(t, f.db.Create(&session).Error)
				require.NoError(t, f.db.Create(&model.CentralSessionCredential{SID: session.SID, UserID: user.Id, Kind: "github_id", Digest: proof.Digest}).Error)
				centralID = service.AuthIdentity{UserID: user.Id, SessionID: session.SID, SessionVersion: 1, UserAuthVersion: 7}
				_, _, err := service.ValidateLoginSession(centralID)
				require.NoError(t, err)
			} else {
				require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", user.Id).Update("github_id", "legacy-github").Error)
			}
			oauth.Register("github-legacy-fixture", &githubLegacyProofProvider{})
			t.Cleanup(func() { oauth.Unregister("github-legacy-fixture") })
			req := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/oauth/state", strings.NewReader(`{"provider":"github-legacy-fixture","intent":"login"}`))
			req.Header.Set("Content-Type", "application/json")
			state := httptest.NewRecorder()
			f.router.ServeHTTP(state, req)
			var flow struct {
				Success bool `json:"success"`
				Data    struct {
					Token string `json:"flow_token"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(state.Body.Bytes(), &flow))
			require.True(t, flow.Success, state.Body.String())
			req = httptest.NewRequest(http.MethodGet, "https://dashboard.test/api/oauth/github-legacy-fixture?state="+flow.Data.Token+"&code=fixture", nil)
			out := httptest.NewRecorder()
			f.router.ServeHTTP(out, req)
			var response struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(out.Body.Bytes(), &response))
			var stored model.User
			require.NoError(t, f.db.First(&stored, user.Id).Error)
			if central {
				assert.False(t, response.Success, out.Body.String())
				assert.Equal(t, "legacy-github", stored.GitHubId)
				_, _, err := service.ValidateLoginSession(centralID)
				require.NoError(t, err)
			} else {
				assert.True(t, response.Success, out.Body.String())
				assert.Equal(t, "numeric-github", stored.GitHubId)
			}
		})
	}
}

func TestGitHubLegacyMigrationRejectsCentralHistoryWithoutCurrentProof(t *testing.T) {
	for _, evidence := range []string{"state", "session", "session-credential"} {
		t.Run(evidence, func(t *testing.T) {
			f := newUniHubRegressionFixture(t)
			t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
			require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.NativeLoginCredential{}, &model.ProductAuthLifecycle{}, &model.AuthFlow{}, &model.Log{}))
			oldLog := model.LOG_DB
			model.LOG_DB = f.db
			t.Cleanup(func() { model.LOG_DB = oldLog })
			user := model.User{Username: "github-history", AffCode: "github-history", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1, GitHubId: "legacy-github"}
			require.NoError(t, f.db.Create(&user).Error)
			switch evidence {
			case "state":
				require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: false, BindingID: "central"}).Error)
			case "session":
				require.NoError(t, f.db.Create(&model.UserSession{SID: "historical-central", UserID: user.Id, AuthRealm: model.CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: model.UserSessionStatusRevoked, RefreshHash: "fixture", LoginMethod: "product_broker", ExpiresAt: time.Now().Unix() + 600}).Error)
			case "session-credential":
				require.NoError(t, f.db.Create(&model.CentralSessionCredential{SID: "historic", UserID: user.Id, Kind: "github_id", Digest: "fixture"}).Error)
			}
			oauth.Register("github-history-fixture", &githubLegacyProofProvider{})
			t.Cleanup(func() { oauth.Unregister("github-history-fixture") })
			req := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/oauth/state", strings.NewReader(`{"provider":"github-history-fixture","intent":"login"}`))
			req.Header.Set("Content-Type", "application/json")
			state := httptest.NewRecorder()
			f.router.ServeHTTP(state, req)
			var flow struct {
				Success bool `json:"success"`
				Data    struct {
					Token string `json:"flow_token"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(state.Body.Bytes(), &flow))
			require.True(t, flow.Success, state.Body.String())
			req = httptest.NewRequest(http.MethodGet, "https://dashboard.test/api/oauth/github-history-fixture?state="+flow.Data.Token+"&code=fixture", nil)
			out := httptest.NewRecorder()
			f.router.ServeHTTP(out, req)
			assert.NotContains(t, out.Body.String(), `"success":true`)
			var stored model.User
			require.NoError(t, f.db.First(&stored, user.Id).Error)
			assert.Equal(t, "legacy-github", stored.GitHubId)
		})
	}
}
