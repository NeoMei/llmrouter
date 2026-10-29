package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var wanImageProviderID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var wanImagePositiveCount = regexp.MustCompile(`^[1-9][0-9]*$`)

func wanImageSubmission(raw []byte) (string, error) {
	var envelope map[string]any
	if common.UnmarshalUnique(raw, &envelope) != nil || !wanImageKeys(envelope, "output", "request_id") {
		return "", platformbilling.ErrInvalid
	}
	output, ok := envelope["output"].(map[string]any)
	if !ok || !wanImageKeys(output, "task_id", "task_status") {
		return "", platformbilling.ErrInvalid
	}
	id, ok := output["task_id"].(string)
	if !ok || !wanImageProviderID.MatchString(id) || (output["task_status"] != "PENDING" && output["task_status"] != "RUNNING") {
		return "", platformbilling.ErrInvalid
	}
	return id, nil
}

func wanImageKeys(value map[string]any, allowed ...string) bool {
	if value == nil {
		return false
	}
	for key := range value {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func wanImageNoOutput(envelope, output map[string]any, reserved int64) bool {
	if !wanImageKeys(envelope, "output", "usage", "request_id") || !wanImageKeys(output, "task_id", "task_status", "code", "message", "task_metrics", "submit_time", "scheduled_time", "end_time", "results") {
		return false
	}
	if results, exists := output["results"]; exists {
		items, ok := results.([]any)
		if !ok || len(items) != 0 {
			return false
		}
	}
	metrics, ok := output["task_metrics"].(map[string]any)
	if !ok || !wanImageKeys(metrics, "TOTAL", "SUCCEEDED", "FAILED") {
		return false
	}
	total, ok := metrics["TOTAL"].(json.Number)
	if !ok || !wanImagePositiveCount.MatchString(string(total)) || metrics["SUCCEEDED"] != json.Number("0") || metrics["FAILED"] != total {
		return false
	}
	count, err := strconv.ParseInt(string(total), 10, 64)
	if err != nil || count != reserved {
		return false
	}
	if usage, exists := envelope["usage"]; exists {
		u, ok := usage.(map[string]any)
		if !ok || !wanImageKeys(u, "image_count") || u["image_count"] != json.Number("0") {
			return false
		}
	}
	return true
}

// readWanImage never repeats the payable submit. It reads only the durable,
// provider-bound task after validating the same actor/account/model grant.
func readWanImage(c *gin.Context, a *platformbilling.Adapter) {
	if c.Request.URL.RawQuery != "" || len(c.Request.Header.Values("Authorization")) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || len(c.Request.Header.Values("X-UniHub-Task-ID")) != 1 {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	row, policy, err := a.WanImageTask(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-UniHub-Task-ID"), c.Param("callID"))
	if err != nil || !wanImageProviderID.MatchString(row.ProviderTaskID) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	pollWanImage(c, a, row, policy)
}

// pollWanImage receives only a verified immutable call binding. Interactive
// readers authenticate before entry; the recovery worker loads that binding
// from the durable journal. It never submits a new payable request.
func pollWanImage(c *gin.Context, a *platformbilling.Adapter, row platformbilling.Call, policy platformbilling.ImagePolicy) {
	if row.Status == "RELEASED" || row.Status == "RELEASE_PENDING" {
		if err := a.ReleaseWanImage(c.Request.Context(), row); err != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"id": row.CallID, "status": "failed"})
		return
	}
	if row.ImageObservation != "" {
		body, err := a.RecoverWanImage(c.Request.Context(), row)
		if err != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "application/json", body)
		return
	}
	if row.Status != "UNKNOWN" {
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	var channel model.Channel
	if model.DB.WithContext(c.Request.Context()).First(&channel, policy.ChannelID).Error != nil || channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.IsMultiKey || channel.Type != policy.ChannelType || channel.GetBaseURL() != policy.Origin {
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	keyHash := sha256.Sum256([]byte(channel.Key))
	if hex.EncodeToString(keyHash[:]) != policy.ChannelKeySHA256 {
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	client := imageProducerHTTPClient()
	if client == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	closed := *client
	closed.Jar = nil
	closed.Timeout = 30 * time.Second
	closed.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, policy.Origin+"/api/v1/tasks/"+url.PathEscape(row.ProviderTaskID), nil)
	if err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	req.Header.Set("Authorization", "Bearer "+channel.Key)
	response, err := closed.Do(req)
	if err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 || response.StatusCode != http.StatusOK {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	var envelope map[string]any
	if common.UnmarshalUnique(raw, &envelope) != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	output, ok := envelope["output"].(map[string]any)
	if !ok || output["task_id"] != row.ProviderTaskID {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if output["task_status"] == "PENDING" || output["task_status"] == "RUNNING" {
		if !wanImageKeys(envelope, "output", "request_id") || !wanImageKeys(output, "task_id", "task_status", "submit_time", "scheduled_time") {
			c.AbortWithStatus(http.StatusBadGateway)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusAccepted, gin.H{"id": row.CallID, "status": "pending"})
		return
	}
	if (output["task_status"] == "FAILED" || output["task_status"] == "CANCELED") && wanImageNoOutput(envelope, output, row.ReservationQuantity) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
		defer cancel()
		if err := a.ReleaseWanImage(ctx, row); err != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"id": row.CallID, "status": "failed"})
		return
	}
	if output["task_status"] != "SUCCEEDED" {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "image_task_unresolved"}})
		return
	}
	result, err := platformbilling.ObserveWanMaskedEdit(policy, row.ProviderTaskID, raw, func(location string) ([]byte, string, error) {
		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, location, nil)
		if err != nil {
			return nil, "", err
		}
		resource, err := closed.Do(request)
		if err != nil {
			return nil, "", err
		}
		defer resource.Body.Close()
		if resource.StatusCode != http.StatusOK {
			return nil, "", platformbilling.ErrInvalid
		}
		data, err := io.ReadAll(io.LimitReader(resource.Body, int64(policy.MaxImageBytes)+1))
		if err != nil || len(data) > policy.MaxImageBytes {
			return nil, "", platformbilling.ErrInvalid
		}
		return data, resource.Header.Get("Content-Type"), nil
	})
	if err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
	defer cancel()
	if err := a.CompleteWanImage(ctx, row, result); err != nil {
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/json", result.Body)
}
