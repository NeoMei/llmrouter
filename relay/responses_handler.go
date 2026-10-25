package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/responsesprobe"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func ResponsesHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	toolpolicy.Clear(c)
	if err := helper.ValidateUniqueResponsesBody(c); err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	info.InitChannelMeta(c)
	// Automatic responses negotiation runs before endpoint guards so chat-only
	// upstreams can serve /v1/responses (and /v1/responses/compact) via
	// conversion. Pass-through bodies keep native behavior.
	passthroughBody := model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled
	if !passthroughBody {
		responsesprobe.DecideResponsesViaChat(c.Request.Context(), info)
	}
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		switch info.ApiType {
		case appconstant.APITypeOpenAI, appconstant.APITypeCodex:
		default:
			if !info.ResponsesViaChat {
				return types.NewErrorWithStatusCode(
					fmt.Errorf("unsupported endpoint %q for api type %d", "/v1/responses/compact", info.ApiType),
					types.ErrorCodeInvalidRequest,
					http.StatusBadRequest,
					types.ErrOptionWithSkipRetry(),
				)
			}
		}
	}

	var responsesReq *dto.OpenAIResponsesRequest
	switch req := info.Request.(type) {
	case *dto.OpenAIResponsesRequest:
		responsesReq = req
	case *dto.OpenAIResponsesCompactionRequest:
		// Only fields documented for POST /v1/responses/compact are forwarded:
		// model, input, instructions, previous_response_id, prompt_cache_key,
		// prompt_cache_options, prompt_cache_retention, service_tier.
		// Undocumented Codex-parity fields (tools, reasoning, text) are parsed
		// for client compatibility but intentionally not sent upstream.
		responsesReq = &dto.OpenAIResponsesRequest{
			Model:                req.Model,
			Input:                req.Input,
			Instructions:         req.Instructions,
			PreviousResponseID:   req.PreviousResponseID,
			ParallelToolCalls:    req.ParallelToolCalls,
			ServiceTier:          req.ServiceTier,
			PromptCacheKey:       req.PromptCacheKey,
			PromptCacheOptions:   req.PromptCacheOptions,
			PromptCacheRetention: req.PromptCacheRetention,
		}
	default:
		return types.NewErrorWithStatusCode(
			fmt.Errorf("invalid request type, expected dto.OpenAIResponsesRequest or dto.OpenAIResponsesCompactionRequest, got %T", info.Request),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	request, err := common.DeepCopy(responsesReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to GeneralOpenAIRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if info.RelayMode == relayconstant.RelayModeResponses {
		normalizeResponsesStore(request)
	}

	if responsesstate.Enabled() {
		if rawPrepared, exists := c.Get(string(appconstant.ContextKeyResponsesPreparedRequest)); exists {
			prepared, ok := rawPrepared.(*dto.OpenAIResponsesRequest)
			if !ok || prepared == nil {
				return responsesContinuityAPIError(errors.New("responses continuity prepared request is invalid"))
			}
			request, err = common.DeepCopy(prepared)
			if err != nil {
				return responsesContinuityAPIError(fmt.Errorf("failed to copy prepared responses request: %w", err))
			}
		} else {
			store, storeErr := responsesstate.Default()
			if storeErr != nil {
				return responsesContinuityAPIError(fmt.Errorf("responses state store unavailable: %w", storeErr))
			}
			prepared, continuityErr := prepareResponsesContinuity(c, info, store, request)
			if continuityErr != nil {
				return responsesContinuityAPIError(continuityErr)
			}
			request = prepared
		}
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	requestPolicy, err := toolpolicy.BuildFromResponses(request.Tools, request.ToolChoice)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact && info.ResponsesViaChat {
		usage, compactErr := responsesCompactViaChat(c, info, adaptor, request)
		if compactErr != nil {
			return compactErr
		}
		usageDto := usage
		originModelName := info.OriginModelName
		originPriceData := info.PriceData
		if _, err := helper.ModelPriceHelper(c, info, info.GetEstimatePromptTokens(), &types.TokenCountMeta{}); err != nil {
			info.OriginModelName = originModelName
			info.PriceData = originPriceData
			return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithSkipRetry(), types.ErrOptionWithStatusCode(http.StatusBadRequest))
		}
		service.PostTextConsumeQuota(c, info, usageDto, nil)
		info.OriginModelName = originModelName
		info.PriceData = originPriceData
		return nil
	}
	var requestBody io.Reader
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		toolpolicy.Set(c, requestPolicy)
		body, size, getBody, closer, err := newResponsesPassthroughBody(c, request, info.RelayMode == relayconstant.RelayModeResponses, info.ModelRouteId > 0)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		info.UpstreamRequestBodySize = size
		info.UpstreamRequestGetBody = getBody
		requestBody = body
	} else {
		convertedRequest, err := adaptor.ConvertOpenAIResponsesRequest(c, info, *request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// remove disabled fields for OpenAI Responses API
		jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		jsonData, policyErr := finalizeOutboundToolPolicy(c, info, convertedRequest, jsonData)
		if policyErr != nil {
			return policyErr
		}

		logger.LogDebug(c, "requestBody: %s", jsonData)
		body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		jsonData = nil
		info.UpstreamRequestBodySize = size
		info.UpstreamRequestGetBody = getBody
		requestBody = body
	}

	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	if resp != nil {
		httpResp = resp.(*http.Response)

		if httpResp.StatusCode != http.StatusOK {
			newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return newAPIError
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	usageDto := usage.(*dto.Usage)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		originModelName := info.OriginModelName
		originPriceData := info.PriceData

		_, err := helper.ModelPriceHelper(c, info, info.GetEstimatePromptTokens(), &types.TokenCountMeta{})
		if err != nil {
			info.OriginModelName = originModelName
			info.PriceData = originPriceData
			return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithSkipRetry(), types.ErrOptionWithStatusCode(http.StatusBadRequest))
		}
		service.PostTextConsumeQuota(c, info, usageDto, nil)

		info.OriginModelName = originModelName
		info.PriceData = originPriceData
		return nil
	}

	if strings.HasPrefix(info.OriginModelName, "gpt-4o-audio") || (info.ChannelType == appconstant.ChannelTypeAli && info.UpstreamModelName == "qwen3.8-omni-flash" && info.ActualUsageValidated) {
		service.PostAudioConsumeQuota(c, info, usageDto, "")
	} else {
		service.PostTextConsumeQuota(c, info, usageDto, nil)
	}
	return nil
}

func normalizeResponsesStore(request *dto.OpenAIResponsesRequest) {
	if request != nil && len(request.Store) == 0 {
		request.Store = json.RawMessage("false")
	}
}

func newResponsesPassthroughBody(c *gin.Context, request *dto.OpenAIResponsesRequest, normalizeStore bool, resolvedRoute bool) (
	body io.Reader,
	size int64,
	getBody func() (io.ReadCloser, error),
	closer io.Closer,
	err error,
) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	reader, err := storage.NewReader()
	if err != nil {
		return nil, 0, nil, nil, err
	}
	rawRequest, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, 0, nil, nil, readErr
	}
	if closeErr != nil {
		return nil, 0, nil, nil, closeErr
	}
	jsonData, err := patchResponsesPassthroughBody(rawRequest, request, normalizeStore, resolvedRoute)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	return relaycommon.NewOutboundJSONBody(jsonData)
}

// patchResponsesPassthroughBody keeps pass-through extensions byte-precise at
// the JSON value level while applying only router-owned Responses mutations.
func patchResponsesPassthroughBody(rawRequest []byte, request *dto.OpenAIResponsesRequest, normalizeStore bool, resolvedRoute bool) ([]byte, error) {
	if request == nil {
		return nil, errors.New("prepared responses request is nil")
	}
	var payload map[string]json.RawMessage
	if err := common.Unmarshal(rawRequest, &payload); err != nil {
		return nil, fmt.Errorf("invalid responses pass-through request: %w", err)
	}
	if resolvedRoute {
		// The raw client alias cannot override the catalog's selected and mapped
		// upstream identity. Preserve every unrelated raw extension value.
		model, err := common.Marshal(request.Model)
		if err != nil {
			return nil, err
		}
		payload["model"] = model
	}

	if normalizeStore {
		if _, exists := payload["store"]; !exists {
			payload["store"] = json.RawMessage("false")
		}
	}
	if rawPrevious, exists := payload["previous_response_id"]; exists && request.PreviousResponseID == "" {
		var previousID string
		if err := common.Unmarshal(rawPrevious, &previousID); err != nil {
			return nil, fmt.Errorf("invalid previous_response_id in pass-through request: %w", err)
		}
		if previousID != "" {
			payload["input"] = append(json.RawMessage(nil), request.Input...)
			delete(payload, "previous_response_id")
		}
	}
	patched, err := common.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode responses pass-through request: %w", err)
	}
	return patched, nil
}

func responsesContinuityAPIError(err error) *types.NewAPIError {
	if errors.Is(err, responsesstate.ErrInvalidClientTaskID) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	status := http.StatusInternalServerError
	if errors.Is(err, responsesstate.ErrLegacyScope) ||
		errors.Is(err, responsesstate.ErrScopeMismatch) ||
		errors.Is(err, responsesstate.ErrNotFound) ||
		errors.Is(err, responsesstate.ErrReplayRejected) {
		status = http.StatusConflict
	}
	return types.NewErrorWithStatusCode(err, types.ErrorCodeResponsesStateScopeInvalid, status, types.ErrOptionWithSkipRetry())
}

// PrepareResponsesContinuityForAttempt validates and materializes Responses
// continuity before billing mutates quota or an upstream request is sent.
// ResponsesHelper consumes the prepared request for the same route attempt.
func PrepareResponsesContinuityForAttempt(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if c != nil {
		c.Set(string(appconstant.ContextKeyResponsesPreparedRequest), nil)
	}
	if info == nil ||
		(info.RelayMode != relayconstant.RelayModeResponses && info.RelayMode != relayconstant.RelayModeResponsesCompact) {
		return nil
	}
	if err := helper.ValidateUniqueResponsesBody(c); err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if !responsesstate.Enabled() {
		return nil
	}
	var request *dto.OpenAIResponsesRequest
	switch req := info.Request.(type) {
	case *dto.OpenAIResponsesRequest:
		var err error
		request, err = common.DeepCopy(req)
		if err != nil {
			return responsesContinuityAPIError(fmt.Errorf("failed to copy responses request: %w", err))
		}
	case *dto.OpenAIResponsesCompactionRequest:
		request = &dto.OpenAIResponsesRequest{
			Model: req.Model, Input: req.Input, Instructions: req.Instructions,
			PreviousResponseID: req.PreviousResponseID, ParallelToolCalls: req.ParallelToolCalls,
			ServiceTier: req.ServiceTier, PromptCacheKey: req.PromptCacheKey,
			PromptCacheOptions: req.PromptCacheOptions, PromptCacheRetention: req.PromptCacheRetention,
		}
	default:
		return types.NewErrorWithStatusCode(
			fmt.Errorf("invalid responses request type %T", info.Request),
			types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry(),
		)
	}
	if info.RelayMode == relayconstant.RelayModeResponses {
		normalizeResponsesStore(request)
	}
	store, err := responsesstate.Default()
	if err != nil {
		return responsesContinuityAPIError(fmt.Errorf("responses state store unavailable: %w", err))
	}
	prepared, err := prepareResponsesContinuity(c, info, store, request)
	if err != nil {
		return responsesContinuityAPIError(err)
	}
	c.Set(string(appconstant.ContextKeyResponsesPreparedRequest), prepared)
	return nil
}

// prepareResponsesContinuity materializes a parent chain only for non-native
// Responses wires and carries the original current turn to response handling.
func prepareResponsesContinuity(c *gin.Context, info *relaycommon.RelayInfo, store responsesstate.Store, request *dto.OpenAIResponsesRequest) (*dto.OpenAIResponsesRequest, error) {
	if c != nil {
		// RelayInfo and Gin context survive ordered candidate retries. Clear the
		// prior attempt before any validation or store access so an error cannot
		// make response persistence reuse stale scope or input.
		c.Set(string(appconstant.ContextKeyResponsesContinuity), nil)
	}
	if c == nil || info == nil || request == nil {
		return nil, errors.New("responses continuity context is incomplete")
	}
	clientTaskID, err := responsesstate.ParseClientTaskID(request.ClientMetadata)
	if err != nil {
		return nil, err
	}
	expectedScope := responsesstate.Scope{
		TenantID:     info.UserId,
		UserID:       info.UserId,
		TokenID:      info.TokenId,
		RouteID:      info.ModelRouteId,
		TargetID:     info.ModelTargetId,
		ChannelID:    info.ResolvedChannelId,
		ClientTaskID: clientTaskID,
		Model:        info.ResolvedModelNameOrOrigin(),
	}
	if expectedScope.RouteID == 0 && expectedScope.TargetID == 0 && expectedScope.ChannelID > 0 {
		expectedScope.RouteKind = "legacy_distributor"
		expectedScope.RouteID = expectedScope.ChannelID
		expectedScope.TargetID = expectedScope.ChannelID
	}
	continuity := responsesstate.ContinuityContext{
		Scope:            expectedScope,
		ParentResponseID: request.PreviousResponseID,
		CurrentInput:     append(json.RawMessage(nil), request.Input...),
	}
	prepared := request
	if request.PreviousResponseID != "" {
		encoded, err := common.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal responses request for state expansion: %w", err)
		}
		expansion, err := responsesstate.Expand(c.Request.Context(), store, expectedScope, encoded)
		if err != nil {
			return nil, err
		}
		var expandedRequest dto.OpenAIResponsesRequest
		if err := common.Unmarshal(expansion.Request, &expandedRequest); err != nil {
			return nil, fmt.Errorf("responses state expansion decode failed: %w", err)
		}
		prepared = &expandedRequest
		continuity = responsesstate.ContinuityContext{
			Scope:            expansion.Scope,
			ParentResponseID: expansion.ParentResponseID,
			CurrentInput:     append(json.RawMessage(nil), expansion.CurrentInput...),
		}
	}
	if len(continuity.CurrentInput) > 0 {
		c.Set(string(appconstant.ContextKeyResponsesContinuity), continuity)
	}
	return prepared, nil
}
