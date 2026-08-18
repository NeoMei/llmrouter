package common

import (
	appcommon "github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/gin-gonic/gin"
)

// PersistResponsesTurn is the provider-neutral completed-turn persistence
// boundary. Providers call it only after protocol and tool-policy validation.
func PersistResponsesTurn(c *gin.Context, info *RelayInfo, response *dto.OpenAIResponsesResponse) {
	if c == nil || c.Request == nil || c.Request.Context().Err() != nil || !responsesstate.Enabled() || info == nil || !responsesstate.EligibleCompletedResponse(response) {
		return
	}
	store, err := responsesstate.Default()
	if err != nil {
		return
	}
	raw, exists := c.Get(string(appconstant.ContextKeyResponsesContinuity))
	if !exists {
		return
	}
	continuity, ok := raw.(responsesstate.ContinuityContext)
	if !ok || len(continuity.CurrentInput) == 0 {
		return
	}
	output, err := appcommon.Marshal(response.Output)
	if err != nil {
		return
	}
	if err := responsesstate.SaveTurn(c.Request.Context(), store, responsesstate.Record{
		Version: responsesstate.CurrentVersion, Scope: continuity.Scope, ResponseID: response.ID,
		ParentResponseID: continuity.ParentResponseID, Input: continuity.CurrentInput, Output: output,
	}); err != nil {
		appcommon.SysLog("responses state save skipped: " + err.Error())
	}
}
