package router

import (
	"net/http"
	"regexp"
)

// runtimeFinanceRoutes is the central actor-finance contract, not a generic
// product-runtime proxy. OIDC sessions retain the existing BFF route policy.
var runtimeFinanceRoutes = []struct {
	method string
	path   *regexp.Regexp
}{
	{http.MethodGet, regexp.MustCompile(`^/v1/(me|accounts|capabilities|products|catalog|billing-presets)$`)},
	{http.MethodGet, regexp.MustCompile(`^/v1/products/[A-Za-z0-9_-]+/(billing-profile|model-consistency)$`)},
	{http.MethodGet, regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(resources|wallets|subscriptions|orders|usage-budget|api-keys|calls|usage/summary|operations)$`)},
	{http.MethodGet, regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(calls|operations|recharges)/[A-Za-z0-9_.:-]+$`)},
	{http.MethodPost, regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(recharges|api-keys)$`)},
	{http.MethodPost, regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/recharges/[A-Za-z0-9_-]+/sandbox-settle$`)},
	{http.MethodDelete, regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/api-keys/[A-Za-z0-9_-]+$`)},
	{http.MethodPut, regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/usage-budget$`)},
}

func runtimeFinanceRoute(method, path string) bool {
	for _, route := range runtimeFinanceRoutes {
		if route.method == method && route.path.MatchString(path) {
			return true
		}
	}
	return false
}
