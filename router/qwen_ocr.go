package router

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"time"
	"unicode/utf8"
)

var qwenOCRHTTPClient = func() *http.Client { return service.GetSSRFProtectedHTTPClient() }

func relayQwenOCR(c *gin.Context, a *platformbilling.Adapter) {
	value, ok := c.Get("platform_billing")
	if !ok {
		c.AbortWithStatus(503)
		return
	}
	s, ok := value.(*platformbilling.Session)
	if !ok {
		c.AbortWithStatus(503)
		return
	}
	p, e := s.OCRPolicy()
	if e != nil {
		c.AbortWithStatus(503)
		return
	}
	var channel model.Channel
	if model.DB.WithContext(c.Request.Context()).First(&channel, p.ChannelID).Error != nil || channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.IsMultiKey || s.ValidateOCRTarget(channel.Id, channel.Type, p.UpstreamModel, channel.Key, channel.GetBaseURL()) != nil {
		c.AbortWithStatus(503)
		return
	}
	catalog, e := readTechnicalCatalog(c.Request.Context(), a, "superwagie")
	available := false
	if e == nil {
		for _, m := range catalog.Models {
			if m.ID == p.PublicModel {
				for _, op := range m.Operations {
					if op.ID == p.WireOperation && op.Available {
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
	body, e := s.QwenOCRBody()
	if e != nil {
		c.AbortWithStatus(400)
		return
	}
	client := qwenOCRHTTPClient()
	if client == nil {
		c.AbortWithStatus(503)
		return
	}
	closed := *client
	closed.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	closed.Timeout = 60 * time.Second
	request, e := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, p.Origin+"/api/v1/services/aigc/multimodal-generation/generation", bytes.NewReader(body))
	if e != nil {
		c.AbortWithStatus(503)
		return
	}
	request.Header.Set("Authorization", "Bearer "+channel.Key)
	request.Header.Set("Content-Type", "application/json")
	if e = s.Dispatch(c.Request.Context(), channel.Id); e != nil {
		c.AbortWithStatus(409)
		return
	}
	response, e := closed.Do(request)
	if e != nil {
		c.AbortWithStatus(502)
		return
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	usage, validationErr := s.QwenOCRUsage(raw)
	valid := e == nil && readErr == nil && response.StatusCode == 200 && validationErr == nil && c.Request.Context().Err() == nil
	if !valid {
		usage.State = "incomplete"
		usage.Measurements = nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
	defer cancel()
	if e = s.RecordOCRObservation(ctx, raw, valid); e != nil {
		c.AbortWithStatus(502)
		return
	}
	if readErr == nil && len(raw) <= 8192 && utf8.Valid(raw) && c.Request.Context().Err() == nil {
		failure, _ := accountpool.AliFailure(response.StatusCode, response.Header.Get("Retry-After"), raw)
		var envelope map[string]json.RawMessage
		var requestID, message string
		if failure.Refusal == accountpool.RefusalInvalidCredential && common.UnmarshalUnique(raw, &envelope) == nil && common.UnmarshalUnique(envelope["request_id"], &requestID) == nil && requestID != "" && len(requestID) <= 256 && common.UnmarshalUnique(envelope["message"], &message) == nil && message != "" && envelope["output"] == nil && envelope["usage"] == nil {
			// Existing provider classification proves no execution. Rejected
			// persists that evidence before deferred Finish releases the hold.
			s.Rejected()
			c.AbortWithStatus(502)
			return
		}
	}
	if e = s.ObserveMetering(ctx, usage); e != nil || !valid {
		c.AbortWithStatus(502)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/json", raw)
}
