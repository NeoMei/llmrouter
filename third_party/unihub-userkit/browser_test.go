package userkit

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserOIDCTransaction(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var server *httptest.Server
	var auth url.Values
	var mutate func(map[string]any)
	sign := func(c map[string]any) string {
		h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
		p, _ := json.Marshal(c)
		s := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
		d := sha256.Sum256([]byte(s))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
		return s + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/token":
			r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != auth.Get("code_challenge") {
				http.Error(w, "bad PKCE", 400)
				return
			}
			c := map[string]any{"iss": server.URL, "sub": "idp-sub", "aud": "product", "exp": time.Now().Add(time.Hour).Unix(), "nonce": auth.Get("nonce")}
			if mutate != nil {
				mutate(c)
			}
			id := sign(c)
			c["tokenType"] = "access-token"
			json.NewEncoder(w).Encode(map[string]any{"access_token": sign(c), "id_token": id, "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	b, err := NewBrowser(context.Background(), BrowserConfig{Issuer: server.URL, ClientID: "product", RedirectURI: "http://127.0.0.1:8080/callback", AccessTokenType: "access-token"})
	if err != nil {
		t.Fatal(err)
	}
	begin := func() string {
		u, e := b.Begin("browser-secret", "/channels?view=all")
		if e != nil {
			t.Fatal(e)
		}
		parsed, _ := url.Parse(u)
		auth = parsed.Query()
		if auth.Get("code_challenge_method") != "S256" || auth.Get("nonce") == "" {
			t.Fatal("missing PKCE/nonce")
		}
		return "http://127.0.0.1:8080/callback?code=valid&state=" + auth.Get("state")
	}
	callback := begin()
	if _, err = b.Complete(context.Background(), "wrong-browser", callback); err == nil {
		t.Fatal("wrong browser accepted")
	}
	if _, err = b.Complete(context.Background(), "browser-secret", callback); err != nil {
		t.Fatalf("wrong browser cancelled real browser: %v", err)
	}
	callback = begin()
	denied := "http://127.0.0.1:8080/callback?error=access_denied&state=" + auth.Get("state")
	if _, err = b.Complete(context.Background(), "browser-secret", denied); err == nil {
		t.Fatal("provider deny accepted")
	}
	if _, err = b.Complete(context.Background(), "browser-secret", callback); err == nil {
		t.Fatal("provider deny state replay accepted")
	}
	callback = begin()
	if _, err = b.Complete(context.Background(), "wrong-browser", callback); err == nil {
		t.Fatal("cross-browser callback accepted")
	}
	callback = begin()
	if _, err = b.Complete(context.Background(), "browser-secret", "http://127.0.0.1:8080/other?code=x&state="+auth.Get("state")); err == nil {
		t.Fatal("wrong callback accepted")
	}
	callback = begin()
	got, err := b.Complete(context.Background(), "browser-secret", callback)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "idp-sub" || got.ReturnTo != "/channels?view=all" || got.AccessToken == "" || got.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("invalid result: %#v", got)
	}
	if _, err = b.Complete(context.Background(), "browser-secret", callback); err == nil {
		t.Fatal("replay accepted")
	}
	for _, change := range []func(map[string]any){func(c map[string]any) { c["nonce"] = "wrong" }, func(c map[string]any) { c["aud"] = "wrong" }, func(c map[string]any) { c["iss"] = "https://wrong.example" }, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }} {
		mutate = change
		callback = begin()
		if _, err = b.Complete(context.Background(), "browser-secret", callback); err == nil {
			t.Fatal("invalid signed token accepted")
		}
	}
	for _, unsafe := range []string{"https://evil.example", "//evil.example", "/\\evil", "/%2f%2fevil", "/a\nLocation: x"} {
		if _, err = b.Begin("browser-secret", unsafe); err == nil {
			t.Fatalf("unsafe return accepted %q", unsafe)
		}
	}
	mutate = nil
	callback = begin()
	b.mu.Lock()
	tx := b.transactions[auth.Get("state")]
	tx.ExpiresAt = time.Now().Add(-time.Minute)
	b.transactions[auth.Get("state")] = tx
	b.mu.Unlock()
	if _, err = b.Complete(context.Background(), "browser-secret", callback); err == nil {
		t.Fatal("expired transaction accepted")
	}
}

func TestBrowserRejectsOversizedDiscovery(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks"})
		w.Write([]byte(strings.Repeat(" ", (1<<20)+1)))
	}))
	defer server.Close()
	if _, err := NewBrowser(context.Background(), BrowserConfig{Issuer: server.URL, ClientID: "product", RedirectURI: "http://127.0.0.1/callback"}); err == nil {
		t.Fatal("oversized discovery accepted")
	}
}
