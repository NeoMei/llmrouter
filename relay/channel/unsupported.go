package channel

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/types"
)

// ErrUnsupportedOperation is returned by adapter methods for operations that the
// provider does not implement. It is surfaced as an OpenAI-compatible
// unsupported_operation error and is never retried.
var ErrUnsupportedOperation = types.NewErrorWithStatusCode(
	errors.New("unsupported operation"),
	types.ErrorCodeUnsupportedOperation,
	http.StatusNotImplemented,
	types.ErrOptionWithSkipRetry(),
)
