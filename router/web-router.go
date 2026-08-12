package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// WebAssets holds the embedded dashboard frontend assets.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
}

func SetWebRouter(router *gin.Engine, assets WebAssets) {
	frontendFS := common.EmbedFolder(assets.BuildFS, "web/dist")

	router.Use(gzip.Gzip(gzip.DefaultCompression))
	router.Use(middleware.GlobalWebRateLimit())
	router.Use(middleware.Cache())

	// Explicitly register the /install onboarding directory. Relay wildcard
	// routes such as /:mode/mj/... make gin's RedirectTrailingSlash believe a
	// sibling path exists, so /install/ would be 301-redirected to /install
	// before the static middleware runs. Registering the route keeps the
	// trailing-slash URL serving the onboarding index.html directly.
	installServer := http.FileServer(frontendFS)
	router.GET("/install", func(c *gin.Context) {
		installServer.ServeHTTP(c.Writer, c.Request)
	})
	router.GET("/install/*filepath", func(c *gin.Context) {
		installServer.ServeHTTP(c.Writer, c.Request)
	})

	router.Use(static.Serve("/", frontendFS))
	router.NoRoute(func(c *gin.Context) {
		c.Set(middleware.RouteTagKey, "web")
		if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/assets") {
			controller.RelayNotFound(c)
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", assets.IndexPage)
	})
}
