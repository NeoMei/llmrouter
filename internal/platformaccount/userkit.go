package platformaccount

import (
	"context"
	"net/http"
	"net/url"
	"sync/atomic"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
)

var runtimeConfig atomic.Pointer[Config]

// Configure records the startup-validated product configuration for auth/refresh.
func Configure(c Config) { runtimeConfig.Store(&c) }
func Current() (Config, error) {
	if c := runtimeConfig.Load(); c != nil {
		return *c, nil
	}
	return FromEnv()
}
func (c Config) Provider() (userkit.Provider, error) {
	instance := c.InstanceID
	if instance == "" {
		instance = "default"
	}
	origin, _ := url.Parse(c.RedirectURI)
	return userkit.NewClient(userkit.Config{BaseURL: c.APIOrigin, Issuer: c.PublicOrigin, ClientID: c.ClientID, ProductID: c.ProductID, InstanceID: instance, HTTPClient: &http.Client{Transport: proofOriginTransport{origin: origin.Scheme + "://" + origin.Host}}})
}

type proofOriginTransport struct{ origin string }

func (t proofOriginTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("Origin", t.origin)
	return http.DefaultTransport.RoundTrip(r)
}
func (c Config) Resolve(ctx context.Context, bearer string) (userkit.Access, error) {
	p, e := c.Provider()
	if e != nil {
		return userkit.Access{}, e
	}
	return p.Resolve(ctx, bearer)
}
