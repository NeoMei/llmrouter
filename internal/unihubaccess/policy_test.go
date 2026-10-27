package unihubaccess

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPolicyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, product, platform, native, listener string
		valid                                     bool
	}{
		{"unihub", "true", "true", "false", "127.0.0.1:3001", true},
		{"standalone", "false", "false", "true", "", true},
		{"private", "true", "true", "true", "127.0.0.1:3001", true},
		{"missing product", "", "true", "false", "", false},
		{"missing platform", "true", "", "false", "", false},
		{"missing native", "false", "false", "", "", false},
		{"invalid boolean", "yes", "true", "false", "", false},
		{"wildcard listener", "true", "true", "false", "0.0.0.0:3001", false},
		{"hostname listener", "true", "true", "false", "localhost:3001", false},
		{"invalid port", "true", "true", "false", "127.0.0.1:0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UNIHUB_PRODUCT_ENABLED", tc.product)
			t.Setenv("UNIHUB_PLATFORM_ENABLED", tc.platform)
			t.Setenv("NATIVE_PUBLIC_ACCESS", tc.native)
			t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", tc.listener)
			p, err := FromEnv()
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.product == "true", p.PublicEnabled)
			assert.Equal(t, tc.platform == "true", p.PlatformEnabled)
			assert.Equal(t, tc.native == "true", p.NativePublicEnabled)
		})
	}
}

func TestPrivateManagementUsesTransportNotHeaders(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "127.0.0.1:3001")
	t.Setenv("NATIVE_ADMIN_ORIGIN", "https://admin.example")
	t.Setenv("NATIVE_ADMIN_PROXY", "admin.example")
	t.Setenv("MANAGEMENT_TOKEN", "secret")
	p, err := FromEnv()
	require.NoError(t, err)
	for _, auth := range [][]string{nil, {"Bearer secret"}, {"Bearer secret", "Bearer attacker"}, {"Bearer secret, Bearer attacker"}, {"Bearer"}} {
		r := httptest.NewRequest("POST", "http://router/api/user/login?native=true", nil)
		r.RemoteAddr = "203.0.113.20:12345"
		r.Header.Set("Origin", "https://admin.example")
		r.Header.Set("Referer", "https://admin.example/")
		r.Header.Set("X-Forwarded-Host", "admin.example")
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		r.Header["Authorization"] = auth
		assert.False(t, p.IsPrivateManagementRequest(r))
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3000}))
		assert.False(t, p.IsPrivateManagementRequest(r))
	}
	r := httptest.NewRequest("POST", "http://router/api/user/login", nil)
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
	assert.True(t, p.IsPrivateManagementRequest(r))
	assert.False(t, p.IsPrivateManagementRequest(nil))
}
