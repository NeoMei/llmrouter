package authz

import (
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/gin-gonic/gin"
)

const productPrincipalKey = "unihub_current_principal"

func SetProductPrincipal(c *gin.Context, p userkit.Principal) { c.Set(productPrincipalKey, p) }
func ProductPrincipal(c *gin.Context) (userkit.Principal, bool) {
	v, ok := c.Get(productPrincipalKey)
	p, valid := v.(userkit.Principal)
	return p, ok && valid
}

// CanRequest is the shared middleware/controller gate. A central snapshot can
// only grant its own permissions; native baselines and overrides never union in.
func CanRequest(c *gin.Context, permission Permission) bool {
	if p, ok := ProductPrincipal(c); ok {
		return p.RequirePermission(c.Request.Context(), permission.Resource+":"+permission.Action, c.FullPath()) == nil
	}
	return Can(c.GetInt("id"), c.GetInt("role"), permission)
}
func RequestCapabilities(c *gin.Context) PermissionsMap {
	if _, ok := ProductPrincipal(c); !ok {
		return Capabilities(c.GetInt("id"), c.GetInt("role"))
	}
	out := PermissionsMap{}
	for _, r := range registry {
		out[r.Resource] = map[string]bool{}
		for _, a := range r.Actions {
			out[r.Resource][a.Action] = CanRequest(c, Permission{Resource: r.Resource, Action: a.Action})
		}
	}
	return out
}
