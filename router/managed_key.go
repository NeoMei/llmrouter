package router

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var managedIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func unifiedAdmissionCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_unified_model_request"
	case http.StatusUnauthorized:
		return "invalid_model_credential"
	case http.StatusPaymentRequired:
		return "unified_budget_or_funds_exhausted"
	case http.StatusForbidden:
		return "unified_credential_forbidden"
	case http.StatusConflict:
		return "logical_call_already_exists"
	default:
		return "platform_billing_unavailable"
	}
}

// SetManagedKeyDispatch must run before native routes are registered. It reuses
// the platform handlers in process, so a unified key can never enter native
// authentication, local quota reservation, or an HTTP forwarding fallback.
func SetManagedKeyDispatch(engine *gin.Engine, adapter *platformbilling.Adapter) {
	platform := gin.New()
	if err := platform.SetTrustedProxies(nil); err != nil {
		panic(err)
	}
	platform.Use(gin.Recovery(), middleware.CORS(), middleware.DecompressRequestMiddleware(), middleware.BodyStorageCleanup(), middleware.StatsMiddleware(), middleware.SystemPerformanceCheck(), middleware.GlobalAPIRateLimit())
	platform.Use(func(c *gin.Context) {
		if requestID, ok := c.Request.Context().Value(common.RequestIdKey).(string); ok {
			c.Set(common.RequestIdKey, requestID)
		}
		c.Next()
	})
	modelsHandler := func(c *gin.Context) {
		models, err := adapter.ManagedModels(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if err != nil {
			c.AbortWithStatusJSON(platformbilling.HTTPStatus(err), gin.H{"error": gin.H{"code": "model_credential_rejected"}})
			return
		}
		if model := c.Param("model"); model != "" {
			if !slices.Contains(models, model) {
				c.AbortWithStatusJSON(404, gin.H{"error": gin.H{"code": "model_not_found"}})
				return
			}
			c.JSON(200, gin.H{"id": model, "object": "model", "created": 0, "owned_by": "unihub"})
			return
		}
		data := make([]gin.H, 0, len(models))
		for _, model := range models {
			data = append(data, gin.H{"id": model, "object": "model", "created": 0, "owned_by": "unihub"})
		}
		c.JSON(200, gin.H{"object": "list", "data": data})
	}
	platform.GET("/platform/v1/models", middleware.DistributedRateLimit(), modelsHandler)
	platform.GET("/platform/v1/models/:model", middleware.DistributedRateLimit(), modelsHandler)
	SetPlatformRouter(platform, adapter)
	engine.Use(func(c *gin.Context) {
		managed := false
		for _, value := range c.Request.Header.Values("Authorization") {
			for _, field := range strings.Fields(value) {
				if strings.HasPrefix(field, "uh_key_") {
					managed = true
				}
			}
		}
		if !managed {
			c.Next()
			return
		}
		values := c.Request.Header.Values("Authorization")
		fields := strings.Fields(c.GetHeader("Authorization"))
		if len(values) != 1 || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || !platformbilling.IsModelCredential(fields[1]) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "invalid_model_credential"}})
			return
		}
		policy, err := unihubaccess.FromEnv()
		if err != nil || !policy.PlatformEnabled || !adapter.Enabled() {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "platform_billing_unavailable"}})
			return
		}
		credential := fields[1]
		path := c.Request.URL.Path
		target := ""
		taskID := ""
		if c.Request.Method == http.MethodGet && (path == "/v1/models" || strings.HasPrefix(path, "/v1/models/")) {
			target = "/platform" + path
		}
		if c.Request.Method == http.MethodPost {
			switch path {
			case "/v1/chat/completions", "/v1/responses", "/v1/embeddings", "/v1/audio/speech", "/v1/audio/transcriptions", "/v1/images/generations", "/v1/images/edits", "/v1/videos":
				target = "/platform" + path
			case "/v1/ocr", "/api/v2/ocr/jobs":
				target = "/platform/v1/ocr/jobs"
			}
		}
		if c.Request.Method == http.MethodGet {
			var operation, id, suffix string
			switch {
			case strings.HasPrefix(path, "/v1/videos/"):
				operation, id = "video", strings.TrimPrefix(path, "/v1/videos/")
			case strings.HasPrefix(path, "/platform/v1/videos/"):
				operation, id = "video", strings.TrimPrefix(path, "/platform/v1/videos/")
			case strings.HasPrefix(path, "/v1/ocr/"):
				operation, id = "ocr", strings.TrimPrefix(path, "/v1/ocr/")
			case strings.HasPrefix(path, "/api/v2/ocr/jobs/"):
				operation, id = "ocr", strings.TrimPrefix(path, "/api/v2/ocr/jobs/")
			case strings.HasPrefix(path, "/platform/v1/ocr/jobs/"):
				operation, id = "ocr", strings.TrimPrefix(path, "/platform/v1/ocr/jobs/")
			case strings.HasPrefix(path, "/platform/v1/calls/"):
				operation, id = "receipt", strings.TrimPrefix(path, "/platform/v1/calls/")
			}
			if operation != "" {
				parts := strings.Split(id, "/")
				id = parts[0]
				if len(parts) == 2 && (parts[1] == "result" || parts[1] == "content") {
					suffix = "/result"
				} else if len(parts) != 1 {
					id = ""
				}
				row, err := adapter.ManagedTask(c.Request.Context(), credential, id)
				if err != nil || (operation != "receipt" && row.Operation != operation) || (operation == "receipt" && suffix != "") {
					c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "unified_task_not_found"}})
					return
				}
				taskID = row.TaskID
				switch operation {
				case "video":
					target = "/platform/v1/videos/" + row.CallID + suffix
				case "ocr":
					target = "/platform/v1/ocr/jobs/" + row.CallID + suffix
				case "receipt":
					target = "/platform/v1/calls/" + row.CallID
				}
			}
		}
		if target == "" {
			c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "unified_operation_unsupported", "message": "this operation is not available for unified API keys"}})
			return
		}
		clientKeys := c.Request.Header.Values("Idempotency-Key")
		if len(clientKeys) > 1 || (len(clientKeys) == 1 && !managedIdempotencyKey.MatchString(clientKeys[0])) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_idempotency_key"}})
			return
		}
		callID := "uk_" + uuid.NewString()
		if len(clientKeys) == 1 {
			sum := sha256.Sum256([]byte(credential + "\x00" + clientKeys[0]))
			callID = "uk_" + hex.EncodeToString(sum[:])
		}
		request := c.Request.Clone(c.Request.Context())
		request.URL.Path, request.URL.RawPath, request.URL.RawQuery = target, "", ""
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("Idempotency-Key", callID)
		if taskID == "" {
			taskID = "task_" + callID
		}
		request.Header.Set("X-UniHub-Task-ID", taskID)
		platform.ServeHTTP(c.Writer, request)
		c.Abort()
	})
}
