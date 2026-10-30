package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

const nativeRealmContextKey = "authenticated_native_realm"

// NativeRealmAuthenticated is set only by authenticated persisted SID/key scope.
func NativeRealmAuthenticated(c *gin.Context) bool { return c.GetBool(nativeRealmContextKey) }
func authenticateNativeSession(c *gin.Context, identity service.AuthIdentity) (bool, error) {
	native, err := service.IsNativeLoginSession(c.Request.Context(), identity)
	if native && err == nil {
		c.Set(nativeRealmContextKey, true)
		common.SetContextKey(c, constant.ContextKeyNativeRealm, true)
	}
	return native, err
}

// Local authenticators belong to the native realm of an enrolled owner. Central
// browser authority must not mutate that owner's password/MFA/passkey recovery.
func nativeAuthenticatorBoundary(c *gin.Context, userID int) bool {
	path := c.FullPath()
	if CentralRealmAuthenticated(c) && (strings.HasPrefix(path, "/api/user/2fa/") || path == "/api/user/:id/2fa" || path == "/api/verify") {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "NATIVE_AUTHENTICATOR_REALM_REQUIRED"})
		return false
	}
	protected := path == "/api/user/self" && c.Request.Method != "GET" || path == "/api/verify" || strings.HasPrefix(path, "/api/user/2fa/") || strings.HasPrefix(path, "/api/user/passkey") || strings.HasPrefix(path, "/api/user/oauth/bindings") || strings.HasPrefix(path, "/api/oauth/") && strings.Contains(path, "/bind")
	// These self-service operations share the owner's original wallet,
	// subscription preference, login PAT and account settings.
	protected = protected || path == "/api/user/token" || path == "/api/user/setting" || path == "/api/user/aff_transfer" || path == "/api/user/checkin" && c.Request.Method != "GET"
	for _, prefix := range []string{"/api/user/topup", "/api/user/pay", "/api/user/wechat-pay/", "/api/user/stripe/", "/api/user/creem/", "/api/user/waffo/", "/api/user/waffo-pancake/"} {
		protected = protected || strings.HasPrefix(path, prefix)
	}
	protected = protected || strings.HasPrefix(path, "/api/subscription/") && !strings.HasPrefix(path, "/api/subscription/admin/") && path != "/api/subscription/plans"
	if path == "/api/user/token" && CentralRealmAuthenticated(c) {
		return true
	}
	if CentralRealmAuthenticated(c) && (path == "/api/oauth/state" || path == "/api/oauth/:provider") {
		return true
	}
	if CentralRealmAuthenticated(c) && (path == "/api/user/passkey/register/begin" || path == "/api/user/passkey/register/finish") {
		return true
	}
	if CentralRealmAuthenticated(c) && (path == "/api/user/passkey" || path == "/api/user/passkey/verify/begin" || path == "/api/user/passkey/verify/finish") {
		return true
	}
	if CentralRealmAuthenticated(c) && path == "/api/user/oauth/bindings/:provider_id" && c.Request.Method == http.MethodDelete {
		return true
	}
	if !protected || NativeRealmAuthenticated(c) {
		return true
	}
	return RequireNativeOwnerRealm(c, userID)
}

// RequireNativeOwnerRealm protects a local account operation after its owner is
// resolved from a trusted session or server-side auth flow.
func RequireNativeOwnerRealm(c *gin.Context, userID int) bool {
	if NativeRealmAuthenticated(c) {
		return true
	}
	var user model.User
	if err := model.DB.WithContext(c.Request.Context()).First(&user, userID).Error; err != nil {
		writeDashboardAuthError(c, err)
		return false
	}
	if user.FundingRealm == model.NativeFundingLocalOnly {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "NATIVE_AUTHENTICATOR_REALM_REQUIRED"})
		return false
	}
	return true
}
