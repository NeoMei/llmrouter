package controller

import (
	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func PlatformTaskStatus(c *gin.Context) { platformTaskRead(c, false) }
func PlatformTaskResult(c *gin.Context) { platformTaskRead(c, true) }

type videoCancelAdaptor interface {
	CancelTask(baseURL, key, taskID, proxy string) (*http.Response, error)
}

func PlatformTaskCancel(c *gin.Context) {
	value, ok := c.Get("platform_task_call")
	if !ok {
		c.AbortWithStatus(404)
		return
	}
	row, ok := value.(platformbilling.Call)
	if !ok || row.Operation != "video" {
		c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "provider_cancellation_unavailable"}})
		return
	}
	var task model.Task
	if err := model.DB.WithContext(c.Request.Context()).Where("platform_call_id = ? AND funding_owner = ?", row.CallID, "platform").First(&task).Error; err != nil || task.GetUpstreamTaskID() != row.ProviderTaskID {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusSuccess {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "provider_task_terminal"}})
		return
	}
	adaptor := service.GetTaskAdaptorFunc(task.Platform)
	canceller, ok := adaptor.(videoCancelAdaptor)
	if !ok || canceller == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "provider_cancellation_unavailable"}})
		return
	}
	channel, err := model.GetChannelById(task.ChannelId, true)
	if err != nil {
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	baseURL := constant.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() != "" {
		baseURL = channel.GetBaseURL()
	}
	key, err := model.TaskAccountKey(c.Request.Context(), model.DB, channel, &task)
	if err != nil {
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	resp, err := canceller.CancelTask(baseURL, key, task.GetUpstreamTaskID(), channel.GetSetting().Proxy)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "provider_cancellation_unknown"}})
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || int64(len(body)) == 1<<20 {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "provider_cancellation_unknown"}})
		return
	}
	var receipt struct {
		Code   string `json:"code"`
		Output struct {
			TaskStatus string `json:"task_status"`
			VideoURL   string `json:"video_url"`
			Results    []struct {
				URL string `json:"url"`
			} `json:"results"`
		} `json:"output"`
	}
	decodeErr := common.UnmarshalUnique(body, &receipt)
	if decodeErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "provider_cancellation_unknown"}})
		return
	}
	if receipt.Code == "UnsupportedOperation" {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "provider_cancellation_unsupported"}})
		return
	}
	if resp.StatusCode != http.StatusOK || receipt.Output.TaskStatus != "CANCELED" || receipt.Output.VideoURL != "" || len(receipt.Output.Results) != 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "provider_cancellation_unknown"}})
		return
	}
	snap := task.Snapshot()
	task.Data = body
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.FinishTime = common.GetTimestamp()
	task.FailReason = "provider canceled"
	if err = service.PrepareNativeTaskEvidence(&task, body); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "native_evidence_conflict"}})
		return
	}
	var evidence struct {
		Source string `json:"source"`
	}
	if common.UnmarshalUnique([]byte(task.NativeEvidence), &evidence) != nil || evidence.Source != "provider_confirmed_no_output" {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "provider_cancellation_unknown"}})
		return
	}
	won, err := task.UpdateWithStatus(snap.Status)
	if err != nil || !won {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "provider_task_transition_conflict"}})
		return
	}
	if err = service.SettleNativeTask(c.Request.Context(), &task); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "native_settlement_pending"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": row.CallID, "task_id": task.TaskID, "status": task.Status, "progress": task.Progress})
}
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
		var attempt platformbilling.Attempt
		var execution model.OCRExecution
		var binding model.OCRAccountBinding
		if OCRRuntime.DB.WithContext(c.Request.Context()).Where("call_id = ?", row.CallID).Order("id DESC").First(&attempt).Error != nil || attempt.ChannelID != job.ChannelID || common.UnmarshalJsonStr(job.Execution, &execution) != nil || common.UnmarshalJsonStr(job.AccountBinding, &binding) != nil || binding.Ref.ChannelID != job.ChannelID || binding.Ref.MemberID == "" || execution.RequestedModel != row.Model || execution.KeySHA256 == "" || len(execution.ResourceOrigins) == 0 {
			c.AbortWithStatusJSON(409, gin.H{"error": gin.H{"code": "ocr_ownership_proof_required"}})
			return
		}
		status, err := OCRRuntime.Service.Status(c.Request.Context(), job.ID, job.UserID, job.TokenID)
		if err != nil {
			c.AbortWithStatus(502)
			return
		}
		if c.GetBool("platform_ocr_native") {
			if status.State == "done" && (service.PlatformTaskSettlementCheck == nil || service.PlatformTaskSettlementCheck(c.Request.Context(), row.CallID) != nil) {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"code": "native_settlement_pending"}})
				return
			}
			status.JobID = row.CallID
			c.JSON(200, gin.H{"code": 0, "msg": "", "data": status})
			return
		}
		if result {
			if service.PlatformTaskSettlementCheck == nil || service.PlatformTaskSettlementCheck(c.Request.Context(), row.CallID) != nil {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"code": "native_settlement_pending"}})
				return
			}
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
		if service.PlatformTaskSettlementCheck == nil || service.PlatformTaskSettlementCheck(c.Request.Context(), row.CallID) != nil {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"code": "native_settlement_pending"}})
			return
		}
		if task.PrivateData.ResultResourceID != "" {
			runtime := RelayMediaRuntime
			if runtime == nil || runtime.Service == nil {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"code": "owned_video_resource_unavailable"}})
				return
			}
			data, err := runtime.Service.ReadOwned(c.Request.Context(), task.PrivateData.ResultResourceID, task.UserId, task.UserId)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"code": "owned_video_resource_unavailable"}})
				return
			}
			c.Header("Content-Type", task.PrivateData.ResultContentType)
			c.Header("Content-Length", strconv.FormatInt(int64(len(data)), 10))
			c.Header("X-Content-Sha256", task.PrivateData.ResultSHA256)
			c.Header("X-Content-Type-Options", "nosniff")
			c.Data(http.StatusOK, task.PrivateData.ResultContentType, data)
			return
		}
		c.Set("platform_verified_video_task", &task)
		c.Params = append(c.Params, gin.Param{Key: "task_id", Value: task.TaskID})
		VideoProxy(c)
		return
	}
	c.JSON(200, gin.H{"id": row.CallID, "task_id": task.TaskID, "status": task.Status, "progress": task.Progress, "result_url": "/platform/v1/videos/" + row.CallID + "/result"})
}
