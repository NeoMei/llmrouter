package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func EmbeddingHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	embeddingReq, ok := info.Request.(*dto.EmbeddingRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected *dto.EmbeddingRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(embeddingReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to EmbeddingRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	space := info.ModelCapabilities.EmbeddingSpace
	if space != nil {
		if request.Dimensions != nil && *request.Dimensions != space.Dimensions {
			return types.NewErrorWithStatusCode(fmt.Errorf("dimensions differ from fixed embedding_space"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		dimensions := space.Dimensions
		request.Dimensions = &dimensions
	}
	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	if space != nil && request.Model != space.Model {
		return types.NewErrorWithStatusCode(fmt.Errorf("mapped upstream model differs from fixed embedding_space"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	convertedRequest, err := adaptor.ConvertEmbeddingRequest(c, info, *request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			return newAPIErrorFromParamOverride(err)
		}
	}

	if space != nil && len(info.ParamOverride) > 0 {
		var final struct {
			Model          string `json:"model"`
			Dimensions     *int   `json:"dimensions"`
			EncodingFormat string `json:"encoding_format"`
		}
		if err := common.Unmarshal(jsonData, &final); err != nil || final.Model != space.Model || final.Dimensions == nil || *final.Dimensions != space.Dimensions || (final.EncodingFormat != "" && final.EncodingFormat != "float") {
			return types.NewErrorWithStatusCode(fmt.Errorf("parameter override violates fixed embedding_space"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	logger.LogDebug(c, "converted embedding request body: %s", jsonData)
	body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	defer closer.Close()
	jsonData = nil
	info.UpstreamRequestBodySize = size
	info.UpstreamRequestGetBody = getBody
	var requestBody io.Reader = body
	statusCodeMappingStr := c.GetString("status_code_mapping")
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

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

	var gate *embeddingResponseGate
	if space != nil {
		if httpResp == nil || httpResp.Body == nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("missing embedding response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		raw, readErr := io.ReadAll(io.LimitReader(httpResp.Body, maxContractEmbeddingResponseBytes+1))
		httpResp.Body.Close()
		if readErr != nil || len(raw) > maxContractEmbeddingResponseBytes {
			return types.NewErrorWithStatusCode(fmt.Errorf("embedding response unreadable or exceeds size limit"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		httpResp.Body = io.NopCloser(bytes.NewReader(raw))
		gate = &embeddingResponseGate{ResponseWriter: c.Writer, header: c.Writer.Header().Clone(), status: http.StatusOK}
		c.Writer = gate
		defer func() { c.Writer = gate.ResponseWriter }()
	}
	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}
	if gate != nil {
		c.Writer = gate.ResponseWriter
		count, countErr := request.InputCount()
		if countErr != nil {
			return types.NewErrorWithStatusCode(countErr, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		normalized, err := normalizeEmbeddingResponse(gate.body.Bytes(), *space, count)
		if err != nil || gate.overflow {
			if err == nil {
				err = fmt.Errorf("embedding response exceeds size limit")
			}
			return types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		for key, values := range gate.header {
			c.Writer.Header()[key] = values
		}
		// This is an uncompressed validated JSON representation. Upstream
		// encodings and representation validators no longer describe its bytes.
		for _, name := range []string{"Content-Encoding", "ETag", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest", "Last-Modified"} {
			c.Writer.Header().Del(name)
		}
		c.Writer.Header().Set("Content-Length", strconv.Itoa(len(normalized)))
		c.Writer.WriteHeader(gate.status)
		if _, err := c.Writer.Write(normalized); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
	}
	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), nil)
	return nil
}
