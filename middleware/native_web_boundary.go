package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/internal/unihubaccess"

	"github.com/gin-gonic/gin"
)

// NativeWebBoundary runs before static assets and SPA fallbacks, including an
// externally hosted frontend. Unknown platform/API paths must never become UI.
func NativeWebBoundary() gin.HandlerFunc {
	native := NativePublicBoundary()
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(c.FullPath(), "/platform/v1/") || strings.HasPrefix(c.FullPath(), "/api/unihub/") {
			c.Next()
			return
		}
		if path == "/platform" || strings.HasPrefix(path, "/platform/") {
			c.AbortWithStatusJSON(404, gin.H{"error": gin.H{"code": "platform_route_not_found"}})
			return
		}
		if path == "/" || path == "/unihub" || path == "/about" || path == "/docs" || path == "/privacy-policy" || path == "/user-agreement" || path == "/install" || strings.HasPrefix(path, "/install/") || strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/brand/") || path == "/favicon.ico" || path == "/logo.png" || path == "/logo.svg" || path == "/manifest.json" || path == "/robots.txt" {
			c.Next()
			return
		}
		// These exact paths serve only the product SPA shell. Browser guards
		// hydrate live access; management and finance APIs keep their own gates.
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			policy, err := unihubaccess.FromEnv()
			if err == nil && policy.PublicEnabled {
				if path == "/sign-in" || IsProductApplicationPath(path) {
					c.Next()
					return
				}
			}
		}
		native(c)
	}
}

// IsProductApplicationPath is shared with OIDC return validation. It identifies
// product pages, never API namespaces or a prefix-wide static fallback.
func IsProductApplicationPath(path string) bool {
	switch path {
	case "/", "/unihub", "/dashboard", "/dashboard/", "/dashboard/overview", "/dashboard/models", "/dashboard/flow", "/dashboard/users",
		"/channels", "/channels/", "/wallet", "/profile", "/403", "/pricing",
		"/usage-logs", "/usage-logs/", "/usage-logs/common", "/usage-logs/drawing", "/usage-logs/task",
		"/models", "/models/", "/models/metadata", "/client-model-routes", "/client-model-routes/",
		"/system-settings", "/system-settings/models", "/system-settings/models/global", "/system-settings/models/routing-reliability":
		return true
	}
	return false
}
