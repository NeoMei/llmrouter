package userkit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// BrowserConfig is backend-only. RedirectURI must be the exact registered callback.
// AccessTokenType optionally requires a signed tokenType claim (Casdoor:
// access-token). Both ID and access tokens must be signed by the discovered issuer
// for this one client. Opaque access tokens require a different verified provider.
type BrowserConfig struct {
	Issuer, ClientID, ClientSecret, RedirectURI, AccessTokenType string
	Scopes                                                       []string
	HTTPClient                                                   *http.Client
}
type browserTransaction struct {
	Binding                   [32]byte
	Nonce, Verifier, ReturnTo string
	ExpiresAt                 time.Time
}

// Browser holds bounded, one-use login transactions. Keep one instance per
// process; use sticky routing for login/callback across replicas. Restarting
// safely invalidates outstanding logins. Tokens are returned only to server code.
type Browser struct {
	config       BrowserConfig
	oauth        oauth2.Config
	verifier     *oidc.IDTokenVerifier
	http         *http.Client
	mu           sync.Mutex
	transactions map[string]browserTransaction
}
type BrowserIdentity struct {
	Subject, AccessToken, ReturnTo string
	ExpiresAt                      int64
}

func browserEndpoint(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.User != nil {
		return false
	}
	return validOrigin(u.Scheme + "://" + u.Host)
}
func NewBrowser(ctx context.Context, c BrowserConfig) (*Browser, error) {
	if !browserEndpoint(c.Issuer) || !browserEndpoint(c.RedirectURI) || strings.TrimSpace(c.ClientID) == "" {
		return nil, ErrInvalid
	}
	h := http.Client{Timeout: 10 * time.Second}
	if c.HTTPClient != nil {
		h = *c.HTTPClient
	}
	if h.Timeout <= 0 || h.Timeout > 30*time.Second {
		h.Timeout = 10 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("OIDC redirect rejected") }
	transport := h.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	h.Transport = browserBoundedTransport{transport}
	clientCtx := oidc.ClientContext(ctx, &h)
	p, e := oidc.NewProvider(clientCtx, c.Issuer)
	if e != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	var metadata struct {
		JWKS string `json:"jwks_uri"`
	}
	if e = p.Claims(&metadata); e != nil {
		return nil, ErrInvalid
	}
	for _, endpoint := range []string{p.Endpoint().AuthURL, p.Endpoint().TokenURL, metadata.JWKS} {
		if !browserEndpoint(endpoint) || (strings.HasPrefix(c.Issuer, "https:") && !strings.HasPrefix(endpoint, "https:")) {
			return nil, ErrInvalid
		}
	}
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	if len(c.Scopes) > 0 {
		scopes = c.Scopes
	}
	hasOpenID := false
	for _, s := range scopes {
		if s == oidc.ScopeOpenID {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		return nil, ErrInvalid
	}
	endpoint := p.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams // no automatic retry of code exchange
	return &Browser{config: c, http: &h, oauth: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURI, Endpoint: endpoint, Scopes: scopes}, verifier: p.VerifierContext(oidc.ClientContext(context.Background(), &h), &oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}), transactions: map[string]browserTransaction{}}, nil
}

// SafeReturn permits only a same-origin absolute path. Reject encoded separators
// too so a later URL decoder cannot transform it into a network-path redirect.
func SafeReturn(raw string) bool {
	if len(raw) > 2048 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return false
	}
	decoded, e := url.PathUnescape(raw)
	if e != nil || strings.HasPrefix(decoded, "//") || strings.ContainsAny(decoded, "\\\r\n\x00") {
		return false
	}
	u, e := url.Parse(raw)
	return e == nil && !u.IsAbs() && u.Host == "" && u.User == nil
}
func browserRandom() (string, error) {
	v := make([]byte, 32)
	if _, e := rand.Read(v); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(v), nil
}

// Begin binds state, nonce and PKCE to an unpredictable browser HttpOnly cookie.
// The caller owns setting a Secure/SameSite=Lax cookie restricted to the callback.
func (b *Browser) Begin(binding, returnTo string) (string, error) {
	if len(binding) < 12 || !SafeReturn(returnTo) {
		return "", ErrInvalid
	}
	state, e := browserRandom()
	if e != nil {
		return "", e
	}
	nonce, e := browserRandom()
	if e != nil {
		return "", e
	}
	verifier := oauth2.GenerateVerifier()
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, t := range b.transactions {
		if !t.ExpiresAt.After(time.Now()) {
			delete(b.transactions, k)
		}
	}
	if len(b.transactions) >= 4096 {
		return "", errors.New("too many pending logins")
	}
	b.transactions[state] = browserTransaction{Binding: sha256.Sum256([]byte(binding)), Nonce: nonce, Verifier: verifier, ReturnTo: returnTo, ExpiresAt: time.Now().Add(5 * time.Minute)}
	return b.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}
func (b *Browser) Complete(ctx context.Context, binding, callbackURL string) (BrowserIdentity, error) {
	u, e := url.Parse(callbackURL)
	expected, _ := url.Parse(b.config.RedirectURI)
	if e != nil || u.Scheme != expected.Scheme || u.Host != expected.Host || u.EscapedPath() != expected.EscapedPath() || u.User != nil || u.Fragment != "" {
		return BrowserIdentity{}, ErrDenied
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(q["state"]) != 1 {
		return BrowserIdentity{}, ErrDenied
	}
	state := q.Get("state")
	hash := sha256.Sum256([]byte(binding))
	b.mu.Lock()
	tx, ok := b.transactions[state]
	if ok && subtle.ConstantTimeCompare(hash[:], tx.Binding[:]) == 1 {
		delete(b.transactions, state)
	}
	b.mu.Unlock()
	if !ok || !tx.ExpiresAt.After(time.Now()) || subtle.ConstantTimeCompare(hash[:], tx.Binding[:]) != 1 {
		return BrowserIdentity{}, ErrDenied
	}
	if len(q["code"]) != 1 || q.Get("code") == "" || q.Get("error") != "" {
		return BrowserIdentity{}, ErrDenied
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, b.http), 30*time.Second)
	defer cancel()
	token, e := b.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(tx.Verifier))
	if e != nil || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		return BrowserIdentity{}, ErrDenied
	}
	rawID, _ := token.Extra("id_token").(string)
	id, e := b.verifier.Verify(ctx, rawID)
	if e != nil || id.Nonce != tx.Nonce || id.Subject == "" {
		return BrowserIdentity{}, ErrDenied
	}
	access, e := b.verifier.Verify(ctx, token.AccessToken)
	if e != nil || access.Subject != id.Subject {
		return BrowserIdentity{}, ErrDenied
	}
	for _, t := range []*oidc.IDToken{id, access} {
		var claims struct {
			AuthorizedParty string `json:"azp"`
		}
		if t.Claims(&claims) != nil || len(t.Audience) != 1 || t.Audience[0] != b.config.ClientID || (claims.AuthorizedParty != "" && claims.AuthorizedParty != b.config.ClientID) {
			return BrowserIdentity{}, ErrDenied
		}
	}
	if b.config.AccessTokenType != "" {
		var purpose struct {
			Type string `json:"tokenType"`
		}
		if access.Claims(&purpose) != nil || purpose.Type != b.config.AccessTokenType {
			return BrowserIdentity{}, ErrDenied
		}
	}
	expiry := access.Expiry
	if id.Expiry.Before(expiry) {
		expiry = id.Expiry
	}
	if !token.Expiry.IsZero() && token.Expiry.Before(expiry) {
		expiry = token.Expiry
	}
	if !expiry.After(time.Now()) {
		return BrowserIdentity{}, ErrDenied
	}
	return BrowserIdentity{Subject: id.Subject, AccessToken: token.AccessToken, ReturnTo: tx.ReturnTo, ExpiresAt: expiry.Unix()}, nil
}

// Bound the discovery, JWKS and code exchange bodies before library decoding.
type browserBoundedTransport struct{ base http.RoundTripper }

func (t browserBoundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	response.Body.Close()
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("OIDC response exceeds bound")
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	return response, nil
}
