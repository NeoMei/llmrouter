package userkit

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProductFinanceTransportIsFiniteAndForwardsOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/v1/product-finance/accounts/acct-1/recharges" || r.URL.Query().Get("product_id") != "llmrouter" {
			t.Errorf("wrong target: %s", r.URL.String())
		}
		if r.Header.Values("Authorization")[0] != "Bearer service-token-long-enough-for-test" || r.Header.Values("X-UniHub-Actor-Authorization")[0] != "Bearer upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
			t.Error("credentials not bound to their headers")
		}
		if r.Header.Get("Idempotency-Key") != "recharge-1" {
			t.Error("idempotency key lost")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"re_1"}`))
	}))
	defer srv.Close()
	c, err := NewProductFinanceClient(ProductFinanceConfig{BaseURL: srv.URL, ProductID: "llmrouter", ServiceToken: "service-token-long-enough-for-test"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Do(context.Background(), "POST", "/accounts/acct-1/recharges", url.Values{"product_id": {"llmrouter"}}, "upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", []byte(`{"amount":"1000"}`), "recharge-1")
	if err != nil || got.StatusCode != 201 || string(got.Body) != `{"id":"re_1"}` || calls != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", got, err, calls)
	}
}

func TestProductFinanceTransportRejectsUnboundedInputsAndDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	attackerCalls := 0
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerCalls++
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-UniHub-Actor-Authorization") != "" {
			t.Error("finance credentials crossed an origin redirect")
		}
	}))
	defer attacker.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, attacker.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, err := NewProductFinanceClient(ProductFinanceConfig{BaseURL: srv.URL, ProductID: "llmrouter", ServiceToken: "service-token-long-enough-for-test"})
	if err != nil {
		t.Fatal(err)
	}
	actor := "upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		method, path, actor string
		query               url.Values
	}{
		{"GET", "https://attacker.test/accounts", actor, nil},
		{"GET", "/accounts/../admin", actor, nil},
		{"GET", "/accounts", "upb_wrong", nil},
		{"PATCH", "/accounts/acct/api-keys/key", actor, nil},
		{"GET", "/accounts", actor, url.Values{"admin": {"true"}}},
	} {
		if _, err = c.Do(context.Background(), tc.method, tc.path, tc.query, tc.actor, nil, ""); err == nil {
			t.Errorf("unsafe request accepted: %#v", tc)
		}
	}
	if _, err = c.Do(context.Background(), "GET", "/accounts", nil, actor, nil, ""); err == nil {
		t.Fatal("redirect should be rejected")
	}
	if calls != 1 || attackerCalls != 0 {
		t.Fatalf("redirect attempted: configured origin calls=%d other origin calls=%d", calls, attackerCalls)
	}
}

func TestProductFinanceTransportIgnoresGlobalDialAndTLSOverrides(t *testing.T) {
	attackerCalls := 0
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attackerCalls++ }))
	defer attacker.Close()
	legitimateCalls := 0
	legitimate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legitimateCalls++
		if r.Header.Get("Authorization") != "Bearer service-token-long-enough-for-test" {
			t.Error("service bearer missing at configured origin")
		}
	}))
	defer legitimate.Close()
	old := http.DefaultTransport
	http.DefaultTransport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(attacker.URL, "http://"))
	}}
	defer func() { http.DefaultTransport = old }()
	c, err := NewProductFinanceClient(ProductFinanceConfig{BaseURL: legitimate.URL, ProductID: "llmrouter", ServiceToken: "service-token-long-enough-for-test"})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := c.http.Transport.(*http.Transport)
	if !ok || transport.DialContext != nil || transport.DialTLSContext != nil || transport.TLSClientConfig != nil || transport.Proxy != nil || !transport.DisableKeepAlives {
		t.Fatal("finance client inherited a caller dialer, proxy, or TLS override")
	}
	actor := "upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err = c.Do(context.Background(), "GET", "/accounts", nil, actor, nil, ""); err != nil {
		t.Fatal(err)
	}
	if legitimateCalls != 1 || attackerCalls != 0 {
		t.Fatalf("configured origin calls=%d attacker calls=%d", legitimateCalls, attackerCalls)
	}
}

func TestProductFinanceTransportKeepsCertificateVerificationEnabled(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	c, err := NewProductFinanceClient(ProductFinanceConfig{BaseURL: server.URL, ProductID: "llmrouter", ServiceToken: "service-token-long-enough-for-test"})
	if err != nil {
		t.Fatal(err)
	}
	actor := "upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err = c.Do(context.Background(), "GET", "/accounts", nil, actor, nil, ""); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	if calls != 0 {
		t.Fatalf("credentialed request reached untrusted TLS peer %d times", calls)
	}
}
