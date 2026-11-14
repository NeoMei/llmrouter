package relay

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// DeepSeekToolsHelper relays the standalone DeepSeek Search and Fetch APIs.
// The original JSON is forwarded byte-for-byte. A call is charged only after a
// successful response that actually contains at least one result/page.
func DeepSeekToolsHelper(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	info.InitChannelMeta(c)
	if info.ChannelType != constant.ChannelTypeDeepSeek {
		return types.NewError(fmt.Errorf("channel does not support %s", c.Request.URL.Path), types.ErrorCodeInvalidRequest)
	}
	request, ok := info.Request.(*dto.DeepSeekToolsRequest)
	if !ok || request == nil {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected *dto.DeepSeekToolsRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	suffix, isToolPath := strings.CutPrefix(c.Request.URL.Path, "/v1/tools/")
	if !isToolPath || (suffix == "search") != (info.RelayMode == relayconstant.RelayModeToolsSearch) || (suffix == "fetch") != (info.RelayMode == relayconstant.RelayModeToolsFetch) {
		return types.NewErrorWithStatusCode(errors.New("search/fetch path and relay mode disagree"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ModelMappedHelper(c, info, request); err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(request.RawBody)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	defer closer.Close()
	info.UpstreamRequestBodySize = size
	info.UpstreamRequestGetBody = getBody

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	resp, err := adaptor.DoRequest(c, info, body)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	httpResp, ok := resp.(*http.Response)
	if !ok || httpResp == nil {
		return types.NewOpenAIError(errors.New("invalid http response"), types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(httpResp)
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		apiErr := service.RelayErrorHandler(c.Request.Context(), httpResp, false)
		service.ResetStatusCode(apiErr, c.GetString("status_code_mapping"))
		return apiErr
	}

	responseBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 16<<20+1))
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	}
	if len(responseBody) > 16<<20 {
		return types.NewErrorWithStatusCode(errors.New("search response exceeds completion bound"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	service.IOCopyBytesGracefully(c, httpResp, responseBody)

	billable := false
	switch info.RelayMode {
	case relayconstant.RelayModeToolsFetch:
		billable = gjson.GetBytes(responseBody, "content").String() != ""
	case relayconstant.RelayModeToolsSearch:
		results := gjson.GetBytes(responseBody, "results")
		billable = results.IsArray() && len(results.Array()) > 0
	}
	if billable {
		service.PostTextConsumeQuota(c, info, &dto.Usage{TotalTokens: 0}, nil)
		return nil
	}
	if info.Billing != nil {
		info.Billing.Refund(c)
	}
	return nil
}
