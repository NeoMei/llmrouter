package userkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ProductFinanceConfig is server-only configuration. Never construct this
// client in browser code or expose ServiceToken to an end user.
type ProductFinanceConfig struct {
	BaseURL, ProductID, InstanceID, ServiceToken string
}

type ProductFinanceClient struct {
	config ProductFinanceConfig
	http   *http.Client
}

type FinanceResult struct {
	StatusCode int
	Body       []byte
}

var financeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var financeActor = regexp.MustCompile(`^upa_[0-9a-f]{64}$`)
var financeServiceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

var financePaths = []struct{ method, path string }{
	{"GET", "/me"}, {"GET", "/accounts"}, {"GET", "/capabilities"}, {"GET", "/catalog"}, {"GET", "/billing-presets"}, {"GET", "/products"},
	{"GET", "/products/{product}/billing-profile"}, {"GET", "/products/{product}/model-consistency"},
	{"GET", "/accounts/{account}/resources"}, {"GET", "/accounts/{account}/wallets"}, {"GET", "/accounts/{account}/subscriptions"}, {"GET", "/accounts/{account}/orders"},
	{"GET", "/accounts/{account}/usage-budget"}, {"GET", "/accounts/{account}/api-keys"}, {"GET", "/accounts/{account}/calls"}, {"GET", "/accounts/{account}/calls/{call}"},
	{"GET", "/accounts/{account}/usage/summary"}, {"GET", "/accounts/{account}/operations"}, {"GET", "/accounts/{account}/operations/{operation}"}, {"GET", "/accounts/{account}/recharges/{order}"},
	{"POST", "/accounts/{account}/recharges"}, {"POST", "/accounts/{account}/recharges/{order}/sandbox-settle"}, {"POST", "/accounts/{account}/api-keys"},
	{"DELETE", "/accounts/{account}/api-keys/{key}"}, {"PUT", "/accounts/{account}/usage-budget"},
}

func NewProductFinanceClient(c ProductFinanceConfig) (*ProductFinanceClient, error) {
	if !validOrigin(c.BaseURL) || !financeServiceID.MatchString(c.ProductID) || (c.InstanceID != "" && !financeServiceID.MatchString(c.InstanceID)) || len(c.ServiceToken) < 32 || strings.ContainsAny(c.ServiceToken, " \r\n\t") {
		return nil, ErrInvalid
	}
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	c.BaseURL = strings.TrimSuffix(c.BaseURL, "/")
	// Use a private transport with default TLS verification, no proxy, no
	// caller-provided dialer, and no connection reuse. The bearer-bearing
	// request can only be routed by the validated URL and system resolver.
	transport := &http.Transport{DisableKeepAlives: true}
	h := http.Client{Timeout: 10 * time.Second, Transport: transport}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("product finance redirect rejected") }
	return &ProductFinanceClient{config: c, http: &h}, nil
}

// Do accepts only a fixed business method/path pair. It has no caller-selected
// host, service authorization, or retry behavior; the actor is sent only in
// the private actor header and the service token remains this client's secret.
func (c *ProductFinanceClient) Do(ctx context.Context, method, path string, query url.Values, actor string, body []byte, idempotencyKey string) (*FinanceResult, error) {
	rechargeCreate := method == http.MethodPost && strings.HasPrefix(path, "/accounts/") && strings.HasSuffix(path, "/recharges")
	if len(body) > 1<<20 || (len(body) != 0 && !json.Valid(body)) || ((method == http.MethodGet || method == http.MethodDelete) && len(body) != 0) || !financeActor.MatchString(actor) || (idempotencyKey != "" && (len(idempotencyKey) > 256 || strings.ContainsAny(idempotencyKey, " \r\n\t"))) || (idempotencyKey != "" && !rechargeCreate) || (rechargeCreate && idempotencyKey == "") || !financeRouteAllowed(method, path) || !financeQueryAllowed(query) {
		return nil, ErrInvalid
	}
	target := c.config.BaseURL + "/internal/v1/product-finance" + path
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	r, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	// net/http may otherwise replay an idempotency-keyed request after a stale
	// keep-alive connection. Deliberately omit GetBody for this one-shot client.
	r.GetBody = nil
	if len(body) == 0 {
		// Preserve a non-replayable empty body for idempotent methods too.
		r.Body = io.NopCloser(strings.NewReader(""))
	}
	r.Header.Set("Authorization", "Bearer "+c.config.ServiceToken)
	r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
	r.Header.Set("Accept", "application/json")
	if len(body) != 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		r.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, errors.New("central product finance unavailable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, ErrInvalid
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, errors.New("product finance redirect rejected")
	}
	return &FinanceResult{StatusCode: resp.StatusCode, Body: data}, nil
}

func financeRouteAllowed(method, path string) bool {
	if strings.ContainsAny(path, "?#%\\\r\n\t") || !strings.HasPrefix(path, "/") {
		return false
	}
	for _, route := range financePaths {
		if route.method != method {
			continue
		}
		want, got := strings.Split(route.path, "/"), strings.Split(path, "/")
		if len(want) != len(got) {
			continue
		}
		match := true
		for i := range want {
			if strings.HasPrefix(want[i], "{") && strings.HasSuffix(want[i], "}") {
				match = match && got[i] != "." && got[i] != ".." && financeID.MatchString(got[i])
			} else {
				match = match && want[i] == got[i]
			}
		}
		if match {
			return true
		}
	}
	return false
}

func financeQueryAllowed(query url.Values) bool {
	if len(query.Encode()) > 4096 {
		return false
	}
	allowed := map[string]bool{"product_id": true, "instance_id": true, "funding_mode": true, "from": true, "to": true, "cursor": true, "limit": true, "origin_product_id": true, "model": true, "asset": true, "account_id": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || len(values[0]) > 1024 || strings.ContainsAny(values[0], "\r\n") {
			return false
		}
	}
	return true
}
