package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// ProductRelayEntry retains the deployment gate before parsing credentials.
// Runtime deployments still require the original authenticator and the
// credential/funding boundary below before any product handler executes.
func ProductRelayEntry() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := unihubaccess.FromEnv(); err == nil && platformaccount.RuntimeEnabled() {
			c.Next()
			return
		}
		NativePublicBoundary()(c)
	}
}

// ProductRelayBoundary runs after the route's original session/key authenticator.
// A verified product credential can use the original relay without exposing
// native identities, keys or local wallets on the public listener.
func ProductRelayBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, err := unihubaccess.FromEnv()
		if err != nil {
			NativePublicBoundary()(c)
			return
		}
		principal, product := authz.ProductPrincipal(c)
		if !product {
			NativePublicBoundary()(c)
			return
		}
		if !platformaccount.RuntimeEnabled() || !principal.HasPermission("product:use") || NativeRealmAuthenticated(c) || c.GetInt("id") <= 0 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "PRODUCT_RELAY_CREDENTIAL_REQUIRED"})
			return
		}
		var owner model.User
		if err := model.DB.WithContext(c.Request.Context()).Select("id", "funding_realm").First(&owner, c.GetInt("id")).Error; err != nil {
			writeDashboardAuthError(c, err)
			return
		}
		if owner.FundingRealm == model.NativeFundingLocalOnly {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "NATIVE_AUTHENTICATOR_REALM_REQUIRED"})
			return
		}
		state, err := model.ProductFundingState(c.Request.Context(), owner.Id)
		if err != nil || state != "central" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "PRODUCT_FUNDING_PENDING"})
			return
		}
		if _, browser := GetSessionAuthIdentity(c); browser && c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions {
			if len(c.Request.Header.Values("Origin")) != 1 || !isAllowedSessionOrigin(c.Request, c.GetHeader("Origin")) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "UNIHUB_ORIGIN_FORBIDDEN"})
				return
			}
		}
		c.Next()
	}
}

// ProductResourceBoundary is only for handlers that independently verify a
// resource-scoped capability. It never authenticates a model or dashboard API.
func ProductResourceBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		policy, err := unihubaccess.FromEnv()
		if err == nil && (policy.PlatformEnabled || platformaccount.RuntimeEnabled()) {
			c.Next()
			return
		}
		NativePublicBoundary()(c)
	}
}
