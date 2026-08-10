package modelcatalog

import (
	"errors"
	"net/http"
	"time"
)

// Target is a candidate channel for a public model route.
type Target struct {
	ChannelID int
	TargetID  int
	Priority  int
	Weight    int
	Wire      string
}

// AttemptResult records the outcome of a single upstream attempt.
type AttemptResult struct {
	Status int
	Reason string
}

// Policy selects the next target for a resolved route, bounded by the plan
// constraints: ordered fallback, only on retryable failures, and only
// when affinity is not pinned.
type Policy struct {
	targets []Target
}

// NewPolicy builds a policy from an ordered list of targets. Targets should be
// sorted by priority/ID, as produced by the catalog resolver.
func NewPolicy(targets []Target) *Policy {
	return &Policy{targets: append([]Target(nil), targets...)}
}

// NextAfter returns the next target to try after the given channel fails, or
// nil if no further retry is allowed.
func (p *Policy) NextAfter(channelID int, result AttemptResult) *Target {
	if !isRetryableFailure(result) {
		return nil
	}
	for i, t := range p.targets {
		if t.ChannelID == channelID {
			// Bounded to the next ordered target; callers enforce the overall retry budget.
			if i+1 >= len(p.targets) {
				return nil
			}
			return &p.targets[i+1]
		}
	}
	return nil
}

func isRetryableFailure(result AttemptResult) bool {
	if result.Status == 0 {
		return true // network failure / no status
	}
	if result.Status == http.StatusRequestTimeout || result.Status == 408 {
		return true
	}
	if result.Status == http.StatusConflict || result.Status == 409 {
		return retryableConflict(result.Reason)
	}
	if result.Status == http.StatusTooManyRequests || result.Status == 429 {
		return true
	}
	if result.Status >= http.StatusInternalServerError {
		return true
	}
	return false
}

func retryableConflict(reason string) bool {
	return reason == "retryable_conflict" || reason == "rate_limited"
}

// RouteDecisionTrace is the bounded audit record attached to a request log.
type RouteDecisionTrace struct {
	RequestedModel string    `json:"requested_model"`
	PublicModel    string    `json:"public_model"`
	ResolvedModel  string    `json:"resolved_model"`
	SelectedWire   string    `json:"selected_wire"`
	RouteID        int       `json:"route_id"`
	Attempts       []Attempt `json:"attempts"`
}

// Attempt records a single upstream attempt in the trace.
type Attempt struct {
	ChannelID  int    `json:"channel_id"`
	Wire       string `json:"wire"`
	Status     int    `json:"status"`
	Reason     string `json:"reason"`
	DurationMs int64  `json:"duration_ms"`
}

// NewAttempt builds an attempt with a duration computed from start time.
func NewAttempt(channelID int, wire string, status int, reason string, start time.Time) Attempt {
	return Attempt{
		ChannelID:  channelID,
		Wire:       wire,
		Status:     status,
		Reason:     reason,
		DurationMs: time.Since(start).Milliseconds(),
	}
}

// ErrNoFallbackTarget is returned when the policy cannot provide a retry target.
var ErrNoFallbackTarget = errors.New("no fallback target available")
