package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func registerGatewayRoutes(apiRouter *gin.RouterGroup) {
	byokGroup := apiRouter.Group("/gateway")
	byokGroup.Use(middleware.UserAuth())
	{
		byokGroup.GET("/providers", controller.ListProviderProfiles)
		byokGroup.GET("/models", controller.ListProviderModels)
		byokGroup.GET("/byok", controller.ListUserProviderKeys)
		byokGroup.POST("/byok", controller.AddUserProviderKey)
		byokGroup.DELETE("/byok/:id", controller.DeleteUserProviderKey)
	}

	platformGroup := apiRouter.Group("/gateway")
	platformGroup.Use(middleware.AdminAuth())
	{
		platformGroup.GET("/platform-key", controller.ListPlatformKeys)
		platformGroup.POST("/platform-key", controller.AddPlatformKey)
	}
}
