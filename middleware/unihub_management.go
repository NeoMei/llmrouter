package middleware

import (
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"net/http"
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
