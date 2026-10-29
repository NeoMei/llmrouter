package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func RerankHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	rerankReq, ok := info.Request.(*dto.RerankRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected dto.RerankRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(rerankReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to ImageRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	var requestBody io.Reader
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		if common.GetContextKeyBool(c, constant.ContextKeyAliNativeRerank) {
			return types.NewErrorWithStatusCode(fmt.Errorf("native rerank compatibility requires adapter conversion; disable request body passthrough"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		info.UpstreamRequestBodySize = storage.Size()
		info.UpstreamRequestGetBody = storage.NewReader
		requestBody = common.ReaderOnly(storage)
	} else {
		convertedRequest, err := adaptor.ConvertRerankRequest(c, info.RelayMode, *request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		// An adapter may legitimately return nil with no error to mean
		// "pass through the original request unchanged". Fall back so we
		// never marshal a literal JSON null upstream.
		if convertedRequest == nil {
			convertedRequest = request
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// apply param override
		if len(info.ParamOverride) > 0 {
			jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
			if err != nil {
				return newAPIErrorFromParamOverride(err)
			}
		}

		if !info.PlatformMode {
			logger.LogDebug(c, "Rerank request body: %s", jsonData)
		}
		var body io.Reader
		var size int64
		var getBody func() (io.ReadCloser, error)
		var closer io.Closer
		if info.PlatformMode && info.PlatformBilling != nil && info.PlatformBilling.AliRerank() {
			body = bytes.NewReader(jsonData)
			size = int64(len(jsonData))
		} else {
			body, size, getBody, closer, err = relaycommon.NewOutboundJSONBody(jsonData)
		}
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		if closer != nil {
			defer closer.Close()
		}
		jsonData = nil
		info.UpstreamRequestBodySize = size
		info.UpstreamRequestGetBody = getBody
		requestBody = body
	}

	if info.PlatformMode {
		if info.PlatformBilling == nil || info.NativeBilling == nil {
			return types.NewError(fmt.Errorf("platform metering context required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		if err := info.PlatformBilling.Dispatch(c.Request.Context(), info.ChannelId); err != nil {
			return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
	}
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		if httpResp.StatusCode != http.StatusOK {
			newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return newAPIError
		}
	}
	if info.PlatformMode && info.PlatformBilling != nil && info.PlatformBilling.AliRerank() {
		if httpResp == nil || httpResp.Body == nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("missing rerank response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxNativeRerankResponseBytes+1))
		httpResp.Body.Close()
		if err != nil || len(raw) > maxNativeRerankResponseBytes {
			return types.NewErrorWithStatusCode(fmt.Errorf("rerank response unreadable or exceeds size limit"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		result, err := info.PlatformBilling.CaptureKnowledgeResult(c.Request.Context(), info.ChannelId, info.ResolvedModelNameOrOrigin(), info.ApiKey, raw)
		if err != nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("rerank result pending verification or settlement"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		c.Header("Content-Type", "application/json")
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "application/json", result)
		return nil
	}

	var nativeWriter *nativeRerankResponseWriter
	originalWriter := c.Writer
	if common.GetContextKeyBool(c, constant.ContextKeyAliNativeRerank) {
		if httpResp == nil || httpResp.Body == nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("missing native rerank response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		// Bound the upstream read too: adapters may buffer before their writes.
		raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxNativeRerankResponseBytes+1))
		service.CloseResponseBodyGracefully(httpResp)
		if err != nil || len(raw) > maxNativeRerankResponseBytes {
			return types.NewErrorWithStatusCode(fmt.Errorf("native rerank upstream body invalid or too large"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		var wire struct {
			Results []json.RawMessage `json:"results"`
			Output  *struct {
				Results []json.RawMessage `json:"results"`
			} `json:"output"`
		}
		if err := common.Unmarshal(raw, &wire); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		results := wire.Results
		if wire.Output != nil {
			results = wire.Output.Results
		}
		count := len(request.Documents)
		if request.TopN != nil {
			count = *request.TopN
		}
		if err := dto.ValidateRerankResults(results, len(request.Documents), count); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		httpResp.Body = io.NopCloser(bytes.NewReader(raw))
		nativeWriter = &nativeRerankResponseWriter{ResponseWriter: originalWriter, header: originalWriter.Header().Clone(), status: http.StatusOK}
		c.Writer = nativeWriter
	}
	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	c.Writer = originalWriter
	if newAPIError != nil {
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}
	if nativeWriter != nil {
		normalizedUsage, ok := usage.(*dto.Usage)
		if !ok || normalizedUsage == nil || normalizedUsage.TotalTokens < 0 || normalizedUsage.PromptTokens < 0 || normalizedUsage.CompletionTokens < 0 {
			return types.NewErrorWithStatusCode(fmt.Errorf("invalid native rerank usage"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		if nativeWriter.overflow {
			return types.NewErrorWithStatusCode(fmt.Errorf("native rerank response too large"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		wrapped, err := nativeRerankResponse(nativeWriter.body.Bytes(), request)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		for key, values := range nativeWriter.header {
			originalWriter.Header()[key] = values
		}
		for _, key := range []string{"Content-Encoding", "ETag", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest", "Last-Modified"} {
			originalWriter.Header().Del(key)
		}
		originalWriter.Header().Set("Content-Type", "application/json")
		originalWriter.Header().Set("Content-Length", strconv.Itoa(len(wrapped)))
		originalWriter.WriteHeader(nativeWriter.status)
		_, _ = originalWriter.Write(wrapped)
	}
	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), nil)
	return nil
}
