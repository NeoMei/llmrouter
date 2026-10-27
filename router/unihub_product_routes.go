package router

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"net/http"
)

// These routes are product operations. Identity, funds, model credentials and
// deployment provisioning keep their separate native/UniHub authority boundaries.
func registerUniHubProductApplicationRoutes() {
	use := authz.Permission{Resource: "product", Action: "use"}
	catalogRead := authz.Permission{Resource: "catalog", Action: "read"}
	catalogWrite := authz.Permission{Resource: "catalog", Action: "write"}
	logRead := authz.Permission{Resource: "log", Action: "read"}
	routingRead := authz.Permission{Resource: "routing", Action: "read"}
	routingWrite := authz.Permission{Resource: "routing", Action: "write"}
	for _, path := range []string{"/api/models", "/api/capabilities", "/api/user/self/groups", "/api/user/models", "/api/data/self", "/api/data/flow/self", "/api/log/self", "/api/log/self/search", "/api/log/self/stat", "/api/mj/self", "/api/task/self", "/api/pricing", "/api/ratio_config", "/api/perf-metrics", "/api/perf-metrics/summary"} {
		middleware.RegisterUniHubManagementRoute(http.MethodGet, path, use)
	}
	for _, path := range []string{"/api/authz/catalog", "/api/group/", "/api/prefill_group/", "/api/models/", "/api/models/search", "/api/models/:id", "/api/vendors/", "/api/vendors/search", "/api/vendors/:id"} {
		middleware.RegisterUniHubManagementRoute(http.MethodGet, path, catalogRead, authz.ChannelRead)
	}
	for _, path := range []string{"/api/models/", "/api/vendors/", "/api/prefill_group/"} {
		middleware.RegisterUniHubManagementRoute(http.MethodPost, path, catalogWrite)
		middleware.RegisterUniHubManagementRoute(http.MethodPut, path, catalogWrite)
	}
	for _, path := range []string{"/api/models/:id", "/api/vendors/:id", "/api/prefill_group/:id"} {
		middleware.RegisterUniHubManagementRoute(http.MethodDelete, path, catalogWrite)
	}
	for _, path := range []string{"/api/log/", "/api/log/stat", "/api/log/search", "/api/log/channel_affinity_usage_cache", "/api/log/:request_id/route-decision", "/api/data/", "/api/data/users", "/api/data/flow", "/api/mj/", "/api/task/"} {
		middleware.RegisterUniHubManagementRoute(http.MethodGet, path, logRead)
	}
	for _, path := range []string{"/api/client_model_routes/", "/api/client_model_routes/options"} {
		middleware.RegisterUniHubManagementRoute(http.MethodGet, path, routingRead)
	}
	middleware.RegisterUniHubManagementRoute(http.MethodPost, "/api/client_model_routes/", routingWrite)
	middleware.RegisterUniHubManagementRoute(http.MethodPut, "/api/client_model_routes/", routingWrite)
	middleware.RegisterUniHubManagementRoute(http.MethodDelete, "/api/client_model_routes/:id", routingWrite)
	middleware.RegisterUniHubManagementRoute(http.MethodGet, "/api/option/", authz.Permission{Resource: "settings", Action: "read"})
	middleware.RegisterUniHubManagementRoute(http.MethodPut, "/api/option/", authz.Permission{Resource: "settings", Action: "write"})
}
