package router

import (
	"bytes"
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
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
	if !adapter.Enabled() {
		return
	}
	engine.GET("/platform/v1/calls/:callID", middleware.RouteTag("relay"), func(c *gin.Context) {
		if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer uh_model_") || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
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
	engine.POST("/platform/v1/chat/completions", middleware.RouteTag("relay"), func(c *gin.Context) {
		if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer uh_model_") {
			c.AbortWithStatusJSON(401, gin.H{"error": gin.H{"code": "invalid_model_credential"}})
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, adapter.BodyLimit()+1))
		if err != nil || int64(len(body)) > adapter.BodyLimit() {
			c.AbortWithStatusJSON(413, gin.H{"error": gin.H{"code": "request_too_large"}})
			return
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
	}, middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, types.RelayFormatOpenAI) })
}
