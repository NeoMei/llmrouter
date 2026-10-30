package middleware

import (
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"sync"
)

var productRoutes = struct {
	sync.RWMutex
	permissions map[string][]authz.Permission
}{permissions: map[string][]authz.Permission{}}

// RegisterUniHubManagementRoute is populated from the same router table that
// installs RequirePermission. Unknown routes and the secret-key route stay shut.
func RegisterUniHubManagementRoute(method, path string, p authz.Permission, alternatives ...authz.Permission) {
	productRoutes.Lock()
	defer productRoutes.Unlock()
	productRoutes.permissions[method+" "+path] = append([]authz.Permission{p}, alternatives...)
}
func uniHubManagementPermission(c *gin.Context) ([]authz.Permission, bool) {
	productRoutes.RLock()
	defer productRoutes.RUnlock()
	p, ok := productRoutes.permissions[c.Request.Method+" "+c.FullPath()]
	return p, ok
}
func loadUniHubAuthority(c *gin.Context, identity service.AuthIdentity) bool {
	if _, ok := authz.ProductPrincipal(c); ok {
		return true
	}
	p, _, e := service.ResolveUniHubSession(c.Request.Context(), identity)
	if e != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "UNIHUB_REAUTH_REQUIRED"})
		return false
	}
	authz.SetProductPrincipal(c, p)
	return true
}

// RoleFromAccess is only a compatibility projection for the product's original
// role guards. An explicit central role is authoritative; granular capabilities
// remain mandatory. A delegated permission never implies product root.
func RoleFromAccess(a userkit.Access) int {
	if a.ProductRole == 1 || a.ProductRole == 10 || a.ProductRole == 100 {
		return a.ProductRole
	}
	for _, p := range a.Permissions {
		if p != "product:use" {
			return common.RoleAdminUser
		}
	}
	return common.RoleCommonUser
}
func LoadRuntimeAuthority(c *gin.Context, localID int, authVersion int64) bool {
	if _, ok := authz.ProductPrincipal(c); ok {
		return true
	}
	if e := model.SyncProductAuthVersions(c.Request.Context(), localID); e != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "UNIHUB_LIFECYCLE_PENDING"})
		return false
	}
	v, e := platformaccount.IdentityForLocal(c.Request.Context(), strconv.Itoa(localID))
	if e != nil {
		// Runtime service errors do not establish that this user's session was
		// revoked. Deny the request while keeping recovery possible.
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "UNIHUB_AUTHORITY_UNAVAILABLE"})
		return false
	}
	if CentralRealmAuthenticated(c) && !v.Access.Active {
		if e := model.RecordCentralAuthDenial(c.Request.Context(), localID, v); e != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "UNIHUB_AUTHORITY_UNAVAILABLE"})
			return false
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_DISABLED"})
		return false
	}
	if authVersion > 0 && v.AuthVersion != authVersion {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_VERSION_REVOKED"})
		return false
	}
	if e := model.VerifyUniHubIdentityBinding(localID, v.Access.Issuer, v.Access.Subject); e != nil {
		c.AbortWithStatusJSON(401, gin.H{"success": false, "code": "UNIHUB_MAPPING_MISMATCH"})
		return false
	}
	p := userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(localID)}, Access: v.Access}
	if !p.HasPermission("product:use") {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "UNIHUB_PERMISSION_DENIED"})
		return false
	}
	authz.SetProductPrincipal(c, p)
	return true
}
