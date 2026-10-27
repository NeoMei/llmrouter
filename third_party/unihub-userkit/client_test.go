package userkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientBindsProofAndBoundsResponses(t *testing.T) {
	for _, mode := range []string{"ok", "wrong-client", "wrong-product", "wrong-instance", "inactive", "expired", "wrong-issuer", "wrong-access-product", "wrong-access-instance", "bad-user", "error", "oversized", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing bearer")
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/other", 302)
					return
				}
				if mode == "error" {
					w.WriteHeader(503)
					return
				}
				if mode == "oversized" {
					w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
					return
				}
				if r.URL.Path == "/v1/capabilities" {
					client, product, instance := "notes-client", "notes", "default"
					if mode == "wrong-client" {
						client = "other"
					}
					if mode == "wrong-product" {
						product = "other"
					}
					if mode == "wrong-instance" {
						instance = "other"
					}
					json.NewEncoder(w).Encode(map[string]any{"kind": "product", "client_id": client, "product_id": product, "instance_id": instance, "scopes": []string{"access:read"}})
					return
				}
				a := validAccess()
				if mode == "expired" {
					a.ExpiresAt = time.Now().Add(-time.Minute).Unix()
				}
				if mode == "wrong-issuer" {
					a.Issuer = "https://foreign.test"
				}
				if mode == "wrong-access-product" {
					a.ProductID = "other"
				}
				if mode == "wrong-access-instance" {
					a.InstanceID = "other"
				}
				if mode == "inactive" {
					a.Active = false
				}
				if mode == "bad-user" {
					a.User.ID = "different"
				}
				json.NewEncoder(w).Encode(a)
			}))
			defer srv.Close()
			client, e := NewClient(Config{BaseURL: srv.URL, Issuer: "https://hub.test", ClientID: "notes-client", ProductID: "notes"})
			if e != nil {
				t.Fatal(e)
			}
			a, e := client.Resolve(context.Background(), "secret")
			if mode == "ok" {
				if e != nil || a.Subject != "central-1" || calls != 2 {
					t.Fatalf("%#v %v calls %d", a, e, calls)
				}
			} else if e == nil {
				t.Fatal("untrusted proof accepted")
			}
			if mode == "redirect" && calls != 1 {
				t.Fatal("redirect followed")
			}
		})
	}
}
