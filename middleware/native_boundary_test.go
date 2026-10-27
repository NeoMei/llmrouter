package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativePublicBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, product, platform, native string
		status                          int
	}{
		{"unihub disabled native", "true", "true", "false", 403},
		{"unihub coexistence", "true", "true", "true", 204},
		{"platform coexistence", "false", "true", "true", 204},
		{"missing product", "", "true", "false", 403},
		{"explicit standalone", "false", "false", "true", 204},
		{"standalone without opt in", "false", "false", "false", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UNIHUB_PRODUCT_ENABLED", tc.product)
			t.Setenv("UNIHUB_PLATFORM_ENABLED", tc.platform)
			t.Setenv("NATIVE_PUBLIC_ACCESS", tc.native)
			t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "127.0.0.1:3001")
			r := gin.New()
			r.POST("/login", NativePublicBoundary(), func(c *gin.Context) { c.Status(204) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/login", nil))
			require.Equal(t, tc.status, w.Code)
			if tc.status == 403 {
				assert.JSONEq(t, `{"success":false,"code":"NATIVE_ACCESS_DISABLED","message":"native identity and billing are managed by UniHub"}`, w.Body.String())
			}
		})
	}
}
