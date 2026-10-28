package router

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/nativespeech"
	"github.com/gin-gonic/gin"
)

var newQwenASRProducer = nativespeech.NewQwenASRProducer

func relayNativeQwenASR(c *gin.Context, adapter *platformbilling.Adapter) {
	value, ok := c.Get("platform_billing")
	if !ok {
		c.AbortWithStatus(503)
		return
	}
	session, ok := value.(*platformbilling.Session)
	if !ok {
		c.AbortWithStatus(503)
		return
	}
	policy, err := session.AudioPolicy()
	if err != nil || policy.WireOperation != "dashscope.audio.transcriptions.create" {
		c.AbortWithStatus(503)
		return
	}
	var channel model.Channel
	if model.DB.WithContext(c.Request.Context()).First(&channel, policy.ChannelID).Error != nil || channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.IsMultiKey || session.ValidateAudioTarget(channel.Id, channel.Type, policy.UpstreamModel, channel.Key, channel.GetBaseURL()) != nil {
		c.AbortWithStatus(503)
		return
	}
	catalog, err := readTechnicalCatalog(c.Request.Context(), adapter, "superwagie")
	available := false
	if err == nil {
		for _, m := range catalog.Models {
			if m.ID == policy.PublicModel {
				for _, op := range m.Operations {
					if op.ID == policy.WireOperation && op.Available {
						available = true
					}
				}
			}
		}
	}
	if !available {
		c.AbortWithStatus(503)
		return
	}
	request, err := session.QwenASRRequest()
	if err != nil {
		c.AbortWithStatus(400)
		return
	}
	producer, err := newQwenASRProducer(policy.Origin, channel.Key)
	if err != nil {
		c.AbortWithStatus(503)
		return
	}
	if err = session.Dispatch(c.Request.Context(), channel.Id); err != nil {
		c.AbortWithStatus(409)
		return
	}
	result, sendErr := producer.SendQwenASR(c.Request.Context(), request)
	delivered := false
	if sendErr == nil && result.Completed && c.Request.Context().Err() == nil {
		c.Header("Content-Type", "application/json")
		c.Header("Cache-Control", "no-store")
		n, e := c.Writer.Write(result.Raw)
		delivered = e == nil && n == len(result.Raw)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
	defer cancel()
	if err = session.RecordQwenASRObservation(ctx, result, delivered); err != nil {
		if !c.Writer.Written() {
			c.AbortWithStatus(502)
		}
		return
	}
	price, _ := session.MeteredPrice()
	usage := platformbilling.NativeQwenASRMetering(price, result)
	if !delivered {
		usage.State = "incomplete"
		usage.Measurements = nil
	}
	if err = session.ObserveMetering(ctx, usage); err != nil {
		if !c.Writer.Written() {
			c.AbortWithStatus(502)
		}
		return
	}
	if !delivered {
		c.AbortWithStatus(502)
	}
}
