package relay

import (
	"fmt"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/url"
	"strings"
)

// platformGeneration owns the four native managed generation paths. It retains
// the admitted JSON through real adapter authentication/HTTP/raw usage handling,
// without native convenience transformations or estimated-usage settlement.
func platformGeneration(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	toolpolicy.Clear(c)
	info.InitChannelMeta(c)
	session := info.PlatformBilling
	format, proven := platformbilling.TokenProducer(session.WireOperation().ID, info.ChannelType)
	if (format == types.RelayFormatClaude && (len(model_setting.GetClaudeSettings().HeadersSettings[info.OriginModelName]) > 0 || info.ChannelOtherSettings.ClaudeBetaQuery)) || !proven || format != info.GetFinalRequestRelayFormat() || len(info.ParamOverride) > 0 || len(info.HeadersOverride) > 0 || info.ChannelSetting.SystemPrompt != "" || info.ChannelSetting.PassThroughBodyEnabled || model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ResponsesViaChat || service.ShouldChatCompletionsUseResponsesGlobal(info.ChannelId, info.ChannelType, info.OriginModelName) {
		return types.NewError(fmt.Errorf("managed native producer or unchanged wire unavailable"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ModelMappedHelper(c, info, nil); err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	raw, err := session.GenerationBody(info.ChannelType, format, info.UpstreamModelName)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	policy, err := toolpolicy.BuildFromOutboundJSON(format, raw)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	toolpolicy.Set(c, policy)
	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("managed adaptor unavailable"), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	if session.WireOperation().ID == "responses.compact" {
		target, e := adaptor.GetRequestURL(info)
		parsed, parseErr := url.Parse(target)
		if e != nil || parseErr != nil || parsed.Path != "/v1/responses/compact" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return types.NewError(fmt.Errorf("certified compact endpoint unavailable"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
	}
	body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(raw)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	defer closer.Close()
	info.UpstreamRequestBodySize, info.UpstreamRequestGetBody = size, getBody
	// Dispatch durably writes UNKNOWN + DISPATCHED before DoRequest can see bytes.
	if !session.NativeHistoryEnabled() {
		if err = session.Dispatch(c.Request.Context(), info.ChannelId); err != nil {
			return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
	}
	resp, err := adaptor.DoRequest(c, info, body)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	httpResp, ok := resp.(*http.Response)
	if !ok || httpResp == nil {
		return types.NewError(fmt.Errorf("missing managed provider response"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
	if httpResp.StatusCode != http.StatusOK {
		return service.RelayErrorHandler(c.Request.Context(), httpResp, false)
	}
	// The adapter's NativeTokenObserver owns evidence; dto.Usage is never funding.
	_, apiErr := adaptor.DoResponse(c, httpResp, info)
	return apiErr
}
