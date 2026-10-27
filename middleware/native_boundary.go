package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/gin-gonic/gin"
)

// NativePublicBoundary protects legacy identity and billing endpoints. Public
// callers receive a stable migration response; private management requests
// may retain compatibility access when explicitly configured.
func NativePublicBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		policy, err := unihubaccess.FromEnv()
		if err == nil && (!policy.PublicEnabled || policy.NativePublicEnabled || policy.IsPrivateManagementRequest(c.Request)) {
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
