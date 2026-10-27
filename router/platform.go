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
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strings"
	"time"
)

// SetPlatformRouter takes an explicit adapter. Disabled deployments register no
// platform endpoint and keep the legacy router untouched.
func SetPlatformRouter(engine *gin.Engine, adapter *platformbilling.Adapter) {
	service.PlatformTaskObserver = adapter.ObserveTask
	service.PlatformTaskRelease = adapter.ReleaseTask
	service.PlatformTaskBind = adapter.BindTask
	if !adapter.Enabled() {
		return
	}
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
	registerPlatformOperation(platform, adapter, "/responses", "responses", "/v1/responses", types.RelayFormatOpenAIResponses)
	registerPlatformOperation(platform, adapter, "/embeddings", "embedding", "/v1/embeddings", types.RelayFormatEmbedding)
	registerPlatformOperation(platform, adapter, "/audio/speech", "speech", "/v1/audio/speech", types.RelayFormatOpenAIAudio)
	registerPlatformOperation(platform, adapter, "/audio/transcriptions", "transcription", "/v1/audio/transcriptions", types.RelayFormatOpenAIAudio)
	registerPlatformOperation(platform, adapter, "/images/generations", "image_generation", "/v1/images/generations", types.RelayFormatOpenAIImage)
	registerPlatformOperation(platform, adapter, "/images/edits", "image_edit", "/v1/images/edits", types.RelayFormatOpenAIImage)
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
		if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || !platformbilling.IsModelCredential(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")) || len(c.Request.Header.Values("Idempotency-Key")) != 1 || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "invalid_platform_scope"}})
			return
		}
		limit := adapter.OperationBodyLimit(operation)
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
		session, err := adapter.AdmitOperation(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), operation, ct, body)
		if err != nil {
			status, code := platformbilling.HTTPStatus(err), "platform_admission_rejected"
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
		c.Request.URL.Path = target
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
		if operation == "ocr" {
			c.Next()
			return
		}
		middleware.Distribute()(c)
	}, func(c *gin.Context) {
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
