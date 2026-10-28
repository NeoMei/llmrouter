package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func prepareMultimodalForAttempt(c *gin.Context, info *relaycommon.RelayInfo, ch *model.Channel) error {
	r, ok := info.Request.(*dto.MultimodalEmbeddingRequest)
	if !ok {
		return nil
	}
	if len(info.RouteCandidates) == 0 || ch.Type != constant.ChannelTypeAli || info.ResolvedModelNameOrOrigin() != "qwen3-vl-embedding" {
		return fmt.Errorf("multimodal embeddings require resolved Ali Qwen route")
	}
	if err := helper.ValidateModelAdmission(c, info, info.ModelCapabilities, capability.OperationMultimodalEmbedding); err != nil {
		return err
	}
	overrides := ch.GetParamOverride()
	if merged, applied := service.ApplyChannelAffinityOverrideTemplate(c, overrides); applied {
		overrides = merged
	}
	if ch.GetSetting().PassThroughBodyEnabled || model_setting.GetGlobalSettings().PassThroughRequestEnabled || len(overrides) > 0 || len(ch.GetHeaderOverride()) > 0 || len(common.GetContextKeyStringMap(c, constant.ContextKeyChannelParamOverride)) > 0 || len(common.GetContextKeyStringMap(c, constant.ContextKeyChannelHeaderOverride)) > 0 {
		return fmt.Errorf("multimodal embeddings require typed requests without overrides")
	}
	if info.PlatformMode {
		value, exists := c.Get(helper.MultimodalPreparedKey)
		if !exists || info.PlatformBilling == nil {
			return fmt.Errorf("frozen knowledge media unavailable")
		}
		p, ok := value.(*helper.PreparedMultimodal)
		if !ok {
			return fmt.Errorf("invalid frozen media")
		}
		if _, err := info.PlatformBilling.KnowledgeBody(ch.Id, info.ResolvedModelNameOrOrigin(), ch.Key); err != nil {
			return err
		}
		return helper.ValidatePreparedMultimodal(p, info.ModelCapabilities)
	}
	if billing_setting.GetBillingMode(info.BillingModelNameOrOrigin()) == billing_setting.BillingModeTieredExpr {
		return fmt.Errorf("multimodal embeddings do not support tiered expression pricing")
	}
	reserve, err := helper.MultimodalVisualReserve(r)
	if err != nil {
		return err
	}
	for _, item := range r.Input {
		if item.Video != "" && (RelayMediaRuntime == nil || RelayMediaRuntime.Service == nil) {
			return fmt.Errorf("video embeddings require configured RELAY_MEDIA runtime")
		}
	}
	value, exists := c.Get(helper.MultimodalPreparedKey)
	var p *helper.PreparedMultimodal
	if exists {
		p = value.(*helper.PreparedMultimodal)
	} else {
		p, err = helper.PrepareMultimodalImages(c.Request.Context(), r, reserve)
		if err != nil {
			return err
		}
		c.Set(helper.MultimodalPreparedKey, p)
		for i, item := range p.Contents {
			if item.Video == "" {
				continue
			}
			owner := RelayMediaRuntime.Service
			ownerUserID, ownerTokenID := info.UserId, info.TokenId
			video, err := owner.PrepareMP4(c.Request.Context(), relaymedia.PrepareInput{UserID: ownerUserID, TokenID: ownerTokenID, RequestID: uuid.NewString(), SourceURL: item.Video})
			if err != nil {
				return fmt.Errorf("video preparation failed")
			}
			p.Video = &video
			p.Contents[i].Video = video.URL
			ctx, cancel := context.WithDeadline(c.Request.Context(), video.AttemptDeadline)
			c.Request = c.Request.WithContext(ctx)
			c.Set("internal.multimodal_cleanup", func() {
				cancel()
				cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
				defer done()
				_ = owner.Revoke(cleanup, video.ID, ownerUserID, ownerTokenID)
			})
		}
	}
	if c.Request.Context().Err() != nil {
		return fmt.Errorf("multimodal preparation deadline exceeded")
	}
	return helper.ValidatePreparedMultimodal(p, info.ModelCapabilities)
}
