package router

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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

var imageProducerHTTPClient = func() *http.Client { return service.NewManagedImageHTTPClient() }

func relayNativeImage(c *gin.Context, a *platformbilling.Adapter) {
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
	p, e := s.ImagePolicy()
	if e != nil {
		c.AbortWithStatus(503)
		return
	}
	var channel model.Channel
	if model.DB.WithContext(c.Request.Context()).First(&channel, p.ChannelID).Error != nil || channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.IsMultiKey || !a.ImageTarget(p.PublicModel, p.WireOperation, channel.Id, channel.Type, p.UpstreamModel, channel.Key, channel.GetBaseURL()) {
		c.AbortWithStatus(503)
		return
	}
	catalog, e := readTechnicalCatalog(c.Request.Context(), a, "superwagie")
	available := false
	if e == nil {
		for _, m := range catalog.Models {
			if m.ID == p.PublicModel {
				for _, op := range m.Operations {
					available = available || (op.ID == p.WireOperation && op.Available)
				}
			}
		}
	}
	if !available {
		c.AbortWithStatus(503)
		return
	}
	body, ct, path, e := s.ImageBody()
	if e != nil {
		c.AbortWithStatus(400)
		return
	}
	client := imageProducerHTTPClient()
	if client == nil {
		c.AbortWithStatus(503)
		return
	}
	closed := *client
	closed.Jar = nil
	closed.Timeout = 120 * time.Second
	closed.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, e := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, p.Origin+path, bytes.NewReader(body))
	if e != nil {
		c.AbortWithStatus(503)
		return
	}
	request.Header.Set("Content-Type", ct)
	if p.ChannelType == constant.ChannelTypeAli && p.WireOperation == "images.edits.create" {
		request.Header.Set("X-DashScope-Async", "enable")
	}
	if p.ChannelType == constant.ChannelTypeGemini {
		request.Header.Set("x-goog-api-key", channel.Key)
	} else {
		request.Header.Set("Authorization", "Bearer "+channel.Key)
	}
	if e = s.Dispatch(c.Request.Context(), channel.Id); e != nil {
		c.AbortWithStatus(409)
		return
	}
	if p.ChannelType == constant.ChannelTypeAli && p.WireOperation == "images.edits.create" {
		// Once dispatch is journaled, a disconnected client must not prevent
		// us from receiving and binding the paid supplier task ID.
		dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 120*time.Second)
		defer cancel()
		request = request.WithContext(dispatchCtx)
	}
	response, e := closed.Do(request)
	if e != nil {
		c.AbortWithStatus(502)
		return
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, p.MaxBodyBytes+1))
	if p.ChannelType == constant.ChannelTypeAli && readErr == nil && len(raw) <= 8192 && utf8.Valid(raw) && c.Request.Context().Err() == nil {
		failure, _ := accountpool.AliFailure(response.StatusCode, response.Header.Get("Retry-After"), raw)
		var envelope map[string]json.RawMessage
		var requestID, message string
		if failure.Refusal == accountpool.RefusalInvalidCredential && common.UnmarshalUnique(raw, &envelope) == nil && common.UnmarshalUnique(envelope["request_id"], &requestID) == nil && requestID != "" && len(requestID) <= 256 && common.UnmarshalUnique(envelope["message"], &message) == nil && message != "" && envelope["output"] == nil && envelope["usage"] == nil {
			s.Rejected()
			c.AbortWithStatus(502)
			return
		}
	}
	if readErr != nil || int64(len(raw)) > p.MaxBodyBytes || response.StatusCode != 200 {
		c.AbortWithStatus(502)
		return
	}
	if p.ChannelType == constant.ChannelTypeAli && p.WireOperation == "images.edits.create" {
		providerID, err := wanImageSubmission(raw)
		if err != nil {
			c.AbortWithStatus(502)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
		defer cancel()
		if err := s.BindTask(ctx, providerID); err != nil {
			c.AbortWithStatus(502)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusAccepted, gin.H{"id": s.CallID(), "status": "pending"})
		return
	}
	if c.Request.Context().Err() != nil {
		c.AbortWithStatus(502)
		return
	}
	// Resources use the same DNS/SSRF-protected client but never the producer's
	// authentication headers. The parser checks each URL against the certificate.
	result, e := platformbilling.ObserveImageResult(p, raw, func(location string) ([]byte, string, error) {
		req, e := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, location, nil)
		if e != nil {
			return nil, "", e
		}
		res, e := closed.Do(req)
		if e != nil {
			return nil, "", e
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return nil, "", platformbilling.ErrInvalid
		}
		data, e := io.ReadAll(io.LimitReader(res.Body, int64(p.MaxImageBytes)+1))
		if e != nil || len(data) > p.MaxImageBytes {
			return nil, "", platformbilling.ErrInvalid
		}
		return data, res.Header.Get("Content-Type"), nil
	})
	if e != nil || c.Request.Context().Err() != nil {
		c.AbortWithStatus(502)
		return
	}
	// Confirmed production is persisted before delivery. A broken downstream
	// connection cannot erase a known supplier production or trigger recreation.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
	defer cancel()
	if e = s.ObserveImages(ctx, result); e != nil {
		c.AbortWithStatus(502)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/json", result.Body)
}
