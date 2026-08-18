package relay

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/gateway"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// finalizeOutboundToolPolicy applies channel overrides and captures the policy
// from the exact final semantic body before a request body or network request is
// created. Callers must run disabled-field filtering first when applicable.
func finalizeOutboundToolPolicy(c *gin.Context, info *relaycommon.RelayInfo, convertedRequest any, jsonData []byte) ([]byte, *types.NewAPIError) {
	toolpolicy.Clear(c)
	var err error
	if info != nil && len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			return nil, newAPIErrorFromParamOverride(err)
		}
	}
	if err := captureOutboundToolPolicy(c, info, convertedRequest, jsonData); err != nil {
		if info != nil && len(info.ParamOverride) > 0 {
			return nil, newAPIErrorFromParamOverride(err)
		}
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	jsonData, repairErr := repairFinalGatewayChat(c, info, convertedRequest, jsonData)
	if repairErr != nil {
		return nil, repairErr
	}
	return jsonData, nil
}

func repairFinalGatewayChat(c *gin.Context, info *relaycommon.RelayInfo, convertedRequest any, jsonData []byte) ([]byte, *types.NewAPIError) {
	if !isFinalGatewayChat(info, convertedRequest) {
		return jsonData, nil
	}
	repaired, err := gateway.RepairFinalChatRequest(c, jsonData)
	if err != nil {
		toolpolicy.Clear(c)
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return repaired, nil
}

func isFinalGatewayChat(info *relaycommon.RelayInfo, convertedRequest any) bool {
	if info == nil || info.ApiType != constant.APITypeGateway {
		return false
	}
	format, ok := relaycommon.GuessRelayFormatFromRequest(convertedRequest)
	if !ok {
		format = info.GetFinalRequestRelayFormat()
	}
	return format == types.RelayFormatOpenAI
}

func captureOutboundToolPolicy(c *gin.Context, info *relaycommon.RelayInfo, convertedRequest any, jsonData []byte) error {
	format, ok := relaycommon.GuessRelayFormatFromRequest(convertedRequest)
	if !ok && info != nil {
		format = info.GetFinalRequestRelayFormat()
	}
	if format == "" {
		toolpolicy.Clear(c)
		return fmt.Errorf("cannot determine final relay format for tool policy")
	}
	policy, err := toolpolicy.BuildFromOutboundJSON(format, jsonData)
	if err != nil {
		toolpolicy.Clear(c)
		return err
	}
	toolpolicy.Set(c, policy)
	return nil
}
