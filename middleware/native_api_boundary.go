package middleware

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// NativeAPIBoundary keeps native-only deployments private unless compatibility
// is explicitly enabled. UniHub sessions retain their product permission gates
// even when existing native accounts can use their own authenticated routes.
func NativeAPIBoundary() gin.HandlerFunc {
	native := NativePublicBoundary()
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/api/user/auth/session-options" && c.Request.Method == http.MethodGet && os.Getenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED") == "true" {
			c.Next()
			return
		}
		switch path {
		case "/api/status", "/api/uptime/status", "/api/notice", "/api/user-agreement", "/api/privacy-policy", "/api/about", "/api/home_page_content",
			"/api/stripe/webhook", "/api/creem/webhook", "/api/waffo/webhook", "/api/wechat-pay/notify", "/api/user/epay/notify", "/api/subscription/epay/notify", "/api/subscription/epay/return":
			c.Next()
			return
		}
		if c.FullPath() == "/api/waffo-pancake/webhook/:env" {
			c.Next()
			return
		}
		policy, err := unihubaccess.FromEnv()
		if err == nil && (policy.IsPrivateManagementRequest(c.Request) || policy.NativePublicEnabled) {
			c.Next()
			return
		}
		_, management := uniHubManagementPermission(c)
		if err == nil && (policy.PublicEnabled || platformaccount.RuntimeEnabled()) && (publicProjectionPath(c) || management) {
			if !hasUniHubBrowserSession(c) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "UNIHUB_SESSION_REQUIRED"})
				return
			}
			if management {
				// Catalog/header routes may have no UserAuth downstream. Resolve
				// current product authority before a registered handler can run.
				authHelper(c, common.RoleCommonUser)
				return
			}
			c.Next()
			return
		}
		native(c)
	}
}

func isUniHubBrowserLogin(method string) bool {
	return method == "unihub" || (platformaccount.RuntimeEnabled() && method == "product_broker")
}

func isUniHubBrowserSession(session *model.UserSession) bool {
	return session != nil && (isUniHubBrowserLogin(session.LoginMethod) || (platformaccount.RuntimeEnabled() && session.AuthRealm == model.CentralAuthRealm && session.LoginMethod == "central_credential"))
}

func publicProjectionPath(c *gin.Context) bool {
	if c.Request.Method == http.MethodGet {
		switch c.Request.URL.Path {
		case "/api/user/self", "/api/user/sessions":
			return true
		}
	}
	return (c.Request.Method == http.MethodPost && (c.Request.URL.Path == "/api/user/auth/refresh" || c.Request.URL.Path == "/api/user/auth/logout" || c.Request.URL.Path == "/api/user/sessions/revoke-others")) || (c.Request.Method == http.MethodDelete && c.FullPath() == "/api/user/sessions/:sid")
}

func hasUniHubBrowserSession(c *gin.Context) bool {
	// Refresh cookies only choose a session for this boundary check. The refresh
	// handler still verifies the secret and performs rotation/revocation normally.
	if strings.HasPrefix(c.Request.URL.Path, "/api/user/auth/") {
		_, raw, err := service.SelectRefreshCookie(c, strings.TrimSpace(c.GetHeader("X-Auth-Session")))
		if errors.Is(err, service.ErrLoginSessionMismatch) {
			// Let refresh/logout return the explicit conflict; the cookie
			// selector alone never grants authentication or revocation.
			return true
		}
		if err == nil {
			sid, ok := service.RefreshTokenSID(raw)
			if !ok {
				return false
			}
			session, err := model.GetUserSessionCached(sid)
			if err != nil || !isUniHubBrowserSession(session) {
				return false
			}
			projected, err := model.CheckUniHubProjection(session.UserID)
			if c.Request.URL.Path != "/api/user/auth/logout" && (err != nil || !projected) {
				return false
			}
			// A supplied access token must also belong to a UniHub browser session.
			if c.GetHeader("Authorization") == "" {
				return true
			}
		}
	}
	if len(c.Request.Header.Values("Authorization")) != 1 {
		return false
	}
	raw, ok := authorizationToken(c.GetHeader("Authorization"))
	if !ok {
		return false
	}
	identity, internal, err := service.ParseDashboardAccessToken(raw)
	if err != nil || !internal {
		return false
	}
	// Logout only revokes authority and must remain usable after a projection
	// is corrupted. The signed access token and recorded login method still
	// prove this was a UniHub session; no native login session is accepted.
	if c.Request.URL.Path == "/api/user/auth/logout" {
		session, err := model.GetUserSessionCached(identity.SessionID)
		return err == nil && session.UserID == identity.UserID && isUniHubBrowserSession(session)
	}
	session, user, err := service.ValidateLoginSession(identity)
	if err != nil || !isUniHubBrowserSession(session) || user.IdentitySource != model.UniHubIdentitySource {
		return false
	}
	projected, err := model.CheckUniHubProjection(user.Id)
	return err == nil && projected
}
