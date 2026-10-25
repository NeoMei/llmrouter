package router

import (
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func SetUniHubRouter(r *gin.Engine, c platformaccount.Config) {
	r.GET("/api/unihub/config", func(ctx *gin.Context) { ctx.Header("Cache-Control", "no-store"); ctx.JSON(http.StatusOK, c) })
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
}
