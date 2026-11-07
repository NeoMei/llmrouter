package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func newManagedNativeRouter(adapter *platformbilling.Adapter) *gin.Engine {
	adapter.SetNativeDispatchAuthorizer(func(ctx context.Context, grant platformbilling.Claims, name string, quote platformbilling.NativeQuote) error {
		return authorizeNativeDispatch(ctx, adapter, grant, name, quote)
	})
	r := gin.New()
	r.Use(gin.Recovery(), middleware.BodyStorageCleanup(), middleware.RouteTag("relay"))
	for _, entry := range []struct {
		path, target, wire string
		format             types.RelayFormat
	}{
		{"/platform/v1/chat/completions", "/v1/chat/completions", "chat.completions.create", types.RelayFormatOpenAI},
		{"/platform/v1/messages", "/v1/messages", "claude.messages.create", types.RelayFormatClaude},
		{"/platform/v1/models/*native", "", "gemini.generate", types.RelayFormatGemini},
		{"/platform/v1beta/models/*native", "", "gemini.generate", types.RelayFormatGemini},
		{"/platform/v1/responses", "/v1/responses", "responses.create", types.RelayFormatOpenAIResponses},
		{"/platform/v1/responses/compact", "/v1/responses/compact", "responses.compact", types.RelayFormatOpenAIResponsesCompaction},
		{"/platform/v1/embeddings", "/v1/embeddings", "embeddings.create", types.RelayFormatEmbedding},
		{"/platform/v1/rerank", "/v1/rerank", "rerank.create", types.RelayFormatRerank},
		{"/platform/v1/multimodal/embeddings", "/v1/multimodal/embeddings", "multimodal.embeddings.create", types.RelayFormatMultimodalEmbedding},
		{"/platform/v1/images/generations", "/v1/images/generations", "images.generations.create", types.RelayFormatOpenAIImage},
		{"/platform/v1/images/edits", "/v1/images/edits", "images.edits.create", types.RelayFormatOpenAIImage},
		{"/platform/v1/audio/speech", "/v1/audio/speech", "audio.speech.create", types.RelayFormatOpenAIAudio},
		{"/platform/v1/audio/transcriptions", "/v1/audio/transcriptions", "audio.transcriptions.create", types.RelayFormatOpenAIAudio},
		{"/platform/v1/ocr/jobs", "/v1/ocr", "ocr.jobs.create", types.RelayFormat("managed_ocr")},
		{"/platform/api/v2/ocr/jobs", "/api/v2/ocr/jobs", "ocr.jobs.create", types.RelayFormat("managed_ocr")},
	} {
		registerManagedNativeOperation(r, adapter, entry.path, entry.target, entry.wire, entry.format)
	}
	registerManagedNativeImage(r, adapter)
	registerManagedNativeSpeech(r, adapter)
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "native_operation_unavailable"}})
	})
	return r
}

func registerManagedNativeOperation(r *gin.Engine, adapter *platformbilling.Adapter, path, target, wire string, format types.RelayFormat) {
	r.POST(path, func(c *gin.Context) {
		requestTarget := target
		requestQuery := ""
		operation := platformbilling.WireOperation{ID: wire}
		if wire == "gemini.generate" {
			parts := strings.Split(strings.TrimPrefix(c.Param("native"), "/"), ":")
			if len(parts) != 2 || parts[0] == "" || strings.ContainsAny(parts[0], "/:?#%") || (parts[1] != "generateContent" && parts[1] != "streamGenerateContent") {
				c.AbortWithStatus(400)
				return
			}
			operation.PathModel, operation.Mode = parts[0], parts[1]
			requestTarget = "/v1beta/models/" + parts[0] + ":" + parts[1]
			query := c.Request.URL.Query()
			if len(query) > 1 || (len(query) == 1 && (parts[1] != "streamGenerateContent" || len(query["alt"]) != 1 || query.Get("alt") != "sse")) {
				c.AbortWithStatus(400)
				return
			}
			requestQuery = c.Request.URL.RawQuery
		}
		credential := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, (64<<20)+1))
		if err != nil || len(body) > 64<<20 {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		ct, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil {
			c.AbortWithStatus(400)
			return
		}
		var name string
		if ct == "application/json" {
			var in struct {
				Model              string          `json:"model"`
				PreviousResponseID string          `json:"previous_response_id"`
				Store              *bool           `json:"store"`
				Conversation       json.RawMessage `json:"conversation"`
				CachedContent      json.RawMessage `json:"cachedContent"`
				Container          json.RawMessage `json:"container"`
			}
			if common.UnmarshalUnique(body, &in) != nil {
				c.AbortWithStatus(400)
				return
			}
			name = in.Model
			if operation.PathModel != "" {
				if name != "" && name != operation.PathModel {
					c.AbortWithStatus(400)
					return
				}
				name = operation.PathModel
			}
			// Shared routing principals never authorize provider-private history.
			// Ordinary responses are stateless; stored continuations require the
			// separately scoped continuity owner before they can be exposed.
			if validateManagedNativeResources(body, wire) != nil || in.PreviousResponseID != "" || (in.Store != nil && *in.Store) || nativePrivateReference(in.Conversation) || nativePrivateReference(in.CachedContent) || nativePrivateReference(in.Container) {
				c.AbortWithStatusJSON(400, gin.H{"error": gin.H{"code": "managed_native_history_unavailable"}})
				return
			}
			if wire == "responses.create" {
				var fields map[string]json.RawMessage
				if common.UnmarshalUnique(body, &fields) != nil {
					c.AbortWithStatus(400)
					return
				}
				fields["store"] = json.RawMessage("false")
				body, err = common.Marshal(fields)
				if err != nil {
					c.AbortWithStatus(400)
					return
				}
			}
		} else if ct == "multipart/form-data" {
			reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				part, e := reader.NextPart()
				if e == io.EOF {
					break
				}
				if e != nil {
					c.AbortWithStatus(400)
					return
				}
				if part.FormName() == "model" && part.FileName() == "" {
					value, e := io.ReadAll(io.LimitReader(part, 256))
					if e != nil || name != "" || len(value) > 255 {
						part.Close()
						c.AbortWithStatus(400)
						return
					}
					name = string(value)
				}
				part.Close()
			}
		} else {
			c.AbortWithStatus(415)
			return
		}
		if wire == "chat.completions.create" || wire == "claude.messages.create" || wire == "responses.create" || wire == "gemini.generate" {
			op := "chat"
			if wire == "claude.messages.create" {
				op = wire
			}
			if wire == "responses.create" {
				op = "responses"
			}
			if wire == "gemini.generate" {
				op = wire
			}
			body, err = adapter.ManagedRequestBody(op, body)
			if err != nil {
				c.AbortWithStatus(400)
				return
			}
		}
		if target == "/api/v2/ocr/jobs" {
			operation.Mode = "paddle-native-v1"
		}
		s, err := adapter.AdmitNative(c.Request.Context(), credential, c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), operation, name, body)
		if err != nil {
			c.AbortWithStatusJSON(platformbilling.HTTPStatus(err), gin.H{"error": gin.H{"code": unifiedAdmissionCode(platformbilling.HTTPStatus(err))}})
			return
		}
		id, group := adapter.NativePrincipal()
		common.SetContextKey(c, constant.ContextKeyUserId, id)
		common.SetContextKey(c, constant.ContextKeyUserGroup, group)
		common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		c.Set("platform_billing", s)
		c.Header("X-UniHub-Call-ID", s.CallID())
		c.Request.URL.Path, c.Request.URL.RawPath, c.Request.URL.RawQuery = requestTarget, "", requestQuery
		c.Request.Body, c.Request.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		c.Request.Header = http.Header{"Content-Type": []string{mime.FormatMediaType(ct, params)}}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := s.Finish(ctx); err != nil {
				common.SysError("managed native terminal decision pending")
			}
		}()
		c.Next()
	}, middleware.DistributedRateLimit(), middleware.ModelRequestRateLimit(), func(c *gin.Context) {
		if format != types.RelayFormat("managed_ocr") {
			middleware.Distribute()(c)
		} else {
			c.Next()
		}
	}, func(c *gin.Context) {
		if format == types.RelayFormat("managed_ocr") {
			controller.OCRBodyLimit(c)
		} else {
			controller.Relay(c, format)
		}
	}, func(c *gin.Context) {
		if format == types.RelayFormat("managed_ocr") {
			(&controller.OCRHandler{Runtime: controller.OCRRuntime}).Create(c)
		}
	})
}

func nativePrivateReference(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null" && value != `""`
}
