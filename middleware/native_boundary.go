package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/gin-gonic/gin"
)

// NativePublicBoundary protects legacy identity and billing endpoints. Explicit
// native compatibility can coexist with UniHub; downstream authentication still
// enforces the native account, API key, role and billing checks.
func NativePublicBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		policy, err := unihubaccess.FromEnv()
		if err == nil && (policy.NativePublicEnabled || policy.IsPrivateManagementRequest(c.Request)) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "NATIVE_ACCESS_DISABLED",
			"message": "native identity and billing are managed by UniHub",
		})
	}
}
