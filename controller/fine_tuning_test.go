package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubFineTuningLifecycleProvider struct {
	createCalls atomic.Int32
	cancelCalls atomic.Int32
	deleteCalls atomic.Int32
}

func (*stubFineTuningLifecycleProvider) PollFineTuningJob(context.Context, *model.FineTuningJob) (service.FineTuningPollResult, error) {
	return service.FineTuningPollResult{Status: model.FineTuningStatusRunning}, nil
}

func (provider *stubFineTuningLifecycleProvider) CreateFineTuningJob(_ context.Context, input service.FineTuningCreateInput) (service.FineTuningCreateResult, error) {
	provider.createCalls.Add(1)
	content, err := io.ReadAll(input.TrainingFile.Reader)
	if err != nil || len(content) == 0 {
		return service.FineTuningCreateResult{}, assert.AnError
	}
	return service.FineTuningCreateResult{UpstreamID: "upstream-job-1", Status: model.FineTuningStatusRunning}, nil
}

func (provider *stubFineTuningLifecycleProvider) CancelFineTuningJob(context.Context, string) (string, error) {
	provider.cancelCalls.Add(1)
	return model.FineTuningStatusCancelled, nil
}

func (provider *stubFineTuningLifecycleProvider) DeleteFineTunedModel(context.Context, string) error {
	provider.deleteCalls.Add(1)
	return nil
}

func TestFineTuningLifecycleAndAuthorization(t *testing.T) {
	router, _ := setupFineTuningControllerTest(t)
	upload := uploadAPIFile(t, router, 51, "training.jsonl", "fine-tune", []byte("{\"messages\":[]}\n"))
	require.Equal(t, http.StatusOK, upload.Code, upload.Body.String())
	var file dto.APIFileObject
	require.NoError(t, json.Unmarshal(upload.Body.Bytes(), &file))

	createBody := strings.NewReader(`{"training_file":"` + file.ID + `","model":"gpt-4.1-mini","n_epochs":2}`)
	created := performFileRequest(router, 51, http.MethodPost, "/v1/fine-tunes", createBody, "application/json")
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	var job dto.FineTuningJobObject
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &job))
	require.NotEmpty(t, job.ID)
	assert.Equal(t, model.FineTuningStatusRunning, job.Status)
	assert.Equal(t, float64(2), job.Hyperparameters["n_epochs"])

	listed := performFileRequest(router, 51, http.MethodGet, "/v1/fine-tunes", nil, "")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Contains(t, listed.Body.String(), job.ID)
	retrieved := performFileRequest(router, 51, http.MethodGet, "/v1/fine-tunes/"+job.ID, nil, "")
	assert.Equal(t, http.StatusOK, retrieved.Code, retrieved.Body.String())
	hidden := performFileRequest(router, 52, http.MethodGet, "/v1/fine-tunes/"+job.ID, nil, "")
	assert.Equal(t, http.StatusNotFound, hidden.Code, hidden.Body.String())

	events := performFileRequest(router, 51, http.MethodGet, "/v1/fine-tunes/"+job.ID+"/events", nil, "")
	require.Equal(t, http.StatusOK, events.Code, events.Body.String())
	assert.Contains(t, events.Body.String(), "Fine-tuning job created")

	cancelled := performFileRequest(router, 51, http.MethodPost, "/v1/fine-tunes/"+job.ID+"/cancel", nil, "application/json")
	require.Equal(t, http.StatusOK, cancelled.Code, cancelled.Body.String())
	assert.Contains(t, cancelled.Body.String(), model.FineTuningStatusCancelled)
	again := performFileRequest(router, 51, http.MethodPost, "/v1/fine-tunes/"+job.ID+"/cancel", nil, "application/json")
	assert.Equal(t, http.StatusConflict, again.Code, again.Body.String())
	assert.Contains(t, again.Body.String(), "invalid_resource_state")
}

func TestFineTuningCreateHidesOtherUsersFile(t *testing.T) {
	router, provider := setupFineTuningControllerTest(t)
	upload := uploadAPIFile(t, router, 61, "training.jsonl", "fine-tune", []byte("{\"messages\":[]}\n"))
	require.Equal(t, http.StatusOK, upload.Code)
	var file dto.APIFileObject
	require.NoError(t, json.Unmarshal(upload.Body.Bytes(), &file))

	response := performFileRequest(router, 62, http.MethodPost, "/v1/fine-tunes",
		strings.NewReader(`{"training_file":"`+file.ID+`","model":"gpt-test"}`), "application/json")
	assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
	assert.Equal(t, int32(0), provider.createCalls.Load())
}

func TestDeleteModelRequiresOwnedCompletedJob(t *testing.T) {
	router, provider := setupFineTuningControllerTest(t)
	job := &model.FineTuningJob{
		ID: "ftjob-model-delete", TenantID: defaultTenantID, UserID: 71,
		TrainingFileID: "file-a", Model: "gpt-test", Status: model.FineTuningStatusSucceeded,
		ResultModel: "ft:gpt-test:owner", FinishedAt: 1,
	}
	require.NoError(t, model.CreateFineTuningJob(job))

	hidden := performFileRequest(router, 72, http.MethodDelete, "/v1/models/ft:gpt-test:owner", nil, "")
	assert.Equal(t, http.StatusNotFound, hidden.Code, hidden.Body.String())
	deleted := performFileRequest(router, 71, http.MethodDelete, "/v1/models/ft:gpt-test:owner", nil, "")
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	assert.Contains(t, deleted.Body.String(), `"deleted":true`)
	assert.Equal(t, int32(1), provider.deleteCalls.Load())
	again := performFileRequest(router, 71, http.MethodDelete, "/v1/models/ft:gpt-test:owner", nil, "")
	assert.Equal(t, http.StatusNotFound, again.Code, again.Body.String())
}

func setupFineTuningControllerTest(t *testing.T) (*gin.Engine, *stubFineTuningLifecycleProvider) {
	t.Helper()
	router, _ := setupFileControllerTest(t, 1024*1024)
	require.NoError(t, model.DB.AutoMigrate(&model.FineTuningJob{}, &model.FineTuningEvent{}))
	provider := &stubFineTuningLifecycleProvider{}
	oldContextFactory := fineTuningProviderFromContext
	oldJobFactory := fineTuningProviderFromJob
	fineTuningProviderFromContext = func(*gin.Context) (service.FineTuningLifecycleProvider, error) { return provider, nil }
	fineTuningProviderFromJob = func(*model.FineTuningJob) (service.FineTuningLifecycleProvider, error) { return provider, nil }
	t.Cleanup(func() {
		fineTuningProviderFromContext = oldContextFactory
		fineTuningProviderFromJob = oldJobFactory
	})
	router.POST("/v1/fine-tunes", CreateFineTuningJob)
	router.GET("/v1/fine-tunes", ListFineTuningJobs)
	router.GET("/v1/fine-tunes/:id", GetFineTuningJob)
	router.POST("/v1/fine-tunes/:id/cancel", CancelFineTuningJob)
	router.GET("/v1/fine-tunes/:id/events", ListFineTuningEvents)
	router.DELETE("/v1/models/:model", DeleteFineTunedModel)
	return router, provider
}
