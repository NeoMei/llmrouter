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
	if !p.PublicEnabled || p.NativePublicEnabled {
		t.Fatalf("unexpected policy: %+v", p)
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
}
