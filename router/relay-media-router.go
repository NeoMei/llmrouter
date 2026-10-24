package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/gin-gonic/gin"
)

func SetRelayMediaRouter(router *gin.Engine, h *controller.RelayMediaHandler) {
	router.GET("/api/relay-media/resources/:id", h.Resource)
	router.HEAD("/api/relay-media/resources/:id", h.Resource)
}
