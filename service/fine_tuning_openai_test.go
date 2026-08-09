package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAICompatibleFineTuningProviderContract(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer secret", request.Header.Get("Authorization"))
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.Path)
		mu.Unlock()
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/files":
			require.NoError(t, request.ParseMultipartForm(1<<20))
			file, _, err := request.FormFile("file")
			require.NoError(t, err)
			defer file.Close()
			content, err := io.ReadAll(file)
			require.NoError(t, err)
			assert.NotEmpty(t, content)
			writeProviderJSON(t, response, map[string]any{"id": "file-upstream"})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/fine_tuning/jobs":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "file-upstream", body["training_file"])
			writeProviderJSON(t, response, map[string]any{"id": "ftjob-upstream", "status": "queued"})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/fine_tuning/jobs/ftjob-upstream":
			writeProviderJSON(t, response, map[string]any{"id": "ftjob-upstream", "status": "succeeded", "fine_tuned_model": "ft:model"})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/fine_tuning/jobs/ftjob-upstream/events":
			writeProviderJSON(t, response, map[string]any{"data": []map[string]any{{"id": "event-1", "created_at": 123, "level": "info", "message": "done"}}})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/fine_tuning/jobs/ftjob-upstream/cancel":
			writeProviderJSON(t, response, map[string]any{"status": "cancelled"})
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/models/ft:model":
			writeProviderJSON(t, response, map[string]any{"deleted": true})
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleFineTuningProviderForTest(server.URL+"/v1", "secret", server.Client())
	require.NoError(t, err)
	created, err := provider.CreateFineTuningJob(t.Context(), FineTuningCreateInput{
		Model: "gpt-test", TrainingFile: FineTuningUpload{Filename: "training.jsonl", Reader: strings.NewReader("{}\n")},
	})
	require.NoError(t, err)
	assert.Equal(t, "ftjob-upstream", created.UpstreamID)

	poll, err := provider.PollFineTuningJob(t.Context(), &model.FineTuningJob{UpstreamID: created.UpstreamID})
	require.NoError(t, err)
	assert.Equal(t, model.FineTuningStatusSucceeded, poll.Status)
	assert.Equal(t, "ft:model", poll.ResultModel)
	require.Len(t, poll.Events, 1)
	assert.Equal(t, "event-1", poll.Events[0].UpstreamEventID)
	status, err := provider.CancelFineTuningJob(t.Context(), created.UpstreamID)
	require.NoError(t, err)
	assert.Equal(t, model.FineTuningStatusCancelled, status)
	require.NoError(t, provider.DeleteFineTunedModel(context.Background(), "ft:model"))

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, requests, "POST /v1/fine_tuning/jobs")
	assert.Contains(t, requests, "DELETE /v1/models/ft:model")
}

func writeProviderJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(response).Encode(value))
}
