package accountpool

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"time"
)

// DeferNativeOutcome defers only HTTP 2xx semantics to CompleteNativeOutcome.
// BeginSend consumes the request; Observe retains non-2xx, transport and veto ownership.
func DeferNativeOutcome(c *gin.Context) {
	State(c).NativeRequested = true
	State(c).NativeAllStatusesRequested = false
}

// DeferAllStatusNativeOutcome transfers every HTTP status to the native parser.
// Its caller MUST route all responses (including non-2xx) to that parser and
// complete them exactly once. Ordinary adapters should use DeferNativeOutcome.
func DeferAllStatusNativeOutcome(c *gin.Context) {
	DeferNativeOutcome(c)
	State(c).NativeAllStatusesRequested = true
}

// ResetNativeOutcome clears adapter-local state before a new audio attempt,
// including a conversion/URL/header failure that never reached BeginSend.
func ResetNativeOutcome(c *gin.Context) {
	state := State(c)
	state.NativeRequested = false
	state.NativePending = false
	state.NativeAllStatusesRequested = false
	state.NativeAllStatuses = false
}

// CompleteNativeOutcome records one validated outcome against the immutable
// Sent receipt. nil means validated terminal success. An unknown/malformed
// outcome must not call this with nil; it receives no success credit.
func CompleteNativeOutcome(c *gin.Context, failure *FailureInput) error {
	state := State(c)
	if !state.NativePending {
		return errors.New("native outcome is not pending")
	}
	state.NativePending = false
	if failure != nil && (failure.Refusal == RefusalInvalidCredential || failure.Refusal == RefusalQuotaExhausted || failure.StatusCode == 429) {
		state.Uncertain = false
		state.UnsafeResponse = false
	}
	if state.Sent == nil {
		return nil
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
	defer cancel()
	var err error
	if failure == nil {
		err = model.RecordUpstreamAccountSuccess(ctx, state.DB, *state.Sent, state.Scope, now)
	} else {
		decision := ClassifyFailure(*failure, now)

		scope := state.Scope
		if decision.SharedQuota {
			scope = model.UpstreamAccountScope{QuotaGroup: state.Sent.Ref.QuotaGroup}
		}
		if !decision.Failure.RetryAt.IsZero() {
			SetUnavailableHeader(c, &model.UpstreamAccountUnavailable{RetryAt: decision.Failure.RetryAt})
		}
		if decision.Failure.Kind != "" {
			err = model.RecordUpstreamAccountFailure(ctx, state.DB, *state.Sent, scope, decision.Failure, now)
		}
	}
	state.PersistenceError = err
	return err
}
