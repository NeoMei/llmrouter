package platformaccount

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeIdentityDoesNotFallBackAfterCentralDenial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
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
