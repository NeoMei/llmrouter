package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/capability"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	relayprotocol "github.com/QuantumNous/new-api/relay/protocol"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits, relayconstant.RelayModeImagesVariations:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeMultimodalEmbedding:
		err = relay.MultimodalEmbeddingHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func relaySelectedChannel(c *gin.Context, relayInfo *relaycommon.RelayInfo, channel *model.Channel, relayFormat types.RelayFormat, ws *websocket.Conn) (*types.NewAPIError, bool) {
	addUsedChannel(c, channel.Id)
	if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
		return billingErr, false
	}

	bodyStorage, bodyErr := common.GetBodyStorage(c)
	if bodyErr != nil {
		// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
		if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
			return types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry()), false
		}
		return types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry()), false
	}
	c.Request.Body = io.NopCloser(bodyStorage)
	if relayInfo.NativeBilling != nil && !relayInfo.PlatformMode {
		if err := service.MarkNativeBillingDispatched(c.Request.Context(), relayInfo); err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry()), false
		}
	}

	switch relayFormat {
	case types.RelayFormatOpenAIRealtime:
		return relay.WssHelper(c, relayInfo), true
	case types.RelayFormatClaude:
		return relay.ClaudeHelper(c, relayInfo), true
	case types.RelayFormatGemini:
		return geminiRelayHandler(c, relayInfo), true
	default:
		return relayHandler(c, relayInfo), true
	}
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {
	defer func() {
		if cleanup, ok := c.Get("internal.multimodal_cleanup"); ok {
			cleanup.(func())()
		}
	}()

	requestId := c.GetString(common.RequestIdKey)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
	)

	if relayFormat == types.RelayFormatOpenAIRealtime {
		var err error
		ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			helper.WssError(c, ws, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry()).ToOpenAIError())
			return
		}
		defer ws.Close()
	}

	defer func() {
		if newAPIError != nil {
			common.SetContextKey(c, constant.ContextKeyRelayFailed, true)
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			if c.Writer.Written() && c.GetString("binary_stream_content_type") != "" {
				c.Abort()
				return
			}
			if relayFormat != types.RelayFormatOpenAIRealtime && c.Writer.Written() && strings.HasPrefix(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
				writeRelayStreamError(c, relayFormat, newAPIError)
				return
			}
			if relayFormat != types.RelayFormatOpenAIRealtime {
				c.Header("Content-Type", "application/json; charset=utf-8")
			}
			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				helper.WssError(c, ws, newAPIError.ToOpenAIError())
			case types.RelayFormatClaude:
				c.JSON(newAPIError.StatusCode, gin.H{
					"type":  "error",
					"error": newAPIError.ToClaudeError(),
				})
			default:
				c.JSON(newAPIError.StatusCode, gin.H{
					"error": newAPIError.ToOpenAIError(),
				})
			}
		}
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest)
		}
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}

	generalSettings := operation_setting.GetGeneralSetting()
	if relayFormat == types.RelayFormatClaude && relayInfo.IsStream && generalSettings.PingIntervalEnabled {
		stopKeepAlive := helper.StartStreamKeepAlive(c, time.Duration(generalSettings.PingIntervalSeconds)*time.Second)
		defer stopKeepAlive()
	}

	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	needCountToken := constant.CountToken
	// Avoid building huge CombineText (strings.Join) when token counting and sensitive check are both disabled.
	var meta *types.TokenCountMeta
	if needSensitiveCheck || needCountToken {
		meta = request.GetTokenCountMeta()
	} else {
		meta = fastTokenCountMetaForPricing(request)
	}

	if needSensitiveCheck && meta != nil {
		contains, words := service.CheckSensitiveText(meta.CombineText)
		if contains {
			logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))
			newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)
			return
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
		return
	}

	relayInfo.SetEstimatePromptTokens(tokens)

	// Resolve public client model routes before price lookup so billing and
	// channel selection use the same authoritative target identity.
	routeErr := resolveClientModelRoute(c, relayInfo, tokens)
	recordRouteAdmissionDecision(GetRequestId(c), relayInfo)
	if routeErr != nil {
		newAPIError = routeErr
		return
	}

	if relayFormat == types.RelayFormatMultimodalEmbedding && len(relayInfo.RouteCandidates) == 0 {
		newAPIError = types.NewErrorWithStatusCode(errors.New("multimodal embeddings require an explicit public route"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		return
	}
	billingLifecycleStarted := false
	defer func() {
		// Only return quota if downstream failed and quota was actually pre-consumed
		if newAPIError != nil && billingLifecycleStarted && !relayInfo.PlatformMode {
			if relayInfo.NativeBilling != nil {
				// The durable dispatch flag refuses release once output is uncertain.
				_, _ = service.FinishNativeBilling(context.WithoutCancel(c.Request.Context()), relayInfo.NativeBilling.CallID, "pre_dispatch_failure")
				return
			}
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil {
				relayInfo.Billing.Refund(c)
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
		}
	}()

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.ResolvedModelNameOrOrigin(),
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}
	accountpool.State(c).MaxSends = common.RetryTimes + 1
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil
	preparedCandidateIndex := -1
	legacyPricingPrepared := false
	legacyContinuityChannelID := 0

	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		attemptStart := time.Now()
		relayInfo.RetryIndex = retryParam.GetRetry()
		if len(relayInfo.RouteCandidates) > 0 {
			if relayInfo.RouteCandidateIndex != preparedCandidateIndex {
				if candidateErr := prepareRouteCandidateForAttempt(c, relayInfo, relayInfo.RouteCandidateIndex, tokens, meta); candidateErr != nil {
					newAPIError = candidateErr
					var unavailable *model.UpstreamAccountUnavailable
					if errors.As(candidateErr.Err, &unavailable) && advanceRouteCandidate(relayInfo) {
						retryParam.ResetRetryNextTry()
						continue
					}
					if candidateErr.GetErrorCode() == types.ErrorCodeGetChannelFailed &&
						relayInfo.RouteCandidateIndex >= 0 && relayInfo.RouteCandidateIndex < len(relayInfo.RouteCandidates) {
						candidate := relayInfo.RouteCandidates[relayInfo.RouteCandidateIndex]
						AppendRouteAttempt(GetRequestId(c), modelcatalog.Attempt{
							RouteID: candidate.RouteID, TargetID: candidate.TargetID, ChannelID: candidate.ChannelID,
							Wire: candidate.SelectedWire, Status: candidateErr.StatusCode, Reason: string(candidateErr.GetErrorCode()), RejectedReason: "channel_unavailable",
							DurationMs: time.Since(attemptStart).Milliseconds(),
						})
						if shouldRetry(c, candidateErr, common.RetryTimes-retryParam.GetRetry()) && advanceRouteCandidate(relayInfo) {
							continue
						}
					}
					break
				}
				billingLifecycleStarted = true
				preparedCandidateIndex = relayInfo.RouteCandidateIndex
			}
		}

		channel, channelErr := getChannel(c, relayInfo, retryParam)
		if channelErr != nil {
			logger.LogError(c, channelErr.Error())
			newAPIError = channelErr
			if len(relayInfo.RouteCandidates) > 0 {
				candidate := relayInfo.RouteCandidates[relayInfo.RouteCandidateIndex]
				AppendRouteAttempt(GetRequestId(c), modelcatalog.Attempt{
					RouteID: candidate.RouteID, TargetID: candidate.TargetID, ChannelID: candidate.ChannelID,
					Wire: candidate.SelectedWire, Status: channelErr.StatusCode, Reason: string(channelErr.GetErrorCode()), RejectedReason: "channel_unavailable",
					DurationMs: time.Since(attemptStart).Milliseconds(),
				})
			}
			if len(relayInfo.RouteCandidates) > 0 && shouldRetry(c, channelErr, common.RetryTimes-retryParam.GetRetry()) && advanceRouteCandidate(relayInfo) {
				continue
			}
			break
		}
		if len(relayInfo.RouteCandidates) > 0 {
			if err := accountpool.ValidateResources(c); err != nil {
				newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusConflict, types.ErrOptionWithSkipRetry())
				break
			}
		}
		if len(relayInfo.RouteCandidates) == 0 {
			if validationErr := validateDomesticUnderstandingForAttempt(c, relayInfo, channel); validationErr != nil {
				newAPIError = validationErr
				break
			}
			relayInfo.ResolvedChannelId = channel.Id
			if legacyContinuityChannelID != channel.Id {
				if continuityErr := relay.PrepareResponsesContinuityForAttempt(c, relayInfo); continuityErr != nil {
					newAPIError = continuityErr
					break
				}
				legacyContinuityChannelID = channel.Id
			}
			if err := accountpool.ValidateResources(c); err != nil {
				newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusConflict, types.ErrOptionWithSkipRetry())
				break
			}
			if !legacyPricingPrepared {
				if billingErr := prepareAttemptBilling(c, relayInfo, tokens, meta); billingErr != nil {
					newAPIError = billingErr
					break
				}
				billingLifecycleStarted = true
				legacyPricingPrepared = true
			}
		}

		var relayStarted bool
		newAPIError, relayStarted = relaySelectedChannel(c, relayInfo, channel, relayFormat, ws)
		attempt := modelcatalog.NewAttempt(channel.Id, relayInfo.SelectedWire, 0, "", attemptStart)
		attempt.RouteID = relayInfo.ModelRouteId
		attempt.TargetID = relayInfo.ModelTargetId
		AppendRouteAttempt(GetRequestId(c), attempt)
		if newAPIError != nil {
			UpdateLastAttempt(GetRequestId(c), newAPIError.StatusCode, string(newAPIError.GetErrorCode()))
		}
		if !relayStarted {
			break
		}

		if newAPIError == nil {
			relayInfo.RouteDecisionTrace, _ = GetRouteDecisionTrace(GetRequestId(c))
			relayInfo.LastError = nil
			return
		}

		newAPIError = service.NormalizeViolationFeeError(newAPIError)
		relayInfo.LastError = newAPIError

		processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError)

		// Gateway resolves credentials at header construction. Exhaustion there
		// has sent nothing, so an authorized route fallback still has its budget.
		var unavailable *model.UpstreamAccountUnavailable
		if errors.As(newAPIError.Err, &unavailable) && accountpool.RetryAllowed(c) {
			if len(relayInfo.RouteCandidates) > 0 {
				if advanceRouteCandidate(relayInfo) {
					retryParam.ResetRetryNextTry()
					continue
				}
			} else {
				next, selectionErr := middleware.FallbackAccountChannel(c, channel, relayInfo.ResolvedModelNameOrOrigin(), retryParam, newAPIError)
				if selectionErr == nil {
					relayInfo.UsingGroup = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
					accountpool.State(c).RetryChannel = next
					retryParam.ResetRetryNextTry()
					continue
				}
				newAPIError = selectionErr
			}
		}

		if !shouldRetry(c, newAPIError, common.RetryTimes-retryParam.GetRetry()) {
			break
		}
		samePool, poolErr := accountpool.PrepareNext(c)
		if poolErr != nil {
			break
		}
		if samePool {
			accountpool.State(c).RetryChannel = channel
			continue
		}
		if len(relayInfo.RouteCandidates) > 0 {
			if !advanceRouteCandidate(relayInfo) {
				break
			}
		}
	}

	relayInfo.RouteDecisionTrace, _ = GetRouteDecisionTrace(GetRequestId(c))

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	if newAPIError != nil {
		perfmetrics.RecordRelaySampleAsync(relayInfo, false, 0)
	}
}

// Once a keepalive commits SSE headers, errors must use that same stream.
func writeRelayStreamError(c *gin.Context, format types.RelayFormat, apiError *types.NewAPIError) {
	common.SetContextKey(c, constant.ContextKeyRelayFailed, true)
	status, _ := common.GetContextKeyType[*relaycommon.StreamStatus](c, constant.ContextKeyResponseStreamStatus)
	if status == nil {
		status = relaycommon.NewStreamStatus()
		common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, status)
	}
	status.RecordError(apiError.Error())
	status.SetEndReason(relaycommon.StreamEndReasonHandlerStop, apiError)
	if format == types.RelayFormatClaude {
		_ = helper.ClaudeData(c, dto.ClaudeResponse{Type: "error", Error: apiError.ToClaudeError()})
		return
	}
	_ = helper.ObjectData(c, gin.H{"error": apiError.ToOpenAIError()})
	helper.Done(c)
}

func recordRouteAdmissionDecision(requestID string, relayInfo *relaycommon.RelayInfo) {
	StartRouteDecision(requestID, relayInfo)
	for _, candidate := range relayInfo.RejectedRouteCandidates {
		reason := candidate.RejectedReason
		if reason == "" {
			reason = string(types.ErrorCodeContextCapacityExceeded)
		}
		AppendRouteAttempt(requestID, modelcatalog.Attempt{
			RouteID: candidate.RouteID, TargetID: candidate.TargetID, ChannelID: candidate.ChannelID,
			Wire: candidate.SelectedWire, Status: http.StatusBadRequest,
			Reason: reason, RejectedReason: reason,
		})
	}
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func addUsedChannel(c *gin.Context, channelId int) {
	useChannel := c.GetStringSlice("use_channel")
	useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
	c.Set("use_channel", useChannel)
}

func fastTokenCountMetaForPricing(request dto.Request) *types.TokenCountMeta {
	if request == nil {
		return &types.TokenCountMeta{}
	}
	meta := &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
		maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
		if maxCompletionTokens > maxTokens {
			meta.MaxTokens = int(maxCompletionTokens)
		} else {
			meta.MaxTokens = int(maxTokens)
		}
	case *dto.OpenAIResponsesRequest:
		meta.MaxTokens = int(lo.FromPtrOr(r.MaxOutputTokens, uint(0)))
	case *dto.ClaudeRequest:
		meta.MaxTokens = int(lo.FromPtr(r.MaxTokens))
	case *dto.ImageRequest:
		// Pricing for image requests depends on ImagePriceRatio; safe to compute even when CountToken is disabled.
		return r.GetTokenCountMeta()
	default:
		// Best-effort: leave CombineText empty to avoid large allocations.
	}
	return meta
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {
	if prior := accountpool.State(c).RetryChannel; prior != nil {
		accountpool.State(c).RetryChannel = nil
		channel, err := model.GetChannelById(prior.Id, true)
		if err != nil || channel == nil || channel.Status != common.ChannelStatusEnabled {
			return nil, types.NewError(errors.New("retry channel unavailable"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if len(info.RouteCandidates) > 0 {
			if err := revalidateRouteCandidateAbility(c, info.RouteCandidates[info.RouteCandidateIndex]); err != nil {
				return nil, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
			}
		} else if !model.IsChannelEnabledForGroupModel(info.UsingGroup, info.ResolvedModelNameOrOrigin(), channel.Id) {
			return nil, types.NewError(errors.New("retry channel authorization changed"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if err := middleware.SetupContextForSelectedChannel(c, channel, info.ResolvedModelNameOrOrigin()); err != nil {
			return nil, err
		}
		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
		return channel, nil
	}

	if len(info.RouteCandidates) > 0 {
		if info.RouteCandidateIndex < 0 || info.RouteCandidateIndex >= len(info.RouteCandidates) {
			return nil, types.NewError(fmt.Errorf("resolved route candidate index %d is invalid", info.RouteCandidateIndex), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		candidate := info.RouteCandidates[info.RouteCandidateIndex]
		channel, err := model.GetChannelById(candidate.ChannelID, true)
		if err != nil || channel == nil || channel.Status != common.ChannelStatusEnabled {
			return nil, types.NewError(fmt.Errorf("resolved route target %d channel %d is unavailable", candidate.TargetID, candidate.ChannelID), types.ErrorCodeGetChannelFailed)
		}
		// SetupContextForSelectedChannel overwrites common channel keys, but its
		// provider-specific optional keys are conditional. Clear them so a retry
		// cannot inherit organization/version/region/plugin state from the prior
		// candidate.
		c.Set("channel_organization", "")
		c.Set("api_version", "")
		c.Set("region", "")
		c.Set("plugin", "")
		if setupErr := middleware.SetupContextForSelectedChannel(c, channel, candidate.UpstreamModel); setupErr != nil {
			return nil, setupErr
		}
		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
		return channel, nil
	}
	if info.ChannelMeta == nil {
		if selected := accountpool.State(c).Selected; selected != nil {
			channel, err := model.GetChannelById(selected.Ref.ChannelID, true)
			if err != nil {
				return nil, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
			}
			info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
			return channel, nil
		}
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		channel := &model.Channel{
			Id:      c.GetInt("channel_id"),
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}
		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
		return channel, nil
	}
	channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(retryParam)

	if err != nil {
		return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.ResolvedModelNameOrOrigin(), err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if channel == nil {
		return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.ResolvedModelNameOrOrigin()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	channel, newAPIError := middleware.SetupAvailableAccountChannel(c, channel, info.ResolvedModelNameOrOrigin(), retryParam)
	if newAPIError != nil {
		return nil, newAPIError
	}
	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
	return channel, nil
}

func prepareAttemptBilling(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) *types.NewAPIError {
	if info.PlatformMode {
		if info.PlatformBilling == nil {
			return types.NewError(errors.New("platform billing missing"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		// Platform funding is owned by UniHub. Construct the neutral context
		// from the frozen remote tariff before any helper can enter a provider;
		// no local price/quota reservation is consulted.
		price, ok := info.PlatformBilling.MeteredPrice()
		if !ok {
			// exclusive_v1 remains supported only by its Chat helper, which owns
			// Session.Dispatch and raw usage observation.
			return nil
		}
		if (price.Operation == "chat" || price.Operation == "responses" || price.Operation == "embedding") && c.GetInt("channel_type") != constant.ChannelTypeOpenAI {
			return types.NewError(errors.New("platform metered producer unavailable for this channel"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		if len(info.ParamOverride) > 0 || len(info.HeadersOverride) > 0 || info.ChannelSetting.SystemPrompt != "" || info.ChannelSetting.PassThroughBodyEnabled || model_setting.GetGlobalSettings().PassThroughRequestEnabled {
			return types.NewError(errors.New("platform metering requires unchanged validated request"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		if info.NativeBilling == nil {
			info.NativeBilling = &relaycommon.NativeBillingContext{
				CallID: info.PlatformBilling.CallID(), Platform: true, Price: price,
				Observe: info.PlatformBilling.ObserveMetering,
			}
		}
		return nil
	}

	operation := ""
	switch info.RelayMode {
	case relayconstant.RelayModeChatCompletions, relayconstant.RelayModeCompletions, relayconstant.RelayModeGemini:
		operation = "chat"
	case relayconstant.RelayModeResponses:
		operation = "responses"
	case relayconstant.RelayModeEmbeddings, relayconstant.RelayModeMultimodalEmbedding:
		operation = "embedding"
	case relayconstant.RelayModeAudioSpeech:
		operation = "speech"
	case relayconstant.RelayModeAudioTranscription, relayconstant.RelayModeAudioTranslation:
		operation = "transcription"
	case relayconstant.RelayModeImagesGenerations:
		operation = "image_generation"
	case relayconstant.RelayModeImagesEdits:
		operation = "image_edit"
	}
	if info.NativeBilling != nil {
		return types.NewError(errors.New("native dispatch cannot be retried without final evidence"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if price, configured := billing_setting.ResolveModelNativePrice(info.BillingModelNameOrOrigin(), operation); configured {
		var reservation metering.Usage
		var err error
		if operation != "chat" && operation != "responses" && operation != "embedding" {
			if err = helper.ValidateNativeAudioAdmission(c, info); err == nil {
				reservation, err = service.EstimateNativeMediaUsage(info, info.Request, price)
			}
		} else {
			reservation, err = service.NativeTokenReservation(info, price)
		}
		if err != nil {
			return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		ratio := helper.HandleGroupRatio(c, info)
		a := model.NativeAdmission{CallID: info.RequestId, UserID: info.UserId, TokenID: info.TokenId, ChannelID: info.ChannelId, Model: info.BillingModelNameOrOrigin(), Group: info.UsingGroup, Preference: info.UserSetting.BillingPreference, Price: price, GroupRatio: strconv.FormatFloat(ratio.GroupRatio, 'f', -1, 64), QuotaPerUnit: strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64), Reservation: reservation}
		if err := service.BeginNativeBillingForRelay(c.Request.Context(), info, a); err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		return nil
	}
	// Native per-image pricing uses the validated selected model contract. The
	// public alias may resemble DALL·E and carry unrelated size/quality ratios.
	if c.GetInt("channel_type") == constant.ChannelTypeAli {
		if _, image := info.Request.(*dto.ImageRequest); image {
			if value, exists := c.Get(helper.DomesticImageRequestKey); exists {
				native := value.(helper.DomesticImageRequest)
				normalized := *meta
				normalized.ImagePriceRatio = 1
				normalized.BillingRatios = map[string]float64{"n": float64(native.Parameters.N)}
				meta = &normalized
			}
		}
	}

	priceData, err := helper.ModelPriceHelper(c, info, promptTokens, meta)
	if err != nil {
		return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry())
	}
	if err := helper.PrepareMultimodalReservation(c, info); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	priceData = info.PriceData
	if err := helper.ValidateNativeImagePrice(c, info); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ValidateNativeSpeechPrice(c, info); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if priceData.FreeModel {
		logger.LogInfo(c, fmt.Sprintf("模型 %s 免费，跳过预扣费", info.OriginModelName))
		return nil
	}
	if info.Billing == nil {
		return service.PreConsumeBilling(c, priceData.QuotaToPreConsume, info)
	}
	if err := info.Billing.Reserve(priceData.QuotaToPreConsume); err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	info.FinalPreConsumedQuota = info.Billing.GetPreConsumedQuota()
	return nil
}

// prepareRouteCandidateForAttempt keeps the live authorization check ahead of
// every quota reservation. Route candidates are immutable resolution
// snapshots, while channels and abilities can change between resolution and
// an attempt.
func prepareRouteCandidateForAttempt(c *gin.Context, info *relaycommon.RelayInfo, index, promptTokens int, meta *types.TokenCountMeta) *types.NewAPIError {
	if err := selectRouteCandidateForAttempt(c, info, index); err != nil {
		return types.NewError(err, types.ErrorCodeGetChannelFailed)
	}
	UpdateRouteDecisionSelection(GetRequestId(c), info)
	if err := revalidateRouteCandidateAbility(c, info.RouteCandidates[index]); err != nil {
		return types.NewError(err, types.ErrorCodeGetChannelFailed)
	}
	candidateChannel, err := model.GetChannelById(info.RouteCandidates[index].ChannelID, true)
	if err != nil {
		return types.NewError(err, types.ErrorCodeGetChannelFailed)
	}
	if common.GetContextKeyBool(c, constant.ContextKeyDeferredModelRoute) {
		protocol, operation, err := capability.ResolveRequest(c.Request.Method, c.Request.URL.Path)
		if err != nil || !middleware.ChannelSupportsRequestPath(candidateChannel, c.Request.URL.Path, info.ResolvedModelNameOrOrigin(), protocol, operation) {
			return types.NewErrorWithStatusCode(errors.New("configured target does not support the request operation/path"), types.ErrorCodeNoCapableChannel, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}

	if validationErr := prepareMultimodalForAttempt(c, info, candidateChannel); validationErr != nil {
		return types.NewErrorWithStatusCode(validationErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if validationErr := validateDomesticUnderstandingForAttempt(c, info, candidateChannel); validationErr != nil {
		return validationErr
	}

	if err := modelcatalog.ValidateChannelOperations(info.ModelCapabilities, candidateChannel.Type); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if candidateChannel.ChannelInfo.IsMultiKey || common.GetContextKeyBool(c, constant.ContextKeyDeferredModelRoute) {
		if setupErr := middleware.SetupContextForSelectedChannel(c, candidateChannel, info.ResolvedModelNameOrOrigin()); setupErr != nil {
			return setupErr
		}
	}
	contract := info.ModelCapabilities
	if common.GetContextKeyBool(c, constant.ContextKeyAliNativeRerank) || len(contract.InputModalities) > 0 || len(contract.OutputModalities) > 0 || len(contract.Operations) > 0 || contract.Streaming != nil || contract.Async != nil || contract.MediaLimits != nil || contract.EmbeddingSpace != nil {
		// Arbitrary patch operations/templates can inject media or replace an
		// admitted model/input. Only direct, finite sampling scalars are known
		// to preserve this contract before final-body media admission exists.
		for name, value := range common.GetContextKeyStringMap(c, constant.ContextKeyChannelParamOverride) {
			switch name {
			case "temperature", "top_p", "seed", "frequency_penalty", "presence_penalty":
				number, ok := value.(float64)
				if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
					return types.NewErrorWithStatusCode(errors.New("model contract requires direct finite scalar parameter overrides"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
				}
			default:
				return types.NewErrorWithStatusCode(fmt.Errorf("parameter override %q cannot be proven to preserve the declared model contract", name), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
		}
	}

	if err := relay.PrepareResponsesContinuityForAttempt(c, info); err != nil {
		return err
	}
	if err := accountpool.ValidateResources(c); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusConflict, types.ErrOptionWithSkipRetry())
	}
	return prepareAttemptBilling(c, info, promptTokens, meta)
}

func revalidateRouteCandidateAbility(c *gin.Context, candidate relaycommon.RouteCandidate) error {
	if c == nil || c.Request == nil {
		return errors.New("route candidate validation requires a request context")
	}
	sources := modelcatalog.NewGormRuntimeSources(model.DB)
	channel, exists, err := sources.Channel(c.Request.Context(), candidate.ChannelID)
	if err != nil {
		return fmt.Errorf("load resolved route target %d channel %d: %w", candidate.TargetID, candidate.ChannelID, err)
	}
	if !exists || !channel.Enabled {
		return fmt.Errorf("resolved route target %d channel %d is unavailable", candidate.TargetID, candidate.ChannelID)
	}
	abilities, err := sources.Abilities(c.Request.Context(), candidate.ChannelID, candidate.UpstreamModel)
	if err != nil {
		return fmt.Errorf("load resolved route target %d ability: %w", candidate.TargetID, err)
	}
	selectedGroup := strings.TrimSpace(candidate.SelectedGroup)
	for _, ability := range abilities {
		if ability.Enabled &&
			strings.EqualFold(strings.TrimSpace(ability.Model), strings.TrimSpace(candidate.UpstreamModel)) &&
			strings.EqualFold(strings.TrimSpace(ability.Group), selectedGroup) {
			return nil
		}
	}
	return fmt.Errorf(
		"resolved route target %d channel %d no longer has enabled ability for model %s in group %s",
		candidate.TargetID, candidate.ChannelID, candidate.UpstreamModel, candidate.SelectedGroup,
	)
}

func shouldRetry(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	if v, ok := c.Get("platform_billing"); ok {
		session, valid := v.(*platformbilling.Session)
		if !valid || session == nil || !session.CanRetry() || c.Writer.Written() {
			return false
		}
	}
	if openaiErr == nil || retryTimes <= 0 || !accountpool.RetryAllowed(c) {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if types.IsChannelError(openaiErr) {
		return true
	}
	if types.IsSkipRetryError(openaiErr) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	code := openaiErr.StatusCode
	if code >= 200 && code < 300 {
		return false
	}
	if code < 100 || code > 599 {
		return true
	}
	if operation_setting.IsAlwaysSkipRetryCode(openaiErr.GetErrorCode()) {
		return false
	}
	return operation_setting.ShouldRetryByStatusCode(code)
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError) {
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	if accountpool.State(c).Sent == nil && service.ShouldDisableChannel(err) && channelError.AutoBan {
		gopool.Go(func() {
			service.DisableChannel(channelError, err.ErrorWithStatusCode())
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := c.GetInt("channel_id")
		other := make(map[string]interface{})
		if c.Request != nil && c.Request.URL != nil {
			other["request_path"] = c.Request.URL.Path
		}
		other["error_type"] = err.GetErrorType()
		other["error_code"] = err.GetErrorCode()
		other["status_code"] = err.StatusCode
		other["channel_id"] = channelId
		other["channel_name"] = c.GetString("channel_name")
		other["channel_type"] = c.GetInt("channel_type")
		adminInfo := make(map[string]interface{})
		adminInfo["use_channel"] = c.GetStringSlice("use_channel")
		isMultiKey := common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey)
		if isMultiKey {
			adminInfo["is_multi_key"] = true
			adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
		}
		service.AppendChannelAffinityAdminInfo(c, adminInfo)
		other["admin_info"] = adminInfo
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}

}

func RelayMidjourney(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"description": fmt.Sprintf("failed to generate relay info: %s", err.Error()),
			"type":        "upstream_error",
			"code":        4,
		})
		return
	}

	var mjErr *dto.MidjourneyResponse
	switch relayInfo.RelayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		mjErr = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		mjErr = relay.RelayMidjourneyTask(c, relayInfo.RelayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		mjErr = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		mjErr = relay.RelaySwapFace(c, relayInfo)
	default:
		mjErr = relay.RelayMidjourneySubmit(c, relayInfo)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(mjErr)
	if mjErr != nil {
		statusCode := http.StatusBadRequest
		if mjErr.Code == 30 {
			mjErr.Result = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result),
			"type":        "upstream_error",
			"code":        mjErr.Code,
		})
		channelId := c.GetInt("channel_id")
		logger.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result)))
	}
}

func RelayNotFound(c *gin.Context) {
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}

	if relayInfo.PlatformMode {
		if relayInfo.PlatformBilling == nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		price, ok := relayInfo.PlatformBilling.MeteredPrice()
		if !ok || price.Operation != "video" {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		relayInfo.NativeBilling = &relaycommon.NativeBillingContext{CallID: relayInfo.PlatformBilling.CallID(), Platform: true, Price: price, Observe: relayInfo.PlatformBilling.ObserveMetering}
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}

	var result *relay.TaskSubmitResult
	var taskErr *dto.TaskError
	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.ResolvedModelNameOrOrigin(),
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}

	accountpool.State(c).MaxSends = common.RetryTimes + 1
	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		var channel *model.Channel

		if lockedCh, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedCh != nil {
			channel = lockedCh
			if retryParam.GetRetry() > 0 {
				if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
					taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
					break
				}
			}
		} else {
			var channelErr *types.NewAPIError
			channel, channelErr = getChannel(c, relayInfo, retryParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				break
			}
		}

		addUsedChannel(c, channel.Id)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		result, taskErr = relay.RelayTaskSubmit(c, relayInfo)
		if taskErr == nil {
			break
		}

		if !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode))
		}

		if !shouldRetryTaskRelay(c, channel.Id, taskErr, common.RetryTimes-retryParam.GetRetry()) {
			break
		}
		samePool, poolErr := accountpool.PrepareNext(c)
		if poolErr != nil {
			break
		}
		if samePool {
			accountpool.State(c).RetryChannel = channel
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	// ── 成功：结算 + 日志 + 插入任务 ──
	if taskErr == nil {
		if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
			common.SysError("settle task billing error: " + settleErr.Error())
		}
		service.LogTaskConsumption(c, relayInfo)

		task := model.InitTask(result.Platform, relayInfo)
		task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
		if sent := accountpool.State(c).Sent; sent != nil {
			ref := sent.Ref
			task.PrivateData.Account = &ref
			if owner := accountpool.State(c).Owner; owner != nil {
				ownerCopy := *owner
				task.PrivateData.Account = &ownerCopy
			}
			task.PrivateData.Key = ""
		}
		task.PrivateData.BillingSource = relayInfo.BillingSource
		task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
		task.PrivateData.TokenId = relayInfo.TokenId
		task.PrivateData.NodeName = common.NodeName
		task.PrivateData.BillingContext = &model.TaskBillingContext{
			ModelPrice:      relayInfo.PriceData.ModelPrice,
			GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      relayInfo.PriceData.ModelRatio,
			OtherRatios:     relayInfo.PriceData.OtherRatios(),
			OriginModelName: relayInfo.OriginModelName,
			PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		}
		task.Quota = result.Quota
		task.Data = result.TaskData
		task.Action = relayInfo.Action
		if insertErr := task.Insert(); insertErr != nil {
			common.SysError("insert task error: " + insertErr.Error())
		}
	}

	if taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *dto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *dto.TaskError, retryTimes int) bool {
	if retryTimes <= 0 || !accountpool.RetryAllowed(c) || accountpool.State(c).TaskBound {
		return false
	}
	if taskErr == nil {
		return false
	}
	var apiErr *types.NewAPIError
	if errors.As(taskErr.Error, &apiErr) && types.IsSkipRetryError(apiErr) {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if taskErr.StatusCode == 307 {
		return true
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			return false
		}
		return true
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return false
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if taskErr.LocalError {
		return false
	}
	if taskErr.StatusCode/100 == 2 {
		return false
	}
	return true
}

// resolveClientModelRoute attempts to resolve the requested OriginModelName
// into an ordered public-model target list before billing and channel
// selection. A catalog miss preserves the legacy distributor path.
func resolveClientModelRoute(c *gin.Context, relayInfo *relaycommon.RelayInfo, estimatedInputTokens int) *types.NewAPIError {
	if relayInfo == nil || strings.TrimSpace(relayInfo.OriginModelName) == "" {
		return nil
	}
	if model.DB == nil || !model.DB.Migrator().HasTable(&model.ClientModelRoute{}) || !model.DB.Migrator().HasTable(&model.ClientModelTarget{}) {
		if common.GetContextKeyBool(c, constant.ContextKeyDeferredModelRoute) {
			return types.NewErrorWithStatusCode(errors.New("configured model route is no longer available"), types.ErrorCodeGetChannelFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil
	}

	groups, err := getModelListGroups(c)
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}

	client := clientForRequest(c, relayInfo)
	var routes []model.ClientModelRoute
	var targets []model.ClientModelTarget
	if err := model.DB.Where("enabled = ?", true).Find(&routes).Error; err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	if err := model.DB.Where("enabled = ?", true).Find(&targets).Error; err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	if !hasMatchingClientModelRoute(routes, relayInfo.OriginModelName, client) {
		if common.GetContextKeyBool(c, constant.ContextKeyDeferredModelRoute) {
			return types.NewErrorWithStatusCode(errors.New("configured model route is no longer available for this client"), types.ErrorCodeGetChannelFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil
	}

	catalog, err := modelcatalog.AssembleRuntimeCatalog(c.Request.Context(), modelcatalog.RuntimeContext{
		UserID: c.GetInt("id"),
		Groups: groups.ownerGroups,
		Client: client,
	}, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), requestCatalogAuthorizer{})
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}

	resolved, err := catalog.ResolveCandidates(c.Request.Context(), c.GetInt("id"), groups.ownerGroups, relayInfo.OriginModelName, client)
	if err != nil {
		return types.NewError(fmt.Errorf("configured model route %s has no available target: %w", relayInfo.OriginModelName, err), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if len(resolved) > 0 {
		_, operation, operationErr := capability.ResolveRequest(c.Request.Method, c.Request.URL.Path)
		if operationErr == nil {
			if _, audio := relayInfo.Request.(*dto.AudioRequest); audio {
				selectedChannel, err := model.GetChannelById(resolved[0].ChannelID, true)
				if err != nil {
					return types.NewError(err, types.ErrorCodeGetChannelFailed)
				}
				if err := helper.ValidateDomesticAudio(c, relayInfo, selectedChannel.Type, resolved[0].UpstreamModel, resolved[0].BillingModel); err != nil {
					return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
				}
			}
			if err := helper.ValidateModelAdmission(c, relayInfo, resolved[0].Capabilities, operation); err != nil {
				return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
		} else if len(resolved[0].Capabilities.Operations) > 0 {
			return types.NewErrorWithStatusCode(operationErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	admissionTokens := estimatedInputTokens
	if !constant.CountToken && estimatedInputTokens == 0 {
		admissionTokens = -1
		if relayInfo.Request != nil {
			admissionMeta := relayInfo.Request.GetTokenCountMeta()
			if independentEstimate, estimateErr := service.EstimateRequestTokenForAdmission(c, admissionMeta, relayInfo); estimateErr == nil {
				admissionTokens = independentEstimate
			}
		}
	}
	eligible, rejected := filterRouteCandidatesByContext(resolved, admissionTokens)
	relayInfo.RejectedRouteCandidates = make([]relaycommon.RouteCandidate, 0, len(rejected))
	for i := range rejected {
		relayInfo.RejectedRouteCandidates = append(relayInfo.RejectedRouteCandidates, rejectedRouteCandidate(&rejected[i], string(types.ErrorCodeContextCapacityExceeded)))
	}
	if len(eligible) == 0 {
		if len(resolved) > 0 {
			relayInfo.PublicModelName = resolved[0].PublicID
			relayInfo.ModelRouteId = resolved[0].RouteID
		}
		return types.NewErrorWithStatusCode(
			fmt.Errorf("model %s has no route target that can fit the request context", relayInfo.OriginModelName),
			types.ErrorCodeContextCapacityExceeded,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	relayInfo.PublicModelName = eligible[0].PublicID
	relayInfo.RouteCandidates = make([]relaycommon.RouteCandidate, 0, len(eligible))
	for i := range eligible {
		candidate, wireErr := routeCandidateFromResolved(relayInfo, &eligible[i])
		if wireErr != nil {
			relayInfo.RejectedRouteCandidates = append(relayInfo.RejectedRouteCandidates, rejectedRouteCandidate(&eligible[i], "unsupported_wire"))
			continue
		}
		relayInfo.RouteCandidates = append(relayInfo.RouteCandidates, candidate)
	}
	if len(relayInfo.RouteCandidates) == 0 {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("model %s has no route target supporting request format %s", relayInfo.OriginModelName, relayInfo.RelayFormat),
			types.ErrorCodeNoCapableChannel,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if err := selectRouteCandidateForAttempt(c, relayInfo, 0); err != nil {
		return types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	return nil
}

func hasMatchingClientModelRoute(routes []model.ClientModelRoute, requestedModel, client string) bool {
	requested := strings.ToLower(strings.TrimSpace(requestedModel))
	client = strings.ToLower(strings.TrimSpace(client))
	for _, route := range routes {
		routeClient := strings.ToLower(strings.TrimSpace(route.Client))
		if routeClient == "" {
			routeClient = "all"
		}
		if routeClient != "all" && routeClient != client {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(route.PublicId), requested) {
			return true
		}
		var aliases []string
		if common.Unmarshal([]byte(route.Aliases), &aliases) != nil {
			continue
		}
		for _, alias := range aliases {
			if strings.EqualFold(strings.TrimSpace(alias), requested) {
				return true
			}
		}
	}
	return false
}

func filterRouteCandidatesByContext(candidates []modelcatalog.ResolvedModel, estimated int) ([]modelcatalog.ResolvedModel, []modelcatalog.ResolvedModel) {
	eligible := make([]modelcatalog.ResolvedModel, 0, len(candidates))
	rejected := make([]modelcatalog.ResolvedModel, 0)
	for _, candidate := range candidates {
		if estimated < 0 && (candidate.ContextWindow > 0 || candidate.RequiresKnownContext) {
			rejected = append(rejected, candidate)
			continue
		}
		if candidate.ContextWindow > 0 && estimated > candidate.ContextWindow {
			rejected = append(rejected, candidate)
			continue
		}
		if candidate.ContextWindow == 0 && candidate.RequiresKnownContext {
			rejected = append(rejected, candidate)
			continue
		}
		eligible = append(eligible, candidate)
	}
	return eligible, rejected
}

func routeCandidateFromResolved(relayInfo *relaycommon.RelayInfo, resolved *modelcatalog.ResolvedModel) (relaycommon.RouteCandidate, error) {
	candidate := relaycommon.RouteCandidate{
		Capabilities: resolved.Capabilities,
		RouteID:      resolved.RouteID, TargetID: resolved.TargetID, ChannelID: resolved.ChannelID,
		UpstreamModel: resolved.UpstreamModel, BillingModel: resolved.BillingModel,
		SelectedGroup: resolved.SelectedGroup, ContextWindow: resolved.ContextWindow, RequiresKnownContext: resolved.RequiresKnownContext,
	}
	decision, err := selectWireForRequest(relayInfo, resolved)
	if err != nil {
		return relaycommon.RouteCandidate{}, err
	}
	candidate.SelectedWire = string(decision.Wire)
	return candidate, nil
}

func rejectedRouteCandidate(resolved *modelcatalog.ResolvedModel, reason string) relaycommon.RouteCandidate {
	return relaycommon.RouteCandidate{
		RouteID: resolved.RouteID, TargetID: resolved.TargetID, ChannelID: resolved.ChannelID,
		UpstreamModel: resolved.UpstreamModel, BillingModel: resolved.BillingModel, SelectedGroup: resolved.SelectedGroup,
		ContextWindow: resolved.ContextWindow, RequiresKnownContext: resolved.RequiresKnownContext, RejectedReason: reason,
	}
}

func selectRouteCandidateForAttempt(c *gin.Context, relayInfo *relaycommon.RelayInfo, index int) error {
	if relayInfo == nil {
		return errors.New("relay info is nil")
	}
	if index < 0 || index >= len(relayInfo.RouteCandidates) {
		return fmt.Errorf("route candidate index %d is out of range", index)
	}
	candidate := relayInfo.RouteCandidates[index]
	relayInfo.ResetChannelAttemptState()
	relayInfo.RouteCandidateIndex = index
	relayInfo.ResolvedModelName = candidate.UpstreamModel
	if c != nil {
		accountpool.State(c).ResolvedRoute = true
	}
	relayInfo.BillingModelName = candidate.BillingModel
	relayInfo.ModelRouteId = candidate.RouteID
	relayInfo.ModelTargetId = candidate.TargetID
	relayInfo.ResolvedChannelId = candidate.ChannelID
	relayInfo.SelectedWire = candidate.SelectedWire
	relayInfo.ModelCapabilities = candidate.Capabilities
	relayInfo.RouteContextWindow = candidate.ContextWindow
	if candidate.SelectedGroup != "" {
		relayInfo.UsingGroup = candidate.SelectedGroup
		if c != nil {
			common.SetContextKey(c, constant.ContextKeyUsingGroup, candidate.SelectedGroup)
			common.SetContextKey(c, constant.ContextKeyAutoGroup, candidate.SelectedGroup)
		}
	}
	return nil
}

func advanceRouteCandidate(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil || relayInfo.RouteCandidateIndex+1 >= len(relayInfo.RouteCandidates) {
		return false
	}
	relayInfo.RouteCandidateIndex++
	return true
}

func clientForRequest(c *gin.Context, relayInfo *relaycommon.RelayInfo) string {
	if client := strings.TrimSpace(c.Query("client")); client != "" {
		return client
	}
	path := c.Request.URL.Path
	if strings.Contains(path, "/v1/messages") {
		return "claude"
	}
	if strings.Contains(path, "/v1beta/models") || (strings.Contains(path, "/v1/models/") && strings.Contains(path, ":")) {
		return "gemini"
	}
	return "codex"
}

func selectWireForRequest(relayInfo *relaycommon.RelayInfo, resolved *modelcatalog.ResolvedModel) (relayprotocol.Decision, error) {
	expectedWire := expectedWireForFormat(relayInfo.RelayFormat)
	if expectedWire == "" {
		return relayprotocol.Decision{}, errors.New("no wire for request format")
	}
	declared := map[modelcatalog.WireProtocol]bool{}
	for _, wire := range resolved.PreferredWires {
		if wire == expectedWire {
			declared[wire] = true
		}
	}
	if len(declared) == 0 {
		return relayprotocol.Decision{}, errors.New("resolved route does not declare expected wire")
	}
	return relayprotocol.Resolve(relayprotocol.ResolveInput{
		Requested: resolved.PreferredWires,
		Declared:  declared,
		Observed:  map[modelcatalog.WireProtocol]relayprotocol.Observation{},
	})
}

func expectedWireForFormat(format types.RelayFormat) modelcatalog.WireProtocol {
	switch format {
	case types.RelayFormatMultimodalEmbedding:
		return modelcatalog.WireMultimodalEmbedding
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		return modelcatalog.WireResponses
	case types.RelayFormatClaude:
		return modelcatalog.WireAnthropic
	case types.RelayFormatOpenAI, types.RelayFormatOpenAIAudio, types.RelayFormatOpenAIImage, types.RelayFormatEmbedding, types.RelayFormatRerank:
		return modelcatalog.WireOpenAIChat
	}
	return ""
}

// Validate the selected mapped model before any reservation, on both catalog
// and legacy selection. Mapping is evaluated on a private copy so pricing and
// the original request retain their existing authoritative identities.
func validateDomesticUnderstandingForAttempt(c *gin.Context, info *relaycommon.RelayInfo, channel *model.Channel) *types.NewAPIError {
	if channel.Type != constant.ChannelTypeAli && channel.Type != constant.ChannelTypeMiniMax {
		return nil
	}
	selectedModel := info.ResolvedModelNameOrOrigin()
	if len(info.RouteCandidates) == 0 {
		mapped := *info
		mapped.ChannelMeta = &relaycommon.ChannelMeta{UpstreamModelName: selectedModel}
		if err := helper.ModelMappedHelper(c, &mapped, nil); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		selectedModel = mapped.UpstreamModelName
	}
	if _, image := info.Request.(*dto.ImageRequest); image && channel.Type == constant.ChannelTypeAli {
		if err := helper.ValidateDomesticImage(c, info, channel.Type, selectedModel, info.BillingModelNameOrOrigin()); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		overrides := channel.GetParamOverride()
		if merged, applied := service.ApplyChannelAffinityOverrideTemplate(c, overrides); applied {
			overrides = merged
		}
		if channel.GetSetting().PassThroughBodyEnabled || model_setting.GetGlobalSettings().PassThroughRequestEnabled || len(overrides) > 0 || len(common.GetContextKeyStringMap(c, constant.ContextKeyChannelParamOverride)) > 0 || len(channel.GetHeaderOverride()) > 0 || len(common.GetContextKeyStringMap(c, constant.ContextKeyChannelHeaderOverride)) > 0 {
			return types.NewErrorWithStatusCode(errors.New("native images require typed requests without parameter overrides"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil
	}
	if _, audio := info.Request.(*dto.AudioRequest); audio {
		if channel.Type == constant.ChannelTypeMiniMax {
			voice := info.Request.(*dto.AudioRequest).Voice
			// Public system IDs explicitly documented by the selected native API.
			// Cloned/generated/unknown IDs need an owner binding not implemented here.
			switch voice {
			case "English_Graceful_Lady", "English_Insightful_Speaker", "English_radiant_girl", "English_Persuasive_Man", "Chinese (Mandarin)_Lyrical_Voice", "Chinese (Mandarin)_HK_Flight_Attendant":
			default:
				return types.NewErrorWithStatusCode(errors.New("speech requires a verified public system voice; custom voice ownership is unavailable"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
		}
		if err := helper.ValidateDomesticAudio(c, info, channel.Type, selectedModel, info.BillingModelNameOrOrigin()); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if channel.GetSetting().PassThroughBodyEnabled || model_setting.GetGlobalSettings().PassThroughRequestEnabled || len(channel.GetParamOverride()) > 0 {
			return types.NewErrorWithStatusCode(errors.New("native audio requires typed requests without parameter overrides"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil
	}
	if err := helper.ValidateDomesticUnderstanding(info.Request, channel.Type, selectedModel); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if selectedModel != "qwen3.8-omni-flash" && selectedModel != "qwen3.8-max" {
		return nil
	}
	paramOverrides := channel.GetParamOverride()
	if len(info.RouteCandidates) == 0 {
		paramOverrides = common.GetContextKeyStringMap(c, constant.ContextKeyChannelParamOverride)
	}
	if len(info.RouteCandidates) > 0 {
		if merged, applied := service.ApplyChannelAffinityOverrideTemplate(c, paramOverrides); applied {
			paramOverrides = merged
		}
	}
	for name, value := range paramOverrides {
		switch name {
		case "temperature", "top_p", "seed", "frequency_penalty", "presence_penalty":
			number, ok := value.(float64)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
				return types.NewErrorWithStatusCode(errors.New("selected understanding requires finite scalar overrides"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
		default:
			return types.NewErrorWithStatusCode(fmt.Errorf("selected understanding does not support override %q", name), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	settings := channel.GetSetting()
	if len(info.RouteCandidates) == 0 {
		if selectedSettings, ok := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting); ok {
			settings = selectedSettings
		}
	}
	incompatible := settings.PassThroughBodyEnabled || model_setting.GetGlobalSettings().PassThroughRequestEnabled
	if _, chat := info.Request.(*dto.GeneralOpenAIRequest); chat && service.ShouldChatCompletionsUseResponsesGlobal(channel.Id, channel.Type, info.OriginModelName) {
		incompatible = true
	}
	if info.SelectedWire != "" {
		if _, chat := info.Request.(*dto.GeneralOpenAIRequest); chat && info.SelectedWire != "openai_chat" {
			incompatible = true
		}
		if _, responses := info.Request.(*dto.OpenAIResponsesRequest); responses && info.SelectedWire != "responses" {
			incompatible = true
		}
	}
	if incompatible {
		return types.NewErrorWithStatusCode(errors.New("selected understanding requires direct typed Chat or Responses without raw passthrough"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return nil
}
