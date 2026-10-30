package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"net/http/httptest"
	"testing"
)

func TestCentralSharedOwnerDTOCannotExposeNativeAccountData(t *testing.T) {
	user := &model.User{Id: 42, Username: "shared", FundingRealm: model.NativeFundingLocalOnly, Quota: 123, UsedQuota: 17, RequestCount: 8, AffQuota: 55, Setting: `{"secret":"native-settings"}`, GitHubId: "native-github", StripeCustomer: "native-customer"}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/user/self", nil)
	for _, data := range []map[string]interface{}{productSelfData(c, user), BuildSelfUserData(user)} {
		for _, field := range []string{"quota", "used_quota", "request_count", "aff_code", "aff_count", "aff_quota", "aff_history_quota", "inviter_id", "setting", "sidebar_modules", "stripe_customer", "github_id", "discord_id", "oidc_id", "wechat_id", "telegram_id", "linux_do_id"} {
			assert.NotContains(t, data, field)
		}
	}
	c.Set("authenticated_native_realm", true)
	native := productSelfData(c, user)
	assert.Equal(t, 123, native["quota"])
	assert.Equal(t, user.Setting, native["setting"])
}

func TestRealmProjectionRequiresAuthenticatedSession(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/user/self", nil)
	user := &model.User{Id: 42, IdentitySource: "unihub"}
	assert.Equal(t, "UNKNOWN", productSelfData(c, user)["authentication_realm"])
	c.Set("id", 42)
	c.Set("session_id", "server-validated-sid")
	c.Set("auth_version", int64(1))
	c.Set("session_version", int64(1))
	assert.Equal(t, "LEGACY", productSelfData(c, user)["authentication_realm"])
}
