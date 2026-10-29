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

func TestProductBrowserGrantScopesAndSeparateApprovalActor(t *testing.T) {
	for _, bad := range []bool{false, true} {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
				t.Error("missing runtime service authorization")
			}
			switch r.URL.Path {
			case "/internal/v1/product-runtime/identity/browser/approve":
				if r.Header.Get("X-UniHub-Actor-Authorization") != "Bearer upa_actor" {
					t.Error("source actor not separate")
				}
				json.NewEncoder(w).Encode(ProductBrowserApprovalReceipt{FinishURL: srv.URL + "/auth/product/finish?handoff=opaque"})
			case "/internal/v1/product-runtime/identity/browser/exchange":
				if r.Header.Get("X-UniHub-Actor-Authorization") != "" {
					t.Error("target exchange accepted source actor")
				}
				product := "llmrouter"
				if bad {
					product = "another"
				}
				json.NewEncoder(w).Encode(ProductBrowserGrant{Access: Access{Issuer: "https://hub.test", Subject: "central", User: User{ID: "central"}, ProductID: product, InstanceID: "default", Permissions: []string{"product:use"}, Active: true, Version: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProofToken: "ubp_proof", ExpiresAt: time.Now().Add(time.Minute).Unix()})
			}
		}))
		c, e := NewRuntimeClient(ServiceConfig{BaseURL: srv.URL, ProductID: "llmrouter", ServiceToken: strings.Repeat("s", 32)})
		if e != nil {
			t.Fatal(e)
		}
		_, e = c.ApproveProductBrowserHandoff(context.Background(), "upa_actor", ProductBrowserApproval{ClientID: "router", HandoffID: "opaque", State: strings.Repeat("a", 43)})
		if e != nil {
			t.Fatal(e)
		}
		_, e = c.ExchangeProductBrowser(context.Background(), ProductBrowserExchange{ClientID: "router", Code: "code", Verifier: strings.Repeat("v", 43), Nonce: strings.Repeat("n", 43), RedirectURI: "https://router.test/api/unihub/callback"})
		if (e != nil) != bad {
			t.Fatal("wrong target scope accepted or correct rejected", e)
		}
		srv.Close()
	}
}

func TestPrivateRuntimeAcceptsOnlyConfiguredPublicBrowserFinish(t *testing.T) {
	for _, finishOrigin := range []string{"https://hub.test", "https://foreign.test"} {
		t.Run(finishOrigin, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/product-runtime/identity/browser/approve" {
					t.Errorf("unexpected private route %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(ProductBrowserApprovalReceipt{FinishURL: finishOrigin + "/auth/product/finish?handoff=opaque&state=state"})
			}))
			defer srv.Close()
			client, e := NewRuntimeClient(ServiceConfig{BaseURL: srv.URL, BrowserOrigin: "https://hub.test", ProductID: "llmrouter", ServiceToken: strings.Repeat("s", 32)})
			if e != nil {
				t.Fatal(e)
			}
			_, e = client.ApproveProductBrowserHandoff(context.Background(), "upa_source", ProductBrowserApproval{ClientID: "router", HandoffID: "opaque", State: strings.Repeat("a", 43)})
			if (e == nil) != (finishOrigin == "https://hub.test") {
				t.Fatalf("unexpected public finish result for %s: %v", finishOrigin, e)
			}
		})
	}
}
