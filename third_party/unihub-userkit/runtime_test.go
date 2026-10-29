package userkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeClientKeepsServiceAndActorAuthoritySeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/product-runtime/bindings" || r.Header.Get("Authorization") != "Bearer test-only-service-credential-32-bytes" || r.Header.Get("X-UniHub-Actor-Authorization") != "Bearer actor-proof" {
			t.Error("runtime request lost service or actor authority")
			w.WriteHeader(403)
			return
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil || body["local_user_id"] != "42" || body["binding_key"] != "bind-42" {
			t.Error("invalid binding body")
		}
		if _, ok := body["subject"]; ok {
			t.Error("caller supplied identity subject")
		}
		_ = json.NewEncoder(w).Encode(RuntimeBinding{ID: "binding-42", LocalUserID: "42", ProductID: "llmrouter", InstanceID: "default"})
	}))
	defer server.Close()
	c, e := NewRuntimeClient(ServiceConfig{BaseURL: server.URL, ProductID: "llmrouter", InstanceID: "default", ServiceToken: "test-only-service-credential-32-bytes"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Bind(context.Background(), "actor-proof", BindRequest{LocalUserID: "42", BindingKey: "bind-42"})
	if e != nil || b.ID != "binding-42" {
		t.Fatalf("binding: %+v %v", b, e)
	}
}

func TestRuntimeClientRejectsRedirectAndCrossScopeResponse(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/product-runtime/calls/cross" {
			_ = json.NewEncoder(w).Encode(RuntimeCall{ProductID: "other", InstanceID: "default"})
			return
		}
		http.Redirect(w, r, server.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c, e := NewRuntimeClient(ServiceConfig{BaseURL: server.URL, ProductID: "llmrouter", InstanceID: "default", ServiceToken: "test-only-service-credential-32-bytes"})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"cross", "redirect"} {
		if _, e := c.Call(context.Background(), id); e == nil {
			t.Fatalf("accepted unsafe response for %s", id)
		}
	}
}
