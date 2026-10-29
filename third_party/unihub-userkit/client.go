package userkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type Config struct {
	BaseURL, Issuer, ClientID, ProductID, InstanceID string
	HTTPClient                                       *http.Client
}
type Client struct {
	config Config
	http   *http.Client
}

// NewClient clones the supplied client, enforces a bounded total timeout and
// rejects every redirect, including same-origin redirects carrying a bearer.
func NewClient(c Config) (*Client, error) {
	if !validOrigin(c.BaseURL) || !validOrigin(c.Issuer) || c.ClientID == "" || c.ProductID == "" {
		return nil, ErrInvalid
	}
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	c.BaseURL = strings.TrimSuffix(c.BaseURL, "/")
	c.Issuer = strings.TrimSuffix(c.Issuer, "/")
	h := http.Client{Timeout: 10 * time.Second}
	if c.HTTPClient != nil {
		h = *c.HTTPClient
	}
	if h.Timeout <= 0 || h.Timeout > 30*time.Second {
		h.Timeout = 10 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }
	return &Client{c, &h}, nil
}
func validOrigin(value string) bool {
	u, e := url.Parse(value)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())))
}
func (c *Client) get(ctx context.Context, bearer, path string, target any) error {
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, c.config.BaseURL+path, nil)
	if e != nil {
		return ErrInvalid
	}
	r.Header.Set("Authorization", "Bearer "+bearer)
	r.Header.Set("Accept", "application/json")
	resp, e := c.http.Do(r)
	if e != nil {
		return errors.New("central identity unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return ErrDenied
		}
		return errors.New("central identity unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	if e = d.Decode(target); e != nil {
		return ErrInvalid
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (c *Client) Resolve(ctx context.Context, bearer string) (Access, error) {
	if bearer == "" || len(bearer) > 16384 || strings.ContainsAny(bearer, "\r\n") {
		return Access{}, ErrInvalid
	}
	var proof struct {
		Kind       string   `json:"kind"`
		ClientID   string   `json:"client_id"`
		ProductID  string   `json:"product_id"`
		InstanceID string   `json:"instance_id"`
		Scopes     []string `json:"scopes"`
	}
	if e := c.get(ctx, bearer, "/v1/capabilities", &proof); e != nil {
		return Access{}, e
	}
	if proof.Kind != "product" || proof.ClientID != c.config.ClientID || proof.ProductID != c.config.ProductID || proof.InstanceID != c.config.InstanceID || !slices.Contains(proof.Scopes, "access:read") {
		return Access{}, ErrDenied
	}
	var a Access
	if e := c.get(ctx, bearer, "/v1/me/product-access", &a); e != nil {
		return Access{}, e
	}
	if e := validate(a); e != nil {
		return Access{}, e
	}
	if a.Issuer != c.config.Issuer || a.ProductID != c.config.ProductID || a.InstanceID != c.config.InstanceID {
		return Access{}, ErrDenied
	}
	return a, nil
}
