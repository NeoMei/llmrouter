package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func SetRelayMediaRouter(router *gin.Engine, h *controller.RelayMediaHandler) {
	boundary := middleware.ProductResourceBoundary()
	// This immutable resource is independently capability-authenticated by
	// ReadGranted, including digest, expiry and revocation. Platform suppliers
	// require it even while native user/token endpoints remain disabled.

	router.GET("/api/relay-media/resources/:id", boundary, h.Resource)
	router.HEAD("/api/relay-media/resources/:id", boundary, h.Resource)
}
