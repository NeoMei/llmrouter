package platformaccount

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeIdentityDoesNotFallBackAfterCentralDenial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", "")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	if !RuntimeEnabled() {
		t.Fatal("runtime not enabled")
	}
	if _, e := IdentityForLocal(context.Background(), "17"); e == nil {
		t.Fatal("central denial was ignored")
	}
	if _, e := AuthenticateLocal(context.Background(), "17", 1, "passkey"); e == nil {
		t.Fatal("central authentication denial was ignored")
	}
}

func TestRuntimeUsesPrivateAPIWhileKeepingPublicBrowserOrigin(t *testing.T) {
	public := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(public.Close)
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/internal/v1/product-runtime/identity/17", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"binding":{"local_user_id":"17","user_id":"canonical-user","personal_account_id":"payer","product_id":"llmrouter","instance_id":"default"},"access":{"issuer":"https://unihub.test","subject":"canonical-user","user":{"id":"canonical-user"},"product_id":"llmrouter","instance_id":"default","active":true,"permissions":["product:use"]},"auth_version":1,"product_role":1}`))
	}))
	t.Cleanup(private.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", public.URL)
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", private.URL)
	t.Setenv("UNIHUB_PRODUCT_PUBLIC_ORIGIN", public.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	t.Setenv("UNIHUB_PRODUCT_CLIENT_ID", "router")
	t.Setenv("UNIHUB_PRODUCT_REDIRECT_URI", "https://router.test/api/unihub/callback")
	identity, e := IdentityForLocal(context.Background(), "17")
	require.NoError(t, e, "public ingress blocks internal routes; the runtime must use its private API origin")
	assert.Equal(t, "canonical-user", identity.Binding.UserID)
	config, e := FromEnv()
	require.NoError(t, e)
	assert.Equal(t, public.URL, config.PublicOrigin)
	client, e := RuntimeClient()
	require.NoError(t, e)
	assert.Equal(t, private.URL, client.Scope().Origin)
}

func TestRuntimeRejectsInvalidPrivateOriginInsteadOfPublicFallback(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "https://unihub.test")
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", "http://remote-insecure.test")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	_, e := RuntimeClient()
	require.Error(t, e)
}
