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
			token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			if c.Request.Method == http.MethodGet && len(c.Request.Header.Values("Authorization")) == 1 && strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") && c.Request.URL.RawQuery == "" && platformbilling.IsOwnedReadCredential(token) {
				row, err := a.OwnedTaskRead(c.Request.Context(), token, c.Param("callID"))
				if err != nil || row.Operation != op {
					c.AbortWithStatus(404)
					return
				}
				if op == "ocr" && recoverOwnedOCRRead(c.Request.Context(), a, row) != nil {
					c.AbortWithStatusJSON(409, gin.H{"error": gin.H{"code": "ocr_ownership_proof_required"}})
					return
				}
				c.Set("platform_task_call", row)
				c.Next()
				return
			}
			if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || !platformbilling.IsModelCredential(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")) || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
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
			if op == "video" {
				controller.PlatformTaskCancel(c)
				return
			}
			c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "provider_cancellation_unavailable"}})
		})
	}
}
