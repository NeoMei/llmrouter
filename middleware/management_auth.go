package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

func ManagementAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := strings.TrimSpace(os.Getenv("MANAGEMENT_TOKEN"))
		production := strings.EqualFold(strings.TrimSpace(os.Getenv("RUN_MODE")), "production")
		if expected == "" {
			if production {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "management endpoint is not configured"})
				return
			}
			c.Next()
			return
		}

		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") ||
			subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "management authorization required"})
			return
		}
		c.Next()
	}
}
