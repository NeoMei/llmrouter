// Package platformbilling isolates platform model funding from local quota.
package platformbilling

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid   = errors.New("invalid platform request or configuration")
	ErrConflict  = errors.New("platform logical call scope or body conflict")
	ErrDuplicate = errors.New("platform logical call already exists; never resend upstream")
	ErrPending   = errors.New("platform billing pending reconciliation")
)

type Claims struct {
	Operations            map[string][]string `json:"operations,omitempty"`
	CredentialKind        string              `json:"credential_kind"`
	InstanceID            string              `json:"instance_id"`
	ID                    string              `json:"id"`
	Audience              string              `json:"audience"`
	ActorID               string              `json:"actor_id"`
	AccountID             string              `json:"account_id"`
	OriginProductID       string              `json:"origin_product_id"`
	Models                []string            `json:"models"`
	TaskID                string              `json:"task_id"`
	ExpiresAt             time.Time           `json:"expires_at"`
	Asset                 string              `json:"asset"`
	FundingMode           string              `json:"funding_mode"`
	BillingProfileVersion int                 `json:"billing_profile_version"`
}
type Rational struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}

// Request tariff aliases share the native metering wire contract while the
// chat admission path applies its narrower request-only validation.
type RequestTariff = metering.Price
type RequestTerm = metering.Term
type RequestUsage = metering.Usage
type RequestMeasurement = metering.Measurement
type Rate struct {
	Metered    *metering.Price `json:"metered,omitempty"`
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Asset      string          `json:"asset"`
	Status     string          `json:"status"`
	Semantics  string          `json:"semantics"`
	Input      Rational        `json:"input"`
	Output     Rational        `json:"output"`
	CacheRead  Rational        `json:"cache_read"`
	CacheWrite Rational        `json:"cache_write"`
	Reasoning  Rational        `json:"reasoning"`
}
type Usage struct {
	Metered    *metering.Usage `json:"metered,omitempty"`
	Input      string          `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	CacheRead  string          `json:"cache_read,omitempty"`
	CacheWrite string          `json:"cache_write,omitempty"`
	Reasoning  string          `json:"reasoning,omitempty"`
}
type Allocation struct {
	BatchID string `json:"batch_id"`
	Amount  string `json:"amount"`
}
type RemoteCall struct {
	WireOperation         string       `json:"wire_operation,omitempty"`
	InstanceID            string       `json:"instance_id"`
	Held                  string       `json:"held"`
	CashHoldAddress       string       `json:"cash_hold_address,omitempty"`
	Allocations           []Allocation `json:"allocations"`
	CredentialID          string       `json:"credential_id,omitempty"`
	ActorID               string       `json:"actor_id"`
	AccountID             string       `json:"account_id"`
	OriginProductID       string       `json:"origin_product_id"`
	RequestDigest         string       `json:"request_digest"`
	Rate                  Rate         `json:"rate"`
	CallID                string       `json:"call_id"`
	Status                string       `json:"status"`
	Charged               string       `json:"charged,omitempty"`
	ChargeAsset           string       `json:"charge_asset"`
	FundingMode           string       `json:"funding_mode"`
	FundingProductID      string       `json:"funding_product_id"`
	BillingProfileVersion int          `json:"billing_profile_version"`
}
type Config struct {
	// Explicit opt-in; production activation additionally requires the
	// authenticated recovery path and complete host/worker integration.
	AllowAliEmbeddingV4 bool
	AllowAliRerank      bool
	// Separate opt-in requires a knowledge producer and versioned keyring.
	EnableKnowledgeResultRead bool `json:"-"`
	// The single key is retained for old in-process fixtures. Deployments use
	// versioned keys that must be kept across restarts and rotations.
	AliEmbeddingResultKey           []byte            `json:"-"`
	AliEmbeddingResultKeys          map[string][]byte `json:"-"`
	AliEmbeddingResultActiveKey     string
	NativeHistory                   *NativeHistoryConfig
	ImagePolicies                   map[string]map[string]ImagePolicy
	NativePresetPolicies            map[string]map[string]NativePresetPolicy
	OCRPolicies                     map[string]map[string]OCRPolicy
	AudioPolicies                   map[string]map[string]AudioPolicy
	VideoPolicies                   map[string]map[string]VideoPolicy
	KnowledgePolicies               map[string]map[string]KnowledgePolicy
	CompactPolicies                 map[string]CompactPolicy
	NativeInputPolicies             map[string]map[string]NativeInputPolicy
	OperationLimits                 map[string]map[string]OperationLimits
	OperationRateIDs                map[string]map[string]map[string]string
	Enabled                         bool
	Origin, ServiceToken, Group     string
	InstanceID                      string
	PrincipalID                     int
	MaxBodyBytes                    int64
	MaxInputTokens, MaxOutputTokens int
	RateIDs                         map[string]string
	AssetRateIDs                    map[string]map[string]string
	Timeout                         time.Duration
}
type client struct {
	origin, token string
	http          *http.Client
}
type remoteError struct{ status int }

func (e *remoteError) Error() string { return fmt.Sprintf("UniHub service returned HTTP %d", e.status) }
func newClient(c Config) (*client, error) {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(c.ServiceToken) < 32 {
		return nil, ErrInvalid
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()))) {
		return nil, ErrInvalid
	}
	timeout := c.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 10 * time.Second
	}
	return &client{origin: strings.TrimSuffix(c.Origin, "/"), token: c.ServiceToken, http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *client) do(ctx context.Context, method, path, key string, body, out any) error {
	var b []byte
	var err error
	if body != nil {
		b, err = common.Marshal(body)
		if err != nil {
			return ErrInvalid
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(b))
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrPending
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return ErrPending
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &remoteError{status: resp.StatusCode}
	}
	if out != nil && common.Unmarshal(data, out) != nil {
		return ErrPending
	}
	return nil
}

// NormalizeUsage accepts only raw confirmed OpenAI Chat counters. Callers must
// never pass gateway token estimates or client-supplied billing_usage metadata.
func NormalizeUsage(u *dto.Usage) (Usage, error) {
	if u == nil || (u.BillingUsage != nil && u.BillingUsage.Estimated) {
		return Usage{}, ErrPending
	}
	p, c, cr, cw, r := u.PromptTokens, u.CompletionTokens, u.PromptTokensDetails.CachedTokens, u.PromptTokensDetails.CacheWriteTokens, u.CompletionTokenDetails.ReasoningTokens
	if p < 0 || c < 0 || cr < 0 || cw < 0 || r < 0 || u.TotalTokens < 0 || u.InputTokens < 0 || u.OutputTokens < 0 || u.PromptCacheHitTokens < 0 || u.ClaudeCacheCreation5mTokens != 0 || u.ClaudeCacheCreation1hTokens != 0 || u.PromptTokensDetails.TextTokens < 0 || u.CompletionTokenDetails.TextTokens < 0 || cr > p || cw > p-cr || r > c || u.PromptTokensDetails.CachedCreationTokens != 0 || u.PromptTokensDetails.AudioTokens != 0 || u.PromptTokensDetails.ImageTokens != 0 || u.CompletionTokenDetails.AudioTokens != 0 || u.CompletionTokenDetails.ImageTokens != 0 {
		return Usage{}, ErrInvalid
	}
	return Usage{Input: strconv.Itoa(p - cr - cw), Output: strconv.Itoa(c - r), CacheRead: strconv.Itoa(cr), CacheWrite: strconv.Itoa(cw), Reasoning: strconv.Itoa(r)}, nil
}
func reserveBound(rate Rate, input, output int) (string, error) {
	if rate.Status != "published" {
		return "", ErrInvalid
	}
	if rate.Semantics == "metered_v2" {
		return requestBound(rate)
	}
	if rate.Metered != nil || rate.Semantics != "exclusive_v1" {
		return "", ErrInvalid
	}
	vals := []Rational{rate.Input, rate.CacheRead, rate.CacheWrite, rate.Output, rate.Reasoning}
	rat := make([]*big.Rat, 5)
	for i, v := range vals {
		if len(v.Numerator) > 128 || len(v.Denominator) > 128 {
			return "", ErrInvalid
		}
		n, ok := new(big.Int).SetString(v.Numerator, 10)
		if !ok || n.Sign() < 0 {
			return "", ErrInvalid
		}
		d, ok := new(big.Int).SetString(v.Denominator, 10)
		if !ok || d.Sign() <= 0 {
			return "", ErrInvalid
		}
		rat[i] = new(big.Rat).SetFrac(n, d)
	}
	in := rat[0]
	for _, v := range rat[1:3] {
		if v.Cmp(in) > 0 {
			in = v
		}
	}
	out := rat[3]
	if rat[4].Cmp(out) > 0 {
		out = rat[4]
	}
	total := new(big.Rat).Add(new(big.Rat).Mul(in, new(big.Rat).SetInt64(int64(input))), new(big.Rat).Mul(out, new(big.Rat).SetInt64(int64(output))))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(total.Num(), total.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Sign() == 0 {
		q.SetInt64(1)
	}
	if q.BitLen() > 63 {
		return "", ErrInvalid
	}
	return q.String(), nil
}

// HTTPStatus exposes only bounded classifications, never service bodies/secrets.
func HTTPStatus(err error) int {
	if errors.Is(err, ErrDuplicate) || errors.Is(err, ErrConflict) {
		return 409
	}
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrNativeHistory) {
		return 400
	}
	var re *remoteError
	if errors.As(err, &re) && re.status >= 400 && re.status < 500 {
		return re.status
	}
	return 503
}

func (remote RemoteCall) matches(row Call) bool {
	localInstance, remoteInstance := row.InstanceID, remote.InstanceID
	if localInstance == "" {
		localInstance = "default"
	}
	if remoteInstance == "" {
		remoteInstance = "default"
	}
	if localInstance != remoteInstance || row.WireOperation != remote.WireOperation {
		return false
	}
	if row.Operation != "" {
		var frozen Rate
		if common.Unmarshal([]byte(row.RateSnapshot), &frozen) != nil || !reflect.DeepEqual(frozen, remote.Rate) || remote.Held != row.Amount || remote.CredentialID != row.CredentialID {
			return false
		}
		if row.FundingSnapshot != "" && row.FundingSnapshot != remote.fundingSnapshot() {
			return false
		}
	}
	asset, mode, err := normalizeFunding(row.Asset, row.FundingMode)
	if err != nil || row.BillingProfileVersion < 0 {
		return false
	}
	remoteAsset, remoteMode, err := normalizeFunding(remote.ChargeAsset, remote.FundingMode)
	if err != nil || remote.BillingProfileVersion != row.BillingProfileVersion {
		return false
	}
	rateAsset, err := normalizeAsset(remote.Rate.Asset)
	if err != nil || rateAsset != asset || remoteAsset != asset || remoteMode != mode {
		return false
	}
	funder := remote.FundingProductID
	if funder == "" && remoteAsset == "CREDIT/6" {
		funder = "llmrouter"
	}
	localFunder := row.FundingProductID
	if localFunder == "" && asset == "CREDIT/6" {
		localFunder = "llmrouter"
	}
	return funder == "llmrouter" && funder == localFunder && remote.CallID == row.CallID && remote.ActorID == row.ActorID && remote.AccountID == row.AccountID && remote.OriginProductID == row.OriginProductID && remote.RequestDigest == row.Digest && remote.Rate.ID == row.RateID && remote.Rate.Model == row.Model
}

// Missing fields belong to the original CREDIT contract, never to cash funding.
func normalizeAsset(asset string) (string, error) {
	if asset == "" {
		return "CREDIT/6", nil
	}
	if asset != "CREDIT/6" && asset != "CNY/6" {
		return "", ErrInvalid
	}
	return asset, nil
}

func normalizeFunding(asset, mode string) (string, string, error) {
	asset, err := normalizeAsset(asset)
	if err != nil {
		return "", "", err
	}
	if mode == "" {
		mode = "credits"
	}
	if (asset == "CREDIT/6" && mode != "credits") || (asset == "CNY/6" && mode != "prepaid_cash") {
		return "", "", ErrInvalid
	}
	return asset, mode, nil
}

// requestBound intentionally accepts only one flat chat request term, with no
// step/minimum or token price. The selected tariff is immutable for the call.
func requestBound(r Rate) (string, error) {
	p := r.Metered
	if p == nil || p.Version != "metered_v2" || p.Operation != "chat" || p.Policy != "sum" || len(p.Terms) != 1 || r.Input != (Rational{}) || r.Output != (Rational{}) || r.CacheRead != (Rational{}) || r.CacheWrite != (Rational{}) || r.Reasoning != (Rational{}) {
		return "", ErrInvalid
	}
	t := p.Terms[0]
	if t.Key != "requests" || t.Unit != "request" || t.ProviderUnit != "" || t.Step != nil || t.Minimum != nil || !canonicalCount.MatchString(t.Price.Numerator) || !canonicalCount.MatchString(t.Price.Denominator) || len(t.Price.Numerator) > 38 || len(t.Price.Denominator) > 38 {
		return "", ErrInvalid
	}
	n, _ := new(big.Int).SetString(t.Price.Numerator, 10)
	d, _ := new(big.Int).SetString(t.Price.Denominator, 10)
	if d.Sign() <= 0 {
		return "", ErrInvalid
	}
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n, d, rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Sign() == 0 {
		q.SetInt64(1)
	}
	if q.BitLen() > 63 {
		return "", ErrInvalid
	}
	return q.String(), nil
}

func (r RemoteCall) fundingSnapshot() string {
	b, _ := common.Marshal(struct {
		CashHoldAddress string
		Allocations     []Allocation
	}{r.CashHoldAddress, r.Allocations})
	return string(b)
}
