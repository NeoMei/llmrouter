package accountpool

import (
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestNativeOutcomeDoesNotClearHealthBeforeSemanticSuccess(t *testing.T) {
	c, db, refs := relayPoolFixture(t)
	// A new selection after a temporary refusal may legitimately complete later;
	// an HTTP 200 error must not clear the older refusal while being decoded.
	selected := *State(c).Selected
	require.NoError(t, model.RecordUpstreamAccountFailure(c.Request.Context(), db, selected, State(c).Scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: time.Now().Add(time.Minute)}, time.Now()))
	DeferNativeOutcome(c)
	require.NoError(t, BeginSend(c))
	require.NoError(t, Observe(c, &http.Response{StatusCode: 200, Header: http.Header{}}, nil))
	var before []model.UpstreamAccountState
	require.NoError(t, db.Find(&before).Error)
	require.Len(t, before, 1)
	assert.Equal(t, model.UpstreamAccountCooldown, before[0].Kind)
	State(c).Selected = &model.UpstreamAccountSelection{Ref: refs[1]}
	require.NoError(t, CompleteNativeOutcome(c, &FailureInput{Refusal: RefusalInvalidCredential}))
	require.Error(t, CompleteNativeOutcome(c, nil))
	_, err := model.SelectUpstreamAccount(c.Request.Context(), db, refs[:1], State(c).Scope, nil, time.Now())
	require.Error(t, err)
	_, err = model.SelectUpstreamAccount(c.Request.Context(), db, refs[1:], State(c).Scope, nil, time.Now())
	require.NoError(t, err)
}
func TestDefaultNativeDeferralRetainsHTTPFailures(t *testing.T) {
	c, db, refs := relayPoolFixture(t)
	DeferNativeOutcome(c)
	require.NoError(t, BeginSend(c))
	require.NoError(t, Observe(c, &http.Response{StatusCode: 429, Header: http.Header{"X-Should-Retry": []string{"false"}}}, nil))
	_, err := model.SelectUpstreamAccount(c.Request.Context(), db, refs[:1], State(c).Scope, nil, time.Now())
	require.Error(t, err)
	require.Error(t, CompleteNativeOutcome(c, nil))
	assert.False(t, RetryAllowed(c))
}
func TestNativeFundingRefusalUsesReceiptQuotaGroup(t *testing.T) {
	c, db, _ := relayPoolFixture(t)
	refs := []model.UpstreamAccountRef{}
	for i, key := range []string{"group-key-A", "group-key-B"} {
		ref, err := model.ResolveUpstreamAccountRef(c.Request.Context(), db, "group-pool", 15, i, key, "shared-funds")
		require.NoError(t, err)
		refs = append(refs, ref)
	}
	selected, err := model.SelectUpstreamAccount(c.Request.Context(), db, refs[:1], State(c).Scope, nil, time.Now())
	require.NoError(t, err)
	Prepare(c, db, selected, State(c).Scope)
	DeferAllStatusNativeOutcome(c)
	require.NoError(t, BeginSend(c))
	require.NoError(t, Observe(c, &http.Response{StatusCode: 200, Header: http.Header{}}, nil))
	require.NoError(t, CompleteNativeOutcome(c, &FailureInput{Refusal: RefusalQuotaExhausted, SharedQuota: true}))
	_, err = model.SelectUpstreamAccount(c.Request.Context(), db, refs[1:], State(c).Scope, nil, time.Now())
	require.Error(t, err)
}

func TestNativeSuccessClearsOnlyValidatedBackoff(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			c, _, _ := relayPoolFixture(t)
			c.Header("Retry-After", "60")
			DeferNativeOutcome(c)
			require.NoError(t, BeginSend(c))
			require.NoError(t, Observe(c, &http.Response{StatusCode: 200, Header: http.Header{}}, nil))
			require.Equal(t, "60", c.Writer.Header().Get("Retry-After"), "HTTP success alone is not semantic success")
			if success {
				require.NoError(t, CompleteNativeOutcome(c, nil))
				assert.Empty(t, c.Writer.Header().Get("Retry-After"))
			} else {
				require.NoError(t, CompleteNativeOutcome(c, &FailureInput{StatusCode: 502}))
				assert.Equal(t, "60", c.Writer.Header().Get("Retry-After"))
			}
		})
	}
}
