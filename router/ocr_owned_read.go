package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func registerNativeOCRRead(engine gin.IRoutes, a *platformbilling.Adapter) {
	engine.GET("/platform/api/v2/ocr/jobs/:callID", func(c *gin.Context) {
		policy, e := unihubaccess.FromEnv()
		if e != nil || !policy.PlatformEnabled {
			c.AbortWithStatus(503)
			return
		}
		if len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || c.Request.URL.RawQuery != "" {
			c.AbortWithStatus(401)
			return
		}
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		var row platformbilling.Call
		if platformbilling.IsOwnedReadCredential(token) {
			row, e = a.OwnedTaskRead(c.Request.Context(), token, c.Param("callID"))
		} else if platformbilling.IsModelCredential(token) && len(c.Request.Header.Values("X-UniHub-Task-ID")) == 1 {
			row, e = a.Task(c.Request.Context(), token, c.GetHeader("X-UniHub-Task-ID"), c.Param("callID"))
		} else {
			c.AbortWithStatus(401)
			return
		}
		if e != nil || row.Operation != "ocr" || row.WireMode != "paddle-native-v1" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if platformbilling.IsOwnedReadCredential(token) && recoverOwnedOCRRead(c.Request.Context(), a, row) != nil {
			c.AbortWithStatusJSON(409, gin.H{"error": gin.H{"code": "ocr_ownership_proof_required"}})
			return
		}
		c.Set("platform_task_call", row)
		c.Set("platform_ocr_native", true)
		controller.PlatformTaskStatus(c)
	})
}
