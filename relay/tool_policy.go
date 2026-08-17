package relay

import (
	"fmt"

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
	return jsonData, nil
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
