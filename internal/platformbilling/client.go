// Package platformbilling isolates platform model credits from local quota.
package platformbilling

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
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
	ID              string    `json:"id"`
	Audience        string    `json:"audience"`
	ActorID         string    `json:"actor_id"`
	AccountID       string    `json:"account_id"`
	OriginProductID string    `json:"origin_product_id"`
	Models          []string  `json:"models"`
	TaskID          string    `json:"task_id"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type Rational struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}
type Rate struct {
	ID         string   `json:"id"`
	Model      string   `json:"model"`
	Status     string   `json:"status"`
	Semantics  string   `json:"semantics"`
	Input      Rational `json:"input"`
	Output     Rational `json:"output"`
	CacheRead  Rational `json:"cache_read"`
	CacheWrite Rational `json:"cache_write"`
	Reasoning  Rational `json:"reasoning"`
}
type Usage struct {
	Input      string `json:"input"`
	Output     string `json:"output"`
	CacheRead  string `json:"cache_read"`
	CacheWrite string `json:"cache_write"`
	Reasoning  string `json:"reasoning"`
}
type RemoteCall struct {
	ActorID         string `json:"actor_id"`
	AccountID       string `json:"account_id"`
	OriginProductID string `json:"origin_product_id"`
	RequestDigest   string `json:"request_digest"`
	Rate            Rate   `json:"rate"`
	CallID          string `json:"call_id"`
	Status          string `json:"status"`
	Charged         string `json:"charged,omitempty"`
}
type Config struct {
	Enabled                         bool
	Origin, ServiceToken, Group     string
	PrincipalID                     int
	MaxBodyBytes                    int64
	MaxInputTokens, MaxOutputTokens int
	RateIDs                         map[string]string
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
	return Usage{strconv.Itoa(p - cr - cw), strconv.Itoa(c - r), strconv.Itoa(cr), strconv.Itoa(cw), strconv.Itoa(r)}, nil
}
func reserveBound(rate Rate, input, output int) (string, error) {
	if rate.Status != "published" || rate.Semantics != "exclusive_v1" {
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
	if errors.Is(err, ErrInvalid) {
		return 400
	}
	var re *remoteError
	if errors.As(err, &re) && re.status >= 400 && re.status < 500 {
		return re.status
	}
	return 503
}

func (remote RemoteCall) matches(row Call) bool {
	return remote.CallID == row.CallID && remote.ActorID == row.ActorID && remote.AccountID == row.AccountID && remote.OriginProductID == row.OriginProductID && remote.RequestDigest == row.Digest && remote.Rate.ID == row.RateID && remote.Rate.Model == row.Model
}
