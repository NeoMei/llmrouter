package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// Product-owned operations must not borrow a shared native owner's resources.
// Profile PUT keeps only local preferences; identity changes stay with UniHub.
func productAccountBoundary(c *gin.Context, user *model.UserBase) bool {
	path := c.FullPath()
	productAccount := strings.HasPrefix(path, "/api/token/") || path == "/api/user/chat-credential" || path == "/api/user/setting" || path == "/api/user/self" && c.Request.Method == http.MethodPut
	if !productAccount {
		return true
	}
	var owner model.User
	if err := model.DB.WithContext(c.Request.Context()).Select("id", "funding_realm").First(&owner, user.Id).Error; err != nil {
		writeDashboardAuthError(c, err)
		return false
	}
	if owner.FundingRealm == model.NativeFundingLocalOnly {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "NATIVE_AUTHENTICATOR_REALM_REQUIRED"})
		return false
	}
	if path != "/api/user/self" {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 65537))
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var preferences map[string]any
	if err != nil || len(raw) > 65536 || common.Unmarshal(raw, &preferences) != nil || len(preferences) != 1 {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "CENTRAL_IDENTITY_MANAGED"})
		return false
	}
	for name, value := range preferences {
		if _, ok := value.(string); !ok || (name != "language" && name != "sidebar_modules") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "CENTRAL_IDENTITY_MANAGED"})
			return false
		}
	}
	return true
}
