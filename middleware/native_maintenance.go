package middleware

import (
	"os"

	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// NativePrivateMaintenanceIdentity permits only an explicitly enabled private
// listener to recover an existing unbound, local-funded identity. Its caller
// must still authenticate the user and enforce native role permissions.
func NativePrivateMaintenanceIdentity(c *gin.Context, id int) bool {
	if !platformaccount.RuntimeEnabled() || os.Getenv("NATIVE_PRIVATE_MAINTENANCE_ENABLED") != "true" || id <= 0 || model.DB == nil {
		return false
	}
	policy, err := unihubaccess.FromEnv()
	if err != nil || !policy.IsPrivateManagementRequest(c.Request) {
		return false
	}
	return nativeUnboundLocalIdentity(c, id)
}

func nativeUnboundLocalIdentity(c *gin.Context, id int) bool {
	if id <= 0 || model.DB == nil {
		return false
	}
	var user model.User
	if err := model.DB.WithContext(c.Request.Context()).First(&user, id).Error; err != nil {
		return false
	}
	if user.IdentitySource != "" || user.UniHubUserID != "" || user.ProductRegistrationPending || user.ProductJITPending {
		return false
	}
	projected, err := model.CheckUniHubProjection(id)
	if err != nil || projected {
		return false
	}
	state, err := model.ProductFundingState(c.Request.Context(), id)
	if err != nil || state != "local" {
		return false
	}
	var pending int64
	if err := model.DB.WithContext(c.Request.Context()).Model(&model.ProductAuthLifecycle{}).Where("user_id = ? AND delivered = ?", id, false).Count(&pending).Error; err != nil || pending != 0 {
		return false
	}
	return true
}

// Permit only legacy chat under the already configured native network boundary.
// Billing independently reacquires identity and funding under the migration lock.
func nativeLocalLegacyToken(c *gin.Context, id int) bool {
	if !platformaccount.RuntimeEnabled() || c.Request.Method != "POST" || c.FullPath() != "/v1/chat/completions" {
		return false
	}
	policy, err := unihubaccess.FromEnv()
	return err == nil && (policy.NativePublicEnabled || policy.IsPrivateManagementRequest(c.Request)) && nativeUnboundLocalIdentity(c, id)
}

// The maintenance exception never permits token secrets, mutations or model
// calls. Exact routes prevent a private-session credential from gaining a new
// public or administrative capability merely through this opt-in.
func nativePrivateMaintenanceRead(c *gin.Context, id int) bool {
	if c.Request.Method != "GET" {
		return false
	}
	switch c.FullPath() {
	case "/api/user/self", "/api/user/sessions", "/api/token/", "/api/token/:id", "/api/log/self", "/api/log/self/stat", "/api/data/self", "/api/data/flow/self":
		return NativePrivateMaintenanceIdentity(c, id)
	default:
		return false
	}
}
