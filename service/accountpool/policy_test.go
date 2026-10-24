package accountpool

import (
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		value string
		want  time.Duration
	}{
		{"90", 90 * time.Second}, {"0", 0}, {"-1", time.Minute}, {"junk", time.Minute},
		{"999999999999999999999999999", 10 * time.Minute}, {"Sat, 24 Oct 2026 00:02:00 GMT", 2 * time.Minute},
		{"Fri, 23 Oct 2026 00:02:00 GMT", 0},
	} {
		t.Run(tt.value, func(t *testing.T) { assert.Equal(t, tt.want, ParseRetryAfter(tt.value, now)) })
	}
}

func TestClassifyFailure(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		input  FailureInput
		kind   string
		reason string
		wait   time.Duration
		group  bool
	}{
		{"generic forbidden", FailureInput{StatusCode: 403}, "", "", 0, false},
		{"server", FailureInput{StatusCode: 503}, "", "", 0, false},
		{"rate", FailureInput{StatusCode: 429}, "cooldown", "rate_limit", time.Minute, false},
		{"revoked", FailureInput{StatusCode: 401, Refusal: RefusalInvalidCredential}, "invalid", "invalid_credential", 0, false},
		{"quota", FailureInput{StatusCode: 403, Refusal: RefusalQuotaExhausted, SharedQuota: true}, "cooldown", "quota_exhausted", time.Hour, true},
		{"trusted reset bounded", FailureInput{StatusCode: 429, Refusal: RefusalQuotaExhausted, TrustedResetAt: now.Add(60 * 24 * time.Hour)}, "cooldown", "quota_exhausted", 32 * 24 * time.Hour, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := ClassifyFailure(tt.input, now)
			assert.Equal(t, tt.kind, string(out.Failure.Kind))
			assert.Equal(t, tt.reason, out.Failure.Reason)
			assert.Equal(t, tt.group, out.SharedQuota)
			if tt.wait > 0 {
				assert.Equal(t, now.Add(tt.wait), out.Failure.RetryAt)
			}
		})
	}
}
