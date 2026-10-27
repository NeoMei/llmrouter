package controller

import (
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"net/http"
)

func PlatformTaskStatus(c *gin.Context) { platformTaskRead(c, false) }
func PlatformTaskResult(c *gin.Context) { platformTaskRead(c, true) }
func platformTaskRead(c *gin.Context, result bool) {
	value, ok := c.Get("platform_task_call")
	if !ok {
		c.AbortWithStatus(404)
		return
	}
	row, ok := value.(platformbilling.Call)
	if !ok {
		c.AbortWithStatus(404)
		return
	}
	if row.Operation == "ocr" {
		if OCRRuntime == nil {
			c.AbortWithStatus(503)
			return
		}
		var job model.OCRJob
		if err := OCRRuntime.DB.WithContext(c.Request.Context()).Where("platform_call_id = ? AND funding = ? AND provider_job_id = ?", row.CallID, "platform", row.ProviderTaskID).First(&job).Error; err != nil {
			c.AbortWithStatus(404)
			return
		}
		status, err := OCRRuntime.Service.Status(c.Request.Context(), job.ID, job.UserID, job.TokenID)
		if err != nil {
			c.AbortWithStatus(502)
			return
		}
		if result {
			out, err := OCRRuntime.Service.CanonicalResult(c.Request.Context(), job.ID, job.UserID, job.TokenID)
			if err != nil {
				c.AbortWithStatus(409)
				return
			}
			c.JSON(200, out)
			return
		}
		c.JSON(200, gin.H{"id": row.CallID, "job_id": job.ID, "state": status.State, "progress": status.ExtractProgress, "result_url": "/platform/v1/ocr/jobs/" + row.CallID + "/result"})
		return
	}
	var task model.Task
	if err := model.DB.WithContext(c.Request.Context()).Where("platform_call_id = ? AND funding_owner = ?", row.CallID, "platform").First(&task).Error; err != nil || task.GetUpstreamTaskID() != row.ProviderTaskID {
		c.AbortWithStatus(404)
		return
	}
	if result {
		if task.Status != model.TaskStatusSuccess {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		c.Set("platform_verified_video_task", &task)
		c.Params = append(c.Params, gin.Param{Key: "task_id", Value: task.TaskID})
		VideoProxy(c)
		return
	}
	c.JSON(200, gin.H{"id": row.CallID, "task_id": task.TaskID, "status": task.Status, "progress": task.Progress, "result_url": "/platform/v1/videos/" + row.CallID + "/result"})
}
