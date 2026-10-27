package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func SetUniHubRouter(r *gin.Engine, c platformaccount.Config) {
	r.GET("/api/unihub/config", func(ctx *gin.Context) {
		ctx.Header("Cache-Control", "no-store")
		policy, err := unihubaccess.FromEnv()
		nativeAccess := err == nil && (policy.IsPrivateManagementRequest(ctx.Request) || (!policy.PublicEnabled && !policy.PlatformEnabled && policy.NativePublicEnabled))
		ctx.JSON(http.StatusOK, struct {
			platformaccount.Config
			NativeAccess bool `json:"native_access"`
		}{Config: c, NativeAccess: nativeAccess})
	})
	r.POST("/api/unihub/identity", func(ctx *gin.Context) {
		ctx.Header("Cache-Control", "no-store")
		if !c.Enabled {
			ctx.Status(http.StatusNotFound)
			return
		}
		if ctx.Request.ContentLength != 0 || ctx.Request.URL.RawQuery != "" {
			ctx.JSON(400, gin.H{"error": "no_identity_overrides"})
			return
		}
		h := ctx.GetHeader("Authorization")
		if len(ctx.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(h, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")) == "" || len(h) > 16384 {
			ctx.Status(401)
			return
		}
		identity, e := c.Prove(ctx.Request.Context(), strings.TrimPrefix(h, "Bearer "))
		if e != nil {
			ctx.JSON(401, gin.H{"error": e.Error()})
			return
		}
		ctx.JSON(200, identity)
	})
	r.POST("/api/unihub/session", middleware.SessionCookieOriginGuard(), middleware.CriticalRateLimit(), func(ctx *gin.Context) {
		ctx.Header("Cache-Control", "no-store")
		if !c.Enabled || ctx.Request.ContentLength != 0 || ctx.Request.URL.RawQuery != "" || len(ctx.Request.Header.Values("Authorization")) != 1 {
			ctx.Status(http.StatusUnauthorized)
			return
		}
		h := ctx.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")) == "" || len(h) > 16384 {
			ctx.Status(http.StatusUnauthorized)
			return
		}
		identity, err := c.Prove(ctx.Request.Context(), strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			ctx.Status(http.StatusUnauthorized)
			return
		}
		user, err := model.ResolveUniHubProjection(identity)
		if err != nil || user.Status != 1 {
			ctx.Status(http.StatusUnauthorized)
			return
		}
		bundle, err := service.CreateLoginSession(user.Id, "unihub", ctx.ClientIP(), ctx.Request.UserAgent())
		if err != nil {
			ctx.Status(http.StatusUnauthorized)
			return
		}
		service.WriteRefreshCookie(ctx, bundle.RefreshToken)
		ctx.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"access_token": bundle.AccessToken, "token_type": bundle.TokenType, "access_expires_at": bundle.AccessExpiresAt, "user": controller.BuildSelfUserData(user), "session": bundle.Session}})
	})
}
