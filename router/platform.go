package router

import (
	"bytes"
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
	"time"
)

// SetPlatformRouter takes an explicit adapter. Disabled deployments register no
// platform endpoint and keep the legacy router untouched.
func SetPlatformRouter(engine *gin.Engine, adapter *platformbilling.Adapter) {
	service.PlatformTaskObserver = adapter.ObserveTask
	service.PlatformTaskSettlementCheck = adapter.VerifyTaskSettlement
	service.PlatformTaskRelease = adapter.ReleaseTask
	service.PlatformTaskBind = adapter.BindTask
	if runtime := controller.RelayMediaRuntime; runtime != nil && runtime.Service != nil {
		principal, _ := adapter.Principal()
		owner := runtime.Service
		service.PlatformVideoMedia = func(ctx context.Context, sourceURL, callID string, width, height int) (service.PlatformVideoArtifact, error) {
			prepared, err := owner.PrepareMP4WithResolution(ctx, relaymedia.PrepareInput{
				UserID:    principal,
				TokenID:   principal,
				RequestID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("platform-video:"+callID)).String(),
				SourceURL: sourceURL,
			}, width, height)
			if err != nil {
				return service.PlatformVideoArtifact{}, err
			}
			return service.PlatformVideoArtifact{ID: prepared.ID, SHA256: prepared.SHA256, ContentType: prepared.ContentType, Size: prepared.Size, Width: prepared.Facts.Width, Height: prepared.Facts.Height, Seconds: prepared.Facts.Seconds}, nil
		}
	} else {
		service.PlatformVideoMedia = nil
	}
	if !adapter.Enabled() {
		return
	}
	installNativeHistoryResolver(adapter)
	platform := engine.Group("/platform/v1", middleware.RouteTag("relay"), func(c *gin.Context) {
		policy, err := unihubaccess.FromEnv()
		if err != nil || !policy.PlatformEnabled {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "platform_billing_unavailable"}})
			return
		}
		c.Next()
	})
	platform.GET("/calls/:callID", func(c *gin.Context) {
		if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || !platformbilling.IsModelCredential(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")) || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
			c.AbortWithStatus(401)
			return
		}
		receipt, err := adapter.Receipt(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.Param("callID"))
		if err != nil {
			c.JSON(platformbilling.HTTPStatus(err), gin.H{"error": gin.H{"code": "platform_receipt_unavailable"}})
			return
		}
		c.JSON(200, receipt)
	})
	registerPlatformOperation(platform, adapter, "/videos", "video", "/v1/videos", types.RelayFormatTask)
	registerPlatformOperation(platform, adapter, "/ocr/jobs", "ocr", "/v1/ocr", types.RelayFormat("platform_ocr"))
	registerPlatformTaskReads(platform, adapter)
	registerPlatformOperation(engine, adapter, "/platform/api/v2/ocr/jobs", "ocr", "/api/v2/ocr/jobs", types.RelayFormat("platform_ocr"))
	registerNativeOCRRead(engine, adapter)
	registerPlatformOperation(platform, adapter, "/native/dashscope/ocr/recognitions", "dashscope.ocr.recognitions.create", "/v1/native/dashscope/ocr/recognitions", types.RelayFormat("native_ocr"))
	registerPlatformOperation(platform, adapter, "/messages", "claude.messages.create", "/v1/messages", types.RelayFormatClaude)
	registerPlatformOperation(platform, adapter, "/models/*native", "gemini.generate", "", types.RelayFormatGemini)
	registerPlatformOperation(engine, adapter, "/platform/v1beta/models/*native", "gemini.generate", "", types.RelayFormatGemini)
	registerPlatformOperation(platform, adapter, "/responses/compact", "responses.compact", "/v1/responses/compact", types.RelayFormatOpenAIResponsesCompaction)
	registerPlatformOperation(platform, adapter, "/responses", "responses", "/v1/responses", types.RelayFormatOpenAIResponses)
	registerPlatformOperation(platform, adapter, "/multimodal/embeddings", "multimodal.embeddings.create", "/v1/multimodal/embeddings", types.RelayFormatMultimodalEmbedding)
	registerPlatformOperation(engine, adapter, "/platform/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", "multimodal.embeddings.create", "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", types.RelayFormatMultimodalEmbedding)
	registerPlatformOperation(platform, adapter, "/rerank", "rerank.create", "/v1/rerank", types.RelayFormatRerank)
	registerPlatformOperation(engine, adapter, "/platform/api/v1/services/rerank/text-rerank/text-rerank", "rerank.create", "/api/v1/services/rerank/text-rerank/text-rerank", types.RelayFormatRerank)
	registerPlatformOperation(platform, adapter, "/embeddings", "embedding", "/v1/embeddings", types.RelayFormatEmbedding)
	registerPlatformOperation(platform, adapter, "/native/bailian/audio/speech", "bailian.audio.speech.create", "/v1/native/bailian/audio/speech", types.RelayFormat("native_speech"))
	registerPlatformOperation(platform, adapter, "/native/elevenlabs/audio/speech", "elevenlabs.audio.speech.create", "/v1/native/elevenlabs/audio/speech", types.RelayFormat("native_speech"))
	registerPlatformOperation(platform, adapter, "/audio/speech", "speech", "/v1/audio/speech", types.RelayFormatOpenAIAudio)
	registerPlatformOperation(platform, adapter, "/audio/transcriptions", "transcription", "/v1/audio/transcriptions", types.RelayFormatOpenAIAudio)
	registerPlatformOperation(platform, adapter, "/native/dashscope/audio/transcriptions", "dashscope.audio.transcriptions.create", "/v1/native/dashscope/audio/transcriptions", types.RelayFormat("native_qwen_asr"))
	registerPlatformOperation(platform, adapter, "/images/generations", "images.generations.create", "/v1/images/generations", types.RelayFormat("native_image"))
	registerPlatformOperation(platform, adapter, "/native/dashscope/images/generations", "dashscope.images.generate", "/v1/native/dashscope/images/generations", types.RelayFormat("native_image"))
	registerPlatformOperation(platform, adapter, "/native/gemini/images/models/*native", "gemini.images.generate", "", types.RelayFormat("native_image"))
	registerPlatformOperation(platform, adapter, "/images/edits", "images.edits.create", "/v1/images/edits", types.RelayFormat("native_image"))
	platform.GET("/images/edits/:callID", func(c *gin.Context) { readWanImage(c, adapter) })
	platform.POST("/chat/completions", func(c *gin.Context) {
		if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || !platformbilling.IsModelCredential(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")) {
			c.AbortWithStatusJSON(401, gin.H{"error": gin.H{"code": "invalid_model_credential"}})
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, adapter.BodyLimit()+1))
		if err != nil || int64(len(body)) > adapter.BodyLimit() {
			c.AbortWithStatusJSON(413, gin.H{"error": gin.H{"code": "request_too_large"}})
			return
		}
		if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer uh_key_") {
			body, err = adapter.ManagedRequestBody("chat", body)
			if err != nil {
				c.AbortWithStatusJSON(400, gin.H{"error": gin.H{"code": "invalid_model_request"}})
				return
			}
		}
		if len(c.Request.Header.Values("Idempotency-Key")) != 1 || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
			c.AbortWithStatusJSON(400, gin.H{"error": gin.H{"code": "stable_call_and_task_required"}})
			return
		}
		session, err := adapter.Admit(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), body)
		if err != nil {
			status := platformbilling.HTTPStatus(err)
			code := "platform_billing_unavailable"
			if errors.Is(err, platformbilling.ErrDuplicate) {
				code = "logical_call_already_exists"
			}
			if errors.Is(err, platformbilling.ErrConflict) {
				code = "logical_call_conflict"
			}
			if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer uh_key_") && status != http.StatusConflict {
				code = unifiedAdmissionCode(status)
			}
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code}})
			return
		}
		c.Set("platform_billing", session)
		c.Header("X-UniHub-Call-ID", c.GetHeader("Idempotency-Key"))
		id, group := adapter.Principal()
		common.SetContextKey(c, constant.ContextKeyUserId, id)
		common.SetContextKey(c, constant.ContextKeyUserGroup, group)
		common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		// All routing identity is server configuration. Incoming headers can neither
		// select a local token/principal/channel nor escape the platform credential.
		c.Request.Header = http.Header{"Content-Type": []string{"application/json"}}
		c.Request.URL.Path = "/v1/chat/completions"
		c.Request.URL.RawPath = ""
		c.Request.URL.RawQuery = ""
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := session.Finish(ctx); err != nil {
				common.SysError("platform terminal intent persistence failed; call requires reconciliation")
			}
		}()
		c.Next()
	}, middleware.DistributedRateLimit(), middleware.ModelRequestRateLimit(), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, types.RelayFormatOpenAI) })
}

// registerPlatformOperation exposes only operations with a matching metered
// tariff. Admission parses and canonicalizes the complete request before any
// provider relay is entered; unsupported capability returns 400/503 instead of
// falling back to local wallet billing.
func registerPlatformOperation(engine gin.IRoutes, adapter *platformbilling.Adapter, path, operation, target string, format types.RelayFormat) {
	engine.POST(path, middleware.RouteTag("relay"), func(c *gin.Context) {
		policy, policyErr := unihubaccess.FromEnv()
		if policyErr != nil || !policy.PlatformEnabled {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}

		if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || !platformbilling.IsModelCredential(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")) || len(c.Request.Header.Values("Idempotency-Key")) != 1 || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "invalid_platform_scope"}})
			return
		}
		wire := platformbilling.WireOperation{}
		billingOperation := operation
		if operation == "dashscope.ocr.recognitions.create" {
			wire.ID = operation
			billingOperation = "ocr"
			if c.Request.URL.RawQuery != "" {
				c.AbortWithStatus(400)
				return
			}
		}
		if operation == "ocr" {
			wire.ID = "ocr.jobs.create"
			if target == "/api/v2/ocr/jobs" {
				wire.Mode = "paddle-native-v1"
			}
			if c.Request.URL.RawQuery != "" {
				c.AbortWithStatus(400)
				return
			}
		}
		requestTarget := target
		if platformbilling.IsImageWire(operation) {
			wire.ID = operation
			billingOperation = "image_generation"
			if operation == "images.edits.create" {
				billingOperation = "image_edit"
			}
			if c.Request.URL.RawQuery != "" {
				c.AbortWithStatus(400)
				return
			}
			if operation == "gemini.images.generate" {
				parts := strings.Split(strings.TrimPrefix(c.Param("native"), "/"), ":")
				if len(parts) != 2 || parts[1] != "generateContent" || parts[0] == "" || strings.ContainsAny(parts[0], "/:?#%") {
					c.AbortWithStatus(400)
					return
				}
				wire.PathModel = parts[0]
				wire.Mode = parts[1]
			}
		}
		if operation == "bailian.audio.speech.create" || operation == "elevenlabs.audio.speech.create" {
			wire.ID = operation
			billingOperation = "speech"
		}
		if operation == "dashscope.audio.transcriptions.create" {
			wire.ID = operation
			billingOperation = "transcription"
		}
		audioWire := wire.ID
		if operation == "speech" {
			audioWire = "audio.speech.create"
		}
		if operation == "transcription" {
			audioWire = "audio.transcriptions.create"
		}
		if operation == "dashscope.audio.transcriptions.create" {
			audioWire = operation
		}
		if platformbilling.IsAudioWire(audioWire) && c.Request.URL.RawQuery != "" {
			c.AbortWithStatus(400)
			return
		}
		if operation == "rerank.create" || operation == "multimodal.embeddings.create" {
			wire.ID = operation
			billingOperation = "rerank"
			if operation == "multimodal.embeddings.create" {
				billingOperation = "multimodal_embedding"
			}
			if strings.HasPrefix(target, "/api/") {
				wire.Mode = "knowledge-native-v1"
			}
			if c.Request.URL.RawQuery != "" {
				c.AbortWithStatus(400)
				return
			}
		}
		if operation == "responses.compact" {
			wire.ID = operation
			billingOperation = "responses"
			if c.Request.URL.RawQuery != "" {
				c.AbortWithStatus(400)
				return
			}
		}
		if operation == "claude.messages.create" {
			wire.ID = operation
			billingOperation = "chat"
		}
		if operation == "gemini.generate" {
			wire.ID = operation
			billingOperation = "chat"
			parts := strings.Split(strings.TrimPrefix(c.Param("native"), "/"), ":")
			if len(parts) != 2 || (parts[1] != "generateContent" && parts[1] != "streamGenerateContent") || strings.ContainsAny(parts[0], "/:?#%") || parts[0] == "" {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			wire.PathModel, wire.Mode = parts[0], parts[1]
			requestTarget = "/v1beta/models/" + wire.PathModel + ":" + wire.Mode
			q := c.Request.URL.Query()
			if len(q) > 1 || (len(q) == 1 && (wire.Mode != "streamGenerateContent" || len(q["alt"]) != 1 || q.Get("alt") != "sse")) {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
		}
		limit := adapter.OperationBodyLimit(billingOperation)
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
		if err != nil || int64(len(body)) > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{"code": "request_too_large"}})
			return
		}
		ct := platformbilling.ValidatedContentType(c.GetHeader("Content-Type"))
		if ct == "" {
			c.AbortWithStatus(http.StatusUnsupportedMediaType)
			return
		}
		if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer uh_key_") {
			body, err = adapter.ManagedRequestBody(operation, body)
			if err != nil {
				c.AbortWithStatusJSON(400, gin.H{"error": gin.H{"code": "invalid_model_request"}})
				return
			}
		}
		if wire.ID == "ocr.jobs.create" || wire.ID == "dashscope.ocr.recognitions.create" {
			if exists, e := adapter.OCRReplay(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), wire, ct, body); exists || e != nil {
				c.AbortWithStatus(platformbilling.HTTPStatus(e))
				return
			}
		}
		if operation == "embedding" || operation == "rerank.create" || operation == "multimodal.embeddings.create" || operation == "dashscope.ocr.recognitions.create" {
			var raw map[string]any
			if common.UnmarshalUnique(body, &raw) != nil {
				c.AbortWithStatus(400)
				return
			}
			modelID, _ := raw["model"].(string)
			catalog, e := readTechnicalCatalog(c.Request.Context(), adapter, "superwagie")
			available := false
			if e == nil {
				for _, m := range catalog.Models {
					if m.ID == modelID {
						for _, op := range m.Operations {
							if (op.ID == operation || (operation == "embedding" && op.ID == "embeddings.create")) && op.Available {
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
		}
		if platformbilling.IsImageWire(wire.ID) {
			if exists, e := adapter.ImageReplay(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), wire, ct, body); exists || e != nil {
				c.AbortWithStatus(platformbilling.HTTPStatus(e))
				return
			}
			modelID, e := adapter.ImageRequestModel(wire, ct, body)
			if e != nil {
				c.AbortWithStatus(400)
				return
			}
			catalog, e := readTechnicalCatalog(c.Request.Context(), adapter, "superwagie")
			available := false
			if e == nil {
				for _, m := range catalog.Models {
					if m.ID == modelID {
						for _, op := range m.Operations {
							available = available || (op.ID == wire.ID && op.Available)
						}
					}
				}
			}
			if !available {
				c.AbortWithStatus(503)
				return
			}
		}
		if platformbilling.IsAudioWire(audioWire) {
			if exists, e := adapter.AudioReplay(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), audioWire, ct, body); exists || e != nil {
				c.AbortWithStatus(platformbilling.HTTPStatus(e))
				return
			}
			modelID, e := adapter.AudioRequestModel(audioWire, ct, body)
			if e != nil {
				c.AbortWithStatus(400)
				return
			}
			catalog, e := readTechnicalCatalog(c.Request.Context(), adapter, "superwagie")
			available := false
			if e == nil {
				for _, m := range catalog.Models {
					if m.ID == modelID {
						for _, op := range m.Operations {
							if op.ID == audioWire && op.Available {
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
		}
		defer func() {
			if cleanup, ok := c.Get("internal.knowledge_media_cleanup"); ok {
				cleanup.(func())()
			}
		}()
		if operation == "ocr" {
			modelID, e := controller.ValidateManagedOCRBody(c.Request.Context(), ct, body)
			if e != nil {
				c.AbortWithStatus(400)
				return
			}
			catalog, e := readTechnicalCatalog(c.Request.Context(), adapter, "superwagie")
			available := false
			if e == nil {
				for _, m := range catalog.Models {
					if m.ID != modelID {
						continue
					}
					for _, op := range m.Operations {
						if op.ID == "ocr.jobs.create" && op.Available {
							available = true
							break
						}
					}
				}
			}
			if !available {
				c.AbortWithStatus(503)
				return
			}
		}
		var session *platformbilling.Session
		if wire.ID == "multimodal.embeddings.create" {
			session, err = adapter.AdmitPreparedKnowledge(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), wire, ct, body, knowledgeMediaPreparation(c, adapter))
		} else if wire.ID != "" {
			session, err = adapter.AdmitWireOperation(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), wire, ct, body)
		} else {
			session, err = adapter.AdmitOperation(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), operation, ct, body)
		}
		if err != nil {
			status, code := platformbilling.HTTPStatus(err), "platform_admission_rejected"
			if errors.Is(err, platformbilling.ErrNativeHistory) {
				status = http.StatusBadRequest
				code = "native_history_unavailable"
			}
			if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer uh_key_") {
				code = unifiedAdmissionCode(status)
			}
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code}})
			return
		}
		id, group := adapter.Principal()
		common.SetContextKey(c, constant.ContextKeyUserId, id)
		common.SetContextKey(c, constant.ContextKeyUserGroup, group)
		common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		c.Set("platform_billing", session)
		c.Header("X-UniHub-Call-ID", c.GetHeader("Idempotency-Key"))
		if operation == "embedding" || operation == "rerank.create" || operation == "multimodal.embeddings.create" {
			body, err = session.KnowledgeBodyForRelay()
			if err != nil {
				c.AbortWithStatus(400)
				return
			}
			if operation == "rerank.create" && wire.Mode == "knowledge-native-v1" {
				common.SetContextKey(c, constant.ContextKeyAliNativeRerank, true)
			}
		}
		c.Request.URL.Path = requestTarget
		c.Request.URL.RawPath = ""
		c.Request.URL.RawQuery = ""
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Request.Header = http.Header{"Content-Type": []string{ct}}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := session.Finish(ctx); err != nil {
				common.SysError("platform terminal intent persistence failed; call requires reconciliation")
			}
		}()
		c.Next()
	}, middleware.DistributedRateLimit(), middleware.ModelRequestRateLimit(), func(c *gin.Context) {
		if format == types.RelayFormat("native_image") || format == types.RelayFormat("native_speech") || format == types.RelayFormat("native_ocr") || format == types.RelayFormat("native_qwen_asr") {
			c.Next()
			return
		}
		if operation == "ocr" {
			c.Next()
			return
		}
		middleware.Distribute()(c)
	}, func(c *gin.Context) {
		if format == types.RelayFormat("native_image") {
			relayNativeImage(c, adapter)
			return
		}
		if format == types.RelayFormat("native_ocr") {
			relayQwenOCR(c, adapter)
			return
		}
		if format == types.RelayFormat("native_speech") {
			relayNativeSpeech(c, adapter)
			return
		}
		if format == types.RelayFormat("native_qwen_asr") {
			relayNativeQwenASR(c, adapter)
			return
		}
		if operation == "video" {
			controller.RelayTask(c)
			return
		}
		if operation == "ocr" {
			controller.OCRBodyLimit(c)
			return
		}
		controller.Relay(c, format)
	}, func(c *gin.Context) {
		if operation == "ocr" {
			(&controller.OCRHandler{Runtime: controller.OCRRuntime}).Create(c)
		}
	})
}
