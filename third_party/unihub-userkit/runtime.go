package userkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const RuntimeAsset = "LR_QUOTA/0"
const RuntimeUnitVersion = "lr-quota-v1"

// ServiceConfig contains a product-instance credential, never a user credential.
// Runtime amounts are exact native LR quota integers, not cash or CREDIT/6.
type ServiceConfig struct {
	BaseURL, ProductID, InstanceID, ServiceToken string
	BrowserOrigin                                string
	HTTPClient                                   *http.Client
}

type RuntimeClient struct {
	config ServiceConfig
	http   *http.Client
}
type RuntimeBinding struct {
	ID                string `json:"id"`
	LocalUserID       string `json:"local_user_id"`
	UserID            string `json:"user_id"`
	Issuer            string `json:"issuer"`
	Subject           string `json:"subject"`
	PersonalAccountID string `json:"personal_account_id"`
	ProductID         string `json:"product_id"`
	InstanceID        string `json:"instance_id"`
}
type BindRequest struct {
	LocalUserID string `json:"local_user_id"`
	BindingKey  string `json:"binding_key"`
}
type RuntimeAccess struct {
	Binding RuntimeBinding `json:"binding"`
	Access  Access         `json:"access"`
}
type RuntimeReserveRequest struct {
	CallID        string `json:"call_id"`
	LocalUserID   string `json:"local_user_id"`
	LocalKeyID    string `json:"local_key_id"`
	AccountID     string `json:"account_id"`
	RequestDigest string `json:"request_digest"`
	Model         string `json:"model"`
	Operation     string `json:"operation"`
	Asset         string `json:"asset"`
	UnitVersion   string `json:"unit_version"`
	PriceVersion  string `json:"price_version"`
	PricingDigest string `json:"pricing_digest"`
	Amount        string `json:"amount"`
	ParentCallID  string `json:"parent_call_id,omitempty"`
}
type RuntimeSettlementRequest struct {
	Amount        string          `json:"amount"`
	PricingDigest string          `json:"pricing_digest"`
	Usage         json.RawMessage `json:"usage"`
}
type RuntimeTopUpRequest struct {
	RequestKey   string `json:"request_key"`
	Amount       string `json:"amount"`
	AcceptedCall bool   `json:"accepted_call,omitempty"`
}
type RuntimeReleaseRequest struct {
	Reason string `json:"reason"`
}
type RuntimeCall struct {
	RuntimeReserveRequest
	ProductID   string `json:"product_id"`
	InstanceID  string `json:"instance_id"`
	ActorID     string `json:"actor_id"`
	Held        string `json:"held"`
	Charged     string `json:"charged"`
	Status      string `json:"status"`
	FinalDigest string `json:"final_digest,omitempty"`
}
type RuntimeBalance struct {
	ProductID   string `json:"product_id"`
	InstanceID  string `json:"instance_id"`
	AccountID   string `json:"account_id"`
	Asset       string `json:"asset"`
	UnitVersion string `json:"unit_version"`
	Available   string `json:"available"`
	Held        string `json:"held"`
	Consumed    string `json:"consumed"`
}
type RuntimeAdjustmentRequest struct {
	RequestKey        string  `json:"request_key"`
	LocalUserID       string  `json:"local_user_id"`
	AccountID         string  `json:"account_id"`
	Amount            string  `json:"amount"`
	SetAvailable      *string `json:"set_available,omitempty"`
	ExpectedAvailable string  `json:"expected_available,omitempty"`
	Reason            string  `json:"reason"`
	Asset             string  `json:"asset"`
	UnitVersion       string  `json:"unit_version"`
}
type RuntimeImportRequest struct {
	BatchID        string          `json:"batch_id"`
	LegacyUserID   string          `json:"legacy_user_id"`
	SnapshotDigest string          `json:"snapshot_digest"`
	LocalUserID    string          `json:"local_user_id"`
	AccountID      string          `json:"account_id"`
	Amount         string          `json:"amount"`
	Asset          string          `json:"asset"`
	UnitVersion    string          `json:"unit_version"`
	SourceUnit     string          `json:"source_unit"`
	Conversion     string          `json:"conversion"`
	SourceEvidence json.RawMessage `json:"source_evidence,omitempty"`
}
type RuntimeAwardRequest struct {
	ReceiptID     string          `json:"receipt_id"`
	SourceKind    string          `json:"source_kind"`
	ReceiptDigest string          `json:"receipt_digest"`
	Reason        string          `json:"reason"`
	LocalUserID   string          `json:"local_user_id"`
	AccountID     string          `json:"account_id"`
	Amount        string          `json:"amount"`
	Asset         string          `json:"asset"`
	UnitVersion   string          `json:"unit_version"`
	SourcePayload json.RawMessage `json:"source_payload,omitempty"`
}

func (c *RuntimeClient) Award(ctx context.Context, in RuntimeAwardRequest) (out RuntimeAdjustment, e error) {
	e = c.request(ctx, "POST", "/awards", "", in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}

type RuntimeAdjustment struct {
	ID            string `json:"id"`
	ProductID     string `json:"product_id"`
	InstanceID    string `json:"instance_id"`
	AccountID     string `json:"account_id"`
	Amount        string `json:"amount"`
	Status        string `json:"status"`
	RequestDigest string `json:"request_digest"`
}
type RuntimeBudgetRequest struct {
	LocalUserID string `json:"local_user_id"`
	AccountID   string `json:"account_id"`
	Version     int64  `json:"version"`
	MaxTotal    string `json:"max_total"`
	MaxPerCall  string `json:"max_per_call"`
}
type RuntimeBudget struct {
	AccountID       string  `json:"account_id"`
	OriginProductID string  `json:"origin_product_id"`
	InstanceID      string  `json:"instance_id"`
	Asset           string  `json:"asset"`
	FundingMode     string  `json:"funding_mode"`
	Version         int64   `json:"version"`
	MaxTotal        string  `json:"max_total"`
	MaxPerCall      string  `json:"max_per_call"`
	Held            string  `json:"held"`
	Used            string  `json:"used"`
	Remaining       *string `json:"remaining"`
}

func (c *RuntimeClient) Budget(ctx context.Context, localID, account string) (out RuntimeBudget, e error) {
	e = c.request(ctx, "GET", "/users/"+url.PathEscape(localID)+"/budget?account_id="+url.QueryEscape(account), "", nil, &out)
	if e == nil {
		e = c.scope(out.OriginProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) SetBudget(ctx context.Context, actor string, in RuntimeBudgetRequest) (out RuntimeBudget, e error) {
	e = c.request(ctx, "PUT", "/users/"+url.PathEscape(in.LocalUserID)+"/budget", actor, in, &out)
	if e == nil {
		e = c.scope(out.OriginProductID, out.InstanceID)
	}
	return
}

type RuntimeManagedUser struct {
	User       User              `json:"user"`
	Assignment RuntimeAssignment `json:"assignment"`
}
type RuntimeAssignment struct {
	ProductID  string   `json:"product_id"`
	InstanceID string   `json:"instance_id"`
	Roles      []string `json:"roles"`
	Active     bool     `json:"active"`
	Version    int64    `json:"assignment_version"`
}
type RuntimeUserAccessRequest struct {
	UserID  string   `json:"user_id"`
	Roles   []string `json:"roles"`
	Active  bool     `json:"active"`
	Version int64    `json:"version"`
}

type RuntimeError struct {
	Status int
	Code   string
}

func (e *RuntimeError) Error() string { return "central product runtime: " + e.Code }

func NewRuntimeClient(c ServiceConfig) (*RuntimeClient, error) {
	if !validOrigin(c.BaseURL) || (c.BrowserOrigin != "" && !validOrigin(c.BrowserOrigin)) || c.ProductID == "" || len(c.ServiceToken) < 32 || strings.ContainsAny(c.ServiceToken, "\r\n") {
		return nil, ErrInvalid
	}
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	c.BaseURL = strings.TrimSuffix(c.BaseURL, "/")
	c.BrowserOrigin = strings.TrimSuffix(c.BrowserOrigin, "/")
	h := http.Client{Timeout: 10 * time.Second}
	if c.HTTPClient != nil {
		h = *c.HTTPClient
	}
	if h.Timeout <= 0 || h.Timeout > 30*time.Second {
		h.Timeout = 10 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("runtime redirect rejected") }
	return &RuntimeClient{c, &h}, nil
}
func (c *RuntimeClient) request(ctx context.Context, method, path, actor string, body, target any) error {
	var raw []byte
	var e error
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return ErrInvalid
		}
	}
	if strings.ContainsAny(actor, "\r\n") || len(actor) > 16384 {
		return ErrInvalid
	}
	r, e := http.NewRequestWithContext(ctx, method, c.config.BaseURL+"/internal/v1/product-runtime"+path, bytes.NewReader(raw))
	if e != nil {
		return ErrInvalid
	}
	r.Header.Set("Authorization", "Bearer "+c.config.ServiceToken)
	r.Header.Set("Content-Type", "application/json")
	if actor != "" {
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
	}
	response, e := c.http.Do(r)
	if e != nil {
		return errors.New("central product runtime unavailable")
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 {
		return ErrInvalid
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var v struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &v)
		if v.Error == "" {
			v.Error = "request_failed"
		}
		return &RuntimeError{response.StatusCode, v.Error}
	}
	if e = json.Unmarshal(data, target); e != nil {
		return ErrInvalid
	}
	return nil
}
func (c *RuntimeClient) scope(product, instance string) error {
	if product != c.config.ProductID || instance != c.config.InstanceID {
		return ErrDenied
	}
	return nil
}
func (c *RuntimeClient) Bind(ctx context.Context, actor string, in BindRequest) (out RuntimeBinding, e error) {
	e = c.request(ctx, "POST", "/bindings", actor, in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) Access(ctx context.Context, localID string) (out RuntimeAccess, e error) {
	e = c.request(ctx, "GET", "/users/"+url.PathEscape(localID)+"/access", "", nil, &out)
	if e == nil {
		e = c.scope(out.Binding.ProductID, out.Binding.InstanceID)
	}
	return
}
func (c *RuntimeClient) Balance(ctx context.Context, localID, account string) (out RuntimeBalance, e error) {
	e = c.request(ctx, "GET", "/users/"+url.PathEscape(localID)+"/balance?account_id="+url.QueryEscape(account), "", nil, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) Reserve(ctx context.Context, in RuntimeReserveRequest) (out RuntimeCall, e error) {
	e = c.request(ctx, "POST", "/calls/reserve", "", in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) Call(ctx context.Context, id string) (out RuntimeCall, e error) {
	e = c.request(ctx, "GET", "/calls/"+url.PathEscape(id), "", nil, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) Settle(ctx context.Context, id string, in RuntimeSettlementRequest) (out RuntimeCall, e error) {
	e = c.request(ctx, "POST", "/calls/"+url.PathEscape(id)+"/settle", "", in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) TopUp(ctx context.Context, id string, in RuntimeTopUpRequest) (out RuntimeCall, e error) {
	e = c.request(ctx, "POST", "/calls/"+url.PathEscape(id)+"/topup", "", in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) Release(ctx context.Context, id string, in RuntimeReleaseRequest) (out RuntimeCall, e error) {
	e = c.request(ctx, "POST", "/calls/"+url.PathEscape(id)+"/release", "", in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) AdjustCredit(ctx context.Context, actor string, in RuntimeAdjustmentRequest) (out RuntimeAdjustment, e error) {
	e = c.request(ctx, "POST", "/credit-adjustments", actor, in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) ImportBalance(ctx context.Context, in RuntimeImportRequest) (out RuntimeAdjustment, e error) {
	e = c.request(ctx, "POST", "/migrations", "", in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
func (c *RuntimeClient) Users(ctx context.Context, actor, query string) (out []RuntimeManagedUser, e error) {
	e = c.request(ctx, "GET", "/users?q="+url.QueryEscape(query), actor, nil, &out)
	for _, u := range out {
		if e == nil {
			e = c.scope(u.Assignment.ProductID, u.Assignment.InstanceID)
		}
	}
	return
}
func (c *RuntimeClient) SetUserAccess(ctx context.Context, actor string, in RuntimeUserAccessRequest) (out RuntimeAssignment, e error) {
	e = c.request(ctx, "PATCH", "/users/"+url.PathEscape(in.UserID)+"/access", actor, in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
