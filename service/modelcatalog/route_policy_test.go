package modelcatalog

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPolicyFallsBackOnlyOnRetryableFailure(t *testing.T) {
	policy := NewPolicy([]Target{{ChannelID: 1, Priority: 0, Weight: 1}, {ChannelID: 2, Priority: 1, Weight: 1}})
	next := policy.NextAfter(1, AttemptResult{Status: 429})
	require.NotNil(t, next)
	require.Equal(t, 2, next.ChannelID)

	require.Nil(t, policy.NextAfter(1, AttemptResult{Status: http.StatusBadRequest}))
	require.Nil(t, policy.NextAfter(1, AttemptResult{Status: http.StatusUnauthorized}))
	require.Nil(t, policy.NextAfter(2, AttemptResult{Status: 500}))
}

func TestRouteDecisionContainsNoSecrets(t *testing.T) {
	trace := RouteDecisionTrace{
		RequestedModel: "alias",
		PublicModel:    "deepseek/model",
		Attempts: []Attempt{{
			ChannelID: 7,
			Status:    429,
			Reason:    "rate_limited",
		}},
	}
	encoded, err := json.Marshal(trace)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "sk-")
	require.NotContains(t, string(encoded), "Authorization")
}

func TestRetryableConflictOnlyWhenExplicit(t *testing.T) {
	policy := NewPolicy([]Target{{ChannelID: 1}, {ChannelID: 2}})
	require.NotNil(t, policy.NextAfter(1, AttemptResult{Status: 409, Reason: "rate_limited"}))
	require.Nil(t, policy.NextAfter(1, AttemptResult{Status: 409, Reason: "duplicate_id"}))
}
