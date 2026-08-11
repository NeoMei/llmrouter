package middleware

import (
	"github.com/gin-gonic/gin"
)

// SecurityHeaders adds baseline response headers that reduce common attack
// surfaces without breaking the SPA frontend or API clients.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	}
}
