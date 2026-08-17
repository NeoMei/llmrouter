package types

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextCapacityExceededRemainsMachineVisibleInClaudeError(t *testing.T) {
	apiErr := NewErrorWithStatusCode(errors.New("request context is too large"), ErrorCodeContextCapacityExceeded, http.StatusBadRequest, ErrOptionWithSkipRetry())

	claudeErr := apiErr.ToClaudeError()

	assert.Equal(t, string(ErrorCodeContextCapacityExceeded), claudeErr.Type)
	assert.Equal(t, "request context is too large", claudeErr.Message)
}

func TestResponsesStateScopeInvalidRemainsMachineVisibleInClaudeError(t *testing.T) {
	apiErr := NewErrorWithStatusCode(errors.New("responses continuity scope mismatch"), ErrorCodeResponsesStateScopeInvalid, http.StatusConflict, ErrOptionWithSkipRetry())

	claudeErr := apiErr.ToClaudeError()

	assert.Equal(t, string(ErrorCodeResponsesStateScopeInvalid), claudeErr.Type)
	envelope, err := common.Marshal(struct {
		Type  string      `json:"type"`
		Error ClaudeError `json:"error"`
	}{Type: "error", Error: claudeErr})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"error","error":{"type":"responses_state_scope_invalid","message":"responses continuity scope mismatch"}}`, string(envelope))
}
