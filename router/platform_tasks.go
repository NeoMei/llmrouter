package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func registerPlatformTaskReads(engine gin.IRoutes, a *platformbilling.Adapter) {
	for _, op := range []string{"video", "ocr"} {
		path := "/videos/:callID"
		if op == "ocr" {
			path = "/ocr/jobs/:callID"
		}
		authorize := func(c *gin.Context) {
			if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer uh_model_") || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
				c.AbortWithStatus(401)
				return
			}
			row, err := a.Task(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.Param("callID"))
			if err != nil || row.Operation != op {
				c.AbortWithStatus(404)
				return
			}
			c.Set("platform_task_call", row)
			c.Next()
		}
		engine.GET(path, authorize, controller.PlatformTaskStatus)
		engine.GET(path+"/result", authorize, controller.PlatformTaskResult)
		engine.POST(path+"/cancel", authorize, func(c *gin.Context) {
			c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "provider_cancellation_unavailable"}})
		})
	}
}
