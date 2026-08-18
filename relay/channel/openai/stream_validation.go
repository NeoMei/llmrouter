package openai

import (
	"fmt"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func abortInvalidStreamTransport(c *gin.Context, info *relaycommon.RelayInfo, validator *toolpolicy.StreamValidator) *types.NewAPIError {
	if info != nil && info.StreamStatus != nil &&
		info.StreamStatus.EndReason == relaycommon.StreamEndReasonDone &&
		!info.StreamStatus.HasErrors() && c != nil && c.Request != nil && c.Request.Context().Err() == nil {
		return nil
	}
	reason := "stream transport did not end with [DONE]"
	if info != nil && info.StreamStatus != nil {
		reason = info.StreamStatus.Summary()
	}
	return toolpolicy.NewResponseValidationError(validator.Abort(fmt.Errorf("%s", reason)))
}
