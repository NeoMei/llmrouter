// Package accountpool owns provider-independent account health policy. It does
// not decide whether an operation may be replayed or whether a request may retry.
package accountpool

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
)

type Refusal string

const (
	RefusalInvalidCredential Refusal = "invalid_credential"
	RefusalQuotaExhausted    Refusal = "quota_exhausted"
)

// Refusal and TrustedResetAt must come from a provider adapter's authenticated,
// structured protocol interpretation, never free-text matching or client input.
type FailureInput struct {
	StatusCode     int
	RetryAfter     string
	Refusal        Refusal
	TrustedResetAt time.Time
	SharedQuota    bool
}
type FailureDecision struct {
	Failure     model.UpstreamAccountFailure
	SharedQuota bool
}

// ParseRetryAfter returns a safe ordinary rate-limit cooldown. Invalid/missing
// values default to 60s; valid past dates and explicit zero allow immediate retry.
func ParseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > 600 {
			return 10 * time.Minute
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return time.Minute
	}
	return min(max(when.Sub(now), 0), 10*time.Minute)
}

func ClassifyFailure(input FailureInput, now time.Time) FailureDecision {
	var result FailureDecision
	switch input.Refusal {
	case RefusalInvalidCredential:
		result.Failure = model.UpstreamAccountFailure{Kind: model.UpstreamAccountInvalid, Reason: "invalid_credential"}
		return result
	case RefusalQuotaExhausted:
		wait := time.Hour
		if !input.TrustedResetAt.IsZero() {
			wait = min(max(input.TrustedResetAt.Sub(now), 0), 32*24*time.Hour)
		}
		if wait > 0 {
			result.Failure = model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(wait)}
			result.SharedQuota = input.SharedQuota
		}
		return result
	}
	if input.StatusCode == http.StatusTooManyRequests {
		wait := ParseRetryAfter(input.RetryAfter, now)
		if wait > 0 {
			result.Failure = model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(wait)}
		}
	}
	return result
}
