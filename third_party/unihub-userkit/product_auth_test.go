package userkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductIdentityClientRejectsWrongBindingAndPreservesActorBoundary(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/internal/v1/product-runtime/identity/authenticate" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) || r.Header.Get("X-UniHub-Actor-Authorization") != "" {
				t.Error("incorrect delegated authentication boundary")
			}
			id := "17"
			if wrong {
				id = "another"
			}
			json.NewEncoder(w).Encode(ProductIdentity{Binding: RuntimeBinding{LocalUserID: id, ProductID: "llmrouter", InstanceID: "default", UserID: "central"}, Access: Access{Issuer: "https://hub.test", Subject: "central", User: User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default"}, ProductRole: 1, AuthVersion: 1})
		}))
		client, e := NewRuntimeClient(ServiceConfig{BaseURL: srv.URL, ProductID: "llmrouter", ServiceToken: strings.Repeat("s", 32)})
		if e != nil {
			t.Fatal(e)
		}
		_, e = client.AuthenticateProductIdentity(context.Background(), ProductAuthentication{LocalUserID: "17", AuthenticationID: "one-use-event", Method: "passkey", AuthVersion: 1})
		if wrong && e == nil {
			t.Fatal("cross-user identity accepted")
		}
		if !wrong && e != nil {
			t.Fatal(e)
		}
		srv.Close()
	}
}
