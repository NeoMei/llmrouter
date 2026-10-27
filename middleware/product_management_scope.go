package middleware

import (
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

// fallbackManagementPermission declares every original management subsystem.
// Existing exact route permissions win, particularly channel secret disclosure.
// A new subsystem needs an explicit declaration; numeric admin alone is never a grant.
func fallbackManagementPermission(c *gin.Context) ([]authz.Permission, bool) {
	path := c.FullPath()
	if path == "" {
		return nil, false
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions && c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPut && c.Request.Method != http.MethodPatch && c.Request.Method != http.MethodDelete {
		return nil, false
	}
	action := "write"
	if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
		action = "read"
	}
	if path == "/api/channel/:id/key" {
		return []authz.Permission{authz.ChannelSecretView}, true
	}
	if path == "/api/status/test" {
		return []authz.Permission{{Resource: "status", Action: "read"}}, true
	}
	groups := []struct{ prefix, resource string }{
		{"/api/subscription/admin", "subscription"}, {"/api/option", "settings"}, {"/api/custom-oauth-provider", "oauth_provider"},
		{"/api/performance", "performance"}, {"/api/ratio_sync", "ratio_sync"}, {"/api/redemption", "redemption"},
		{"/api/system-task", "system_task"}, {"/api/system-info", "system_info"}, {"/api/deployments", "deployment"},
		{"/api/native-billing", "billing"}, {"/api/gateway", "gateway"}, {"/api/user", "user"},
		{"/api/log", "log"}, {"/api/data", "log"}, {"/api/mj", "log"}, {"/api/task", "log"},
		{"/api/models", "catalog"}, {"/api/vendors", "catalog"}, {"/api/prefill_group", "catalog"}, {"/api/group", "catalog"},
		{"/api/client_model_routes", "routing"}, {"/api/authz", "catalog"},
	}
	for _, g := range groups {
		if path == g.prefix || strings.HasPrefix(path, g.prefix+"/") {
			return []authz.Permission{{Resource: g.resource, Action: action}}, true
		}
	}
	return nil, false
}
