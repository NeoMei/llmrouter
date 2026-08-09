package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var fineTuningProviderFromContext = func(c *gin.Context) (service.FineTuningLifecycleProvider, error) {
	return service.NewOpenAICompatibleFineTuningProviderFromConfig(
		common.GetContextKeyString(c, constant.ContextKeyChannelBaseUrl),
		common.GetContextKeyString(c, constant.ContextKeyChannelKey),
		common.GetContextKeyString(c, constant.ContextKeyChannelOrganization),
		nil,
	)
}

var fineTuningProviderFromJob = func(job *model.FineTuningJob) (service.FineTuningLifecycleProvider, error) {
	provider, err := service.FineTuningProviderForJob(job)
	if err != nil {
		return nil, err
	}
	lifecycle, ok := provider.(service.FineTuningLifecycleProvider)
	if !ok {
		return nil, errors.New("provider does not implement fine-tuning lifecycle operations")
	}
	return lifecycle, nil
}

func CreateFineTuningJob(c *gin.Context) {
	var request dto.FineTuningCreateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeFileError(c, http.StatusBadRequest, "invalid_file", "invalid fine-tuning request")
		return
	}
	request.Model = strings.TrimSpace(request.Model)
	request.TrainingFile = strings.TrimSpace(request.TrainingFile)
	if request.Model == "" || request.TrainingFile == "" || len(request.Suffix) > 64 {
		writeFileError(c, http.StatusBadRequest, "invalid_file", "model and training_file are required and suffix must be at most 64 characters")
		return
	}
	if request.NEpochs != nil {
		if request.Hyperparameters == nil {
			request.Hyperparameters = map[string]any{}
		}
		request.Hyperparameters["n_epochs"] = request.NEpochs
	}
	manager, err := apiFileManagerProvider()
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "file storage is unavailable")
		return
	}
	userID := c.GetInt("id")
	trainingBody, _, trainingFile, err := manager.Open(c.Request.Context(), defaultTenantID, userID, request.TrainingFile)
	if err != nil {
		writeAPIFileManagerError(c, err)
		return
	}
	defer trainingBody.Close()
	if trainingFile.Purpose != "fine-tune" {
		writeFileError(c, http.StatusConflict, "invalid_resource_state", "training_file purpose must be fine-tune")
		return
	}
	var validationBody io.ReadCloser
	var validationUpload *service.FineTuningUpload
	if request.ValidationFile != "" {
		body, _, file, openErr := manager.Open(c.Request.Context(), defaultTenantID, userID, request.ValidationFile)
		if openErr != nil {
			writeAPIFileManagerError(c, openErr)
			return
		}
		validationBody = body
		defer validationBody.Close()
		if file.Purpose != "fine-tune" {
			writeFileError(c, http.StatusConflict, "invalid_resource_state", "validation_file purpose must be fine-tune")
			return
		}
		validationUpload = &service.FineTuningUpload{Filename: file.Filename, Reader: body}
	}
	provider, err := fineTuningProviderFromContext(c)
	if err != nil {
		writeFileError(c, http.StatusServiceUnavailable, "no_capable_channel", "fine-tuning provider is unavailable")
		return
	}
	hyperparameters, _ := json.Marshal(request.Hyperparameters)
	job := &model.FineTuningJob{
		TenantID: defaultTenantID, UserID: userID, TrainingFileID: request.TrainingFile,
		ValidationFileID: request.ValidationFile, Model: request.Model, Suffix: request.Suffix,
		Hyperparameters: string(hyperparameters), ChannelID: common.GetContextKeyInt(c, constant.ContextKeyChannelId),
		ProviderType: common.GetContextKeyInt(c, constant.ContextKeyChannelType), Status: model.FineTuningStatusQueued,
		PollAfter: time.Now().Add(5 * time.Minute).Unix(), BillingSnapshot: `{"mode":"provider-reported"}`,
	}
	if err := model.CreateFineTuningJob(job); err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "failed to create fine-tuning job")
		return
	}
	_, _, _ = model.AppendFineTuningEvent(&model.FineTuningEvent{JobID: job.ID, Level: "info", Message: "Fine-tuning job created"})
	created, err := provider.CreateFineTuningJob(c.Request.Context(), service.FineTuningCreateInput{
		Model: request.Model, Suffix: request.Suffix, Hyperparameters: request.Hyperparameters,
		TrainingFile: service.FineTuningUpload{Filename: trainingFile.Filename, Reader: trainingBody}, ValidationFile: validationUpload,
	})
	if err != nil {
		_, _ = model.TransitionFineTuningJob(job.ID, []string{model.FineTuningStatusQueued}, model.FineTuningStatusFailed,
			map[string]any{"failure_json": marshalFineTuningFailure(err), "finished_at": time.Now().Unix()})
		_, _, _ = model.AppendFineTuningEvent(&model.FineTuningEvent{JobID: job.ID, Level: "error", Message: "Upstream fine-tuning creation failed"})
		writeFileError(c, http.StatusBadGateway, "upstream_error", "upstream fine-tuning creation failed")
		return
	}
	status := created.Status
	if status == "" {
		status = model.FineTuningStatusQueued
	}
	if status != model.FineTuningStatusQueued && status != model.FineTuningStatusRunning && !model.IsFineTuningTerminal(status) {
		_, _ = model.TransitionFineTuningJob(job.ID, []string{model.FineTuningStatusQueued}, model.FineTuningStatusFailed,
			map[string]any{"failure_json": `{"message":"provider returned invalid status"}`, "finished_at": time.Now().Unix()})
		writeFileError(c, http.StatusBadGateway, "upstream_error", "upstream returned an invalid fine-tuning status")
		return
	}
	updates := map[string]any{"upstream_id": created.UpstreamID, "poll_after": time.Now().Unix()}
	if model.IsFineTuningTerminal(status) {
		updates["finished_at"] = time.Now().Unix()
	}
	changed, err := model.TransitionFineTuningJob(job.ID, []string{model.FineTuningStatusQueued}, status, updates)
	if err != nil || !changed {
		writeFileError(c, http.StatusConflict, "invalid_resource_state", "fine-tuning job state changed during creation")
		return
	}
	reloaded, err := model.GetOwnedFineTuningJob(defaultTenantID, userID, job.ID)
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "failed to reload fine-tuning job")
		return
	}
	c.JSON(http.StatusOK, newFineTuningJobObject(reloaded))
}

func ListFineTuningJobs(c *gin.Context) {
	limit, ok := parseFineTuningLimit(c)
	if !ok {
		return
	}
	jobs, err := model.ListFineTuningJobs(defaultTenantID, c.GetInt("id"), c.Query("after"), limit)
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "failed to list fine-tuning jobs")
		return
	}
	objects := make([]dto.FineTuningJobObject, 0, len(jobs))
	for index := range jobs {
		objects = append(objects, newFineTuningJobObject(&jobs[index]))
	}
	c.JSON(http.StatusOK, dto.FineTuningJobList{Object: "list", Data: objects})
}

func GetFineTuningJob(c *gin.Context) {
	job, err := model.GetOwnedFineTuningJob(defaultTenantID, c.GetInt("id"), c.Param("id"))
	if err != nil {
		writeFineTuningLookupError(c, err)
		return
	}
	c.JSON(http.StatusOK, newFineTuningJobObject(job))
}

func CancelFineTuningJob(c *gin.Context) {
	job, err := model.GetOwnedFineTuningJob(defaultTenantID, c.GetInt("id"), c.Param("id"))
	if err != nil {
		writeFineTuningLookupError(c, err)
		return
	}
	if model.IsFineTuningTerminal(job.Status) {
		writeFileError(c, http.StatusConflict, "invalid_resource_state", "fine-tuning job is already terminal")
		return
	}
	provider, err := fineTuningProviderFromJob(job)
	if err != nil {
		writeFileError(c, http.StatusServiceUnavailable, "no_capable_channel", "fine-tuning provider is unavailable")
		return
	}
	if _, err := provider.CancelFineTuningJob(c.Request.Context(), job.UpstreamID); err != nil {
		writeFileError(c, http.StatusBadGateway, "upstream_error", "upstream fine-tuning cancellation failed")
		return
	}
	changed, err := model.TransitionFineTuningJob(job.ID,
		[]string{model.FineTuningStatusQueued, model.FineTuningStatusRunning}, model.FineTuningStatusCancelled,
		map[string]any{"cancelled_at": time.Now().Unix(), "finished_at": time.Now().Unix(), "poll_after": 0, "poll_locked_by": "", "poll_lease_until": 0})
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "failed to cancel fine-tuning job")
		return
	}
	if !changed {
		writeFileError(c, http.StatusConflict, "invalid_resource_state", "fine-tuning job state changed during cancellation")
		return
	}
	_, _, _ = model.AppendFineTuningEvent(&model.FineTuningEvent{JobID: job.ID, Level: "info", Message: "Fine-tuning job cancelled"})
	reloaded, _ := model.GetOwnedFineTuningJob(defaultTenantID, c.GetInt("id"), job.ID)
	c.JSON(http.StatusOK, newFineTuningJobObject(reloaded))
}

func ListFineTuningEvents(c *gin.Context) {
	limit, ok := parseFineTuningLimit(c)
	if !ok {
		return
	}
	after := int64(0)
	if raw := c.Query("after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeFileError(c, http.StatusBadRequest, "invalid_resource_state", "after must be a non-negative event sequence")
			return
		}
		after = parsed
	}
	events, err := model.ListFineTuningEvents(defaultTenantID, c.GetInt("id"), c.Param("id"), after, limit)
	if err != nil {
		writeFineTuningLookupError(c, err)
		return
	}
	objects := make([]dto.FineTuningEventObject, 0, len(events))
	for _, event := range events {
		var data any
		if event.DataJSON != "" {
			_ = json.Unmarshal([]byte(event.DataJSON), &data)
		}
		objects = append(objects, dto.FineTuningEventObject{Object: "fine-tune-event", CreatedAt: event.CreatedAt, Level: event.Level, Message: event.Message, Data: data})
	}
	c.JSON(http.StatusOK, dto.FineTuningEventList{Object: "list", Data: objects})
}

func DeleteFineTunedModel(c *gin.Context) {
	job, err := model.GetOwnedCompletedFineTuningJobByModel(defaultTenantID, c.GetInt("id"), c.Param("model"))
	if err != nil {
		writeFineTuningLookupError(c, err)
		return
	}
	provider, err := fineTuningProviderFromJob(job)
	if err != nil {
		writeFileError(c, http.StatusServiceUnavailable, "no_capable_channel", "fine-tuning provider is unavailable")
		return
	}
	if err := provider.DeleteFineTunedModel(c.Request.Context(), job.ResultModel); err != nil {
		writeFileError(c, http.StatusBadGateway, "upstream_error", "upstream model deletion failed")
		return
	}
	deleted, err := model.MarkFineTunedModelDeleted(job.ID)
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "failed to record model deletion")
		return
	}
	if !deleted {
		writeFileError(c, http.StatusConflict, "invalid_resource_state", "model deletion state changed")
		return
	}
	c.JSON(http.StatusOK, dto.ModelDeleteResponse{ID: job.ResultModel, Object: "model", Deleted: true})
}

func newFineTuningJobObject(job *model.FineTuningJob) dto.FineTuningJobObject {
	if job == nil {
		return dto.FineTuningJobObject{}
	}
	var hyperparameters map[string]any
	if job.Hyperparameters != "" {
		_ = json.Unmarshal([]byte(job.Hyperparameters), &hyperparameters)
	}
	var failure any
	if job.FailureJSON != "" {
		_ = json.Unmarshal([]byte(job.FailureJSON), &failure)
	}
	return dto.FineTuningJobObject{
		ID: job.ID, Object: "fine-tune", Model: job.Model, CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt,
		FineTunedModel: job.ResultModel, Status: job.Status, TrainingFile: job.TrainingFileID,
		ValidationFile: job.ValidationFileID, Hyperparameters: hyperparameters, FailureReason: failure,
	}
}

func parseFineTuningLimit(c *gin.Context) (int, bool) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeFileError(c, http.StatusBadRequest, "invalid_resource_state", "limit must be between 1 and 100")
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func writeFineTuningLookupError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, service.ErrAPIFileNotFound) {
		writeFileError(c, http.StatusNotFound, "resource_not_found", "resource not found")
		return
	}
	writeFileError(c, http.StatusInternalServerError, "upstream_error", "resource lookup failed")
}

func marshalFineTuningFailure(err error) string {
	payload, _ := json.Marshal(map[string]string{"message": err.Error()})
	return string(payload)
}
