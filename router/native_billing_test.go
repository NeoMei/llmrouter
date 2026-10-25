package router

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeBillingRoutesRequireAdmin(t *testing.T) {
	f := newPoolHTTPFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.NativeBilling{}))
	registerNativeBillingRoutes(f.engine.Group("/api"))
	for _, tc := range []struct {
		user   string
		status int
	}{{"", http.StatusUnauthorized}, {"common", http.StatusForbidden}, {"root", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/api/native-billing/", nil)
		if tc.user != "" {
			req.Header.Set("Authorization", "Bearer "+f.tokens[tc.user])
		}
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, req)
		assert.Equal(t, tc.status, w.Code)
	}
}
