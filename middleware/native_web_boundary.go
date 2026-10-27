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
				switch path {
				case "/sign-in", "/dashboard", "/channels", "/channels/", "/wallet", "/403":
					c.Next()
					return
				}
			}
		}
		native(c)
	}
}
