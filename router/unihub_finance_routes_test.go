package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRuntimeFinanceRoutesAreLimitedToDelegatedActions(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/v1/me", true}, {http.MethodGet, "/v1/accounts", true},
		{http.MethodGet, "/v1/products/llmrouter/model-consistency", true},
		{http.MethodGet, "/v1/accounts/acct-1/resources", true}, {http.MethodGet, "/v1/accounts/acct-1/calls/call:1", true},
		{http.MethodGet, "/v1/accounts/acct-1/recharges/order-1", true}, {http.MethodGet, "/v1/accounts/acct-1/recharges", false},
		{http.MethodPost, "/v1/accounts/acct-1/recharges", true}, {http.MethodPost, "/v1/accounts/acct-1/recharges/order-1/sandbox-settle", true},
		{http.MethodPost, "/v1/accounts/acct-1/api-keys", true}, {http.MethodDelete, "/v1/accounts/acct-1/api-keys/key-1", true},
		{http.MethodPut, "/v1/accounts/acct-1/usage-budget", true},
		{http.MethodPost, "/v1/accounts/acct-1/transfers", false}, {http.MethodPost, "/v1/accounts/acct-1/model-credentials", false},
		{http.MethodPatch, "/v1/accounts/acct-1/members/member-1", false}, {http.MethodGet, "/v1/accounts/acct-1/model-pool/calls", false},
		{http.MethodGet, "/v1/accounts/acct-1/../../me", false}, {http.MethodGet, "/v1/accounts/acct%2f1/wallets", false},
	} {
		assert.Equal(t, tc.allowed, runtimeFinanceRoute(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
}
