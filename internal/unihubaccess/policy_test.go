package unihubaccess

import (
	"net/http/httptest"
	"testing"
)

func TestFromEnvPublicUniHubNativeDisabled(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_ORIGIN", "https://admin.example")
	t.Setenv("MANAGEMENT_TOKEN", "secret")
	p, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !p.PublicEnabled || !p.PlatformEnabled || p.NativePublicEnabled {
		t.Fatalf("unexpected policy: %+v", p)
	}
}

func TestFromEnvRequiresProductEnvironment(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected missing product environment error")
	}
}

func TestFromEnvRequiresPlatformWhenProductEnabled(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected missing platform environment error")
	}
}

func TestFromEnvRejectsInvalidBoolean(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "yes")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected strict boolean error")
	}
}

func TestPrivateManagementRequestRequiresTrustedOriginOrToken(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_ORIGIN", "https://admin.example")
	t.Setenv("MANAGEMENT_TOKEN", "secret")
	p, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "https://router.example/api/user/login", nil)
	req.Header.Set("Origin", "https://evil.example")
	if p.IsPrivateManagementRequest(req) {
		t.Fatal("untrusted origin accepted")
	}
	req.Header.Set("Authorization", "Bearer secret")
	if !p.IsPrivateManagementRequest(req) {
		t.Fatal("management token rejected")
	}

	// Origin access is restricted by the configured forwarded host when set.
	t.Setenv("NATIVE_ADMIN_PROXY", "router.internal")
	p, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Del("Authorization")
	req.Header.Set("Origin", "https://admin.example")
	if p.IsPrivateManagementRequest(req) {
		t.Fatal("origin accepted without trusted proxy")
	}
	req.Header.Set("X-Forwarded-Host", "router.internal")
	if !p.IsPrivateManagementRequest(req) {
		t.Fatal("trusted origin and proxy rejected")
	}
}
