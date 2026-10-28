package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"reflect"
	"slices"
	"time"
)

func knowledgeMediaPreparation(c *gin.Context, a *platformbilling.Adapter) platformbilling.KnowledgePreparation {
	return func(ctx context.Context, raw []byte, p platformbilling.KnowledgePolicy) ([]byte, json.RawMessage, error) {
		r, e := helper.ParseMultimodalEmbeddingRequest(raw, false)
		if e != nil {
			return nil, nil, e
		}
		catalog, e := readTechnicalCatalog(ctx, a, "superwagie")
		if e != nil {
			return nil, nil, e
		}
		var selected *platformbilling.CatalogModel
		for i := range catalog.Models {
			if catalog.Models[i].ID == p.PublicModel {
				selected = &catalog.Models[i]
				break
			}
		}
		if selected == nil || !reflect.DeepEqual(selected.Capabilities.EmbeddingSpace, p.Space) {
			return nil, nil, fmt.Errorf("certified embedding space unavailable")
		}
		caps := selected.Capabilities
		if caps.MediaLimits == nil || caps.MediaLimits.MaxPixels == nil || caps.MediaLimits.MaxRequestBytes == nil || int64(len(raw)) > *caps.MediaLimits.MaxRequestBytes {
			return nil, nil, fmt.Errorf("certified media bounds unavailable")
		}
		for _, item := range r.Input {
			kind := "text"
			if item.Image != "" {
				kind = "image"
			}
			if item.Video != "" {
				kind = "video"
			}
			if !slices.Contains(caps.InputModalities, kind) {
				return nil, nil, fmt.Errorf("input modality not certified")
			}
		}
		prepared, e := helper.PrepareMultimodalImages(ctx, r, p.VisualTokens)
		if e != nil {
			return nil, nil, e
		}
		var videoIdentity any
		for i, item := range prepared.Contents {
			if item.Video == "" {
				continue
			}
			runtime := controller.RelayMediaRuntime
			if runtime == nil || runtime.Service == nil || caps.MediaLimits.MaxVideoSeconds == nil || *caps.MediaLimits.MaxVideoSeconds > 600 {
				return nil, nil, fmt.Errorf("certified video runtime unavailable")
			}
			user, _ := a.Principal()
			// This synthetic owner token is internal resource ownership, never native
			// wallet/token authorization. Resource serving requires its private grant.
			video, e := runtime.Service.PrepareMP4(ctx, relaymedia.PrepareInput{UserID: user, TokenID: user, RequestID: uuid.NewString(), SourceURL: item.Video})
			if e != nil {
				return nil, nil, e
			}
			c.Set("internal.knowledge_media_cleanup", func() {
				cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
				defer done()
				_ = runtime.Service.Revoke(cleanup, video.ID, user, user)
			})
			var stored model.RelayMediaResource
			if e = model.DB.WithContext(ctx).First(&stored, "id = ?", video.ID).Error; e != nil {
				return nil, nil, e
			}
			if stored.SHA256 == "" || video.Facts.Seconds > 600 || video.Facts.Seconds <= 0 {
				return nil, nil, fmt.Errorf("invalid frozen video identity")
			}
			videoIdentity = struct {
				ID, Digest string
				Facts      relaymedia.VideoFacts
			}{video.ID, stored.SHA256, video.Facts}
			prepared.Video = &video
			prepared.Contents[i].Video = video.URL
			deadline, cancel := context.WithDeadline(c.Request.Context(), video.AttemptDeadline)
			c.Request = c.Request.WithContext(deadline)
			previous, _ := c.Get("internal.knowledge_media_cleanup")
			c.Set("internal.knowledge_media_cleanup", func() { cancel(); previous.(func())() })
		}
		if e = helper.ValidatePreparedMultimodal(prepared, caps); e != nil {
			return nil, nil, e
		}
		r.Input = prepared.Contents
		effective, e := common.Marshal(r)
		if e != nil {
			return nil, nil, e
		}
		// The relay's existing admission guard measures the frozen body, not
		// the compact source-URL request. Check every representation before
		// reserving: canonical relay, native relay, and selected supplier body.
		effectiveLimit := min(*caps.MediaLimits.MaxRequestBytes, a.BodyLimit())
		if int64(len(effective)) > effectiveLimit {
			return nil, nil, fmt.Errorf("%w: frozen knowledge request exceeds byte limit", platformbilling.ErrInvalid)
		}
		native := dto.QwenMultimodalEmbeddingRequest{}
		native.Input.Contents = r.Input
		native.Parameters.Dimension = r.Dimensions
		native.Parameters.FPS = r.FPS
		native.Parameters.EnableFusion = r.EnableFusion
		for _, modelName := range []string{r.Model, p.UpstreamModel} {
			native.Model = modelName
			body, err := common.Marshal(native)
			if err != nil {
				return nil, nil, err
			}
			if int64(len(body)) > effectiveLimit {
				return nil, nil, fmt.Errorf("%w: frozen knowledge request exceeds byte limit", platformbilling.ErrInvalid)
			}
		}
		sum := sha256.Sum256(effective)
		facts, e := common.Marshal(struct {
			BodySHA256 string
			Images     []helper.MediaFact
			Video      any
		}{hex.EncodeToString(sum[:]), prepared.Images, videoIdentity})
		if e != nil {
			return nil, nil, e
		}
		c.Set(helper.MultimodalPreparedKey, prepared)
		return effective, facts, nil
	}
}
