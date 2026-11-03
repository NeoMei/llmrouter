package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func SetOCRRouter(router *gin.Engine, h *controller.OCRHandler) {
	routes := router.Group("")
	routes.Use(middleware.RouteTag("relay"), middleware.CORS(), controller.OCRBodyLimit)
	// Only job/resource-scoped grants bypass platform bearer authentication.
	routes.GET("/api/ocr/jobs/:id/resources/:resourceID", middleware.ProductResourceBoundary(), h.Resource)
	owned := routes.Group("")
	owned.Use(middleware.ProductRelayEntry(), middleware.TokenAuth(), middleware.ProductRelayBoundary(), middleware.DistributedRateLimit(), middleware.ModelRequestRateLimit())
	owned.POST("/api/v2/ocr/jobs", h.Create)
	owned.POST("/v1/ocr", h.Create)
	reads := routes.Group("")
	reads.Use(middleware.ProductRelayEntry(), middleware.TokenAuthOwnedJobRead(), middleware.ProductRelayBoundary(), middleware.DistributedRateLimit(), middleware.ModelRequestRateLimit())
	reads.GET("/api/v2/ocr/jobs/:id", h.Status)
	reads.GET("/v1/ocr/:id", h.Status)
}
