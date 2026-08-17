package types

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContextCapacityExceededRemainsMachineVisibleInClaudeError(t *testing.T) {
	apiErr := NewErrorWithStatusCode(errors.New("request context is too large"), ErrorCodeContextCapacityExceeded, http.StatusBadRequest, ErrOptionWithSkipRetry())

	claudeErr := apiErr.ToClaudeError()

	assert.Equal(t, string(ErrorCodeContextCapacityExceeded), claudeErr.Type)
	assert.Equal(t, "request context is too large", claudeErr.Message)
}
