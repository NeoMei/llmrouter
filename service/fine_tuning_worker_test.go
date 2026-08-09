package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubFineTuningProvider struct {
	calls  atomic.Int32
	result FineTuningPollResult
	err    error
}

func (provider *stubFineTuningProvider) PollFineTuningJob(context.Context, *model.FineTuningJob) (FineTuningPollResult, error) {
	provider.calls.Add(1)
	return provider.result, provider.err
}

func TestFineTuningWorkerClaimsOnceAcrossInstances(t *testing.T) {
	prepareFineTuningWorkerTest(t)
	job := &model.FineTuningJob{ID: "ftjob-worker-claim", TenantID: "tenant", UserID: 1, TrainingFileID: "file-a", Model: "gpt-test", ProviderType: 77}
	require.NoError(t, model.CreateFineTuningJob(job))
	provider := &stubFineTuningProvider{result: FineTuningPollResult{Status: model.FineTuningStatusSucceeded, ResultModel: "ft:model"}}
	providerFor := func(int) (FineTuningProvider, error) { return provider, nil }
	fixedNow := time.Unix(time.Now().Unix(), 0)

	var wg sync.WaitGroup
	for _, runner := range []string{"runner-a", "runner-b"} {
		wg.Add(1)
		go func(runner string) {
			defer wg.Done()
			worker := &FineTuningWorker{ProviderFor: providerFor, BatchSize: 10, Now: func() time.Time { return fixedNow }}
			worker.RunOnce(t.Context(), runner)
		}(runner)
	}
	wg.Wait()
	assert.Equal(t, int32(1), provider.calls.Load())
	reloaded, err := model.GetFineTuningJobByID(job.ID)
	require.NoError(t, err)
	assert.Equal(t, model.FineTuningStatusSucceeded, reloaded.Status)
	assert.Equal(t, "ft:model", reloaded.ResultModel)
	assert.Greater(t, reloaded.BillingSettledAt, int64(0))
}

func TestFineTuningWorkerDeduplicatesProviderEvents(t *testing.T) {
	prepareFineTuningWorkerTest(t)
	job := &model.FineTuningJob{ID: "ftjob-worker-events", TenantID: "tenant", UserID: 1, TrainingFileID: "file-a", Model: "gpt-test", ProviderType: 88}
	require.NoError(t, model.CreateFineTuningJob(job))
	provider := &stubFineTuningProvider{result: FineTuningPollResult{
		Status: model.FineTuningStatusRunning,
		Events: []model.FineTuningEvent{{UpstreamEventID: "provider-event-1", Level: "info", Message: "running"}},
	}}
	worker := &FineTuningWorker{ProviderFor: func(int) (FineTuningProvider, error) { return provider, nil }, BatchSize: 10, Now: time.Now}
	first := worker.RunOnce(t.Context(), "runner-a")
	assert.Equal(t, 1, first.Completed)
	require.NoError(t, model.DB.Model(&model.FineTuningJob{}).Where("id = ?", job.ID).Updates(map[string]any{"poll_after": 0, "poll_lease_until": 0}).Error)
	second := worker.RunOnce(t.Context(), "runner-b")
	assert.Equal(t, 1, second.Completed)

	var count int64
	require.NoError(t, model.DB.Model(&model.FineTuningEvent{}).Where("job_id = ?", job.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	assert.Equal(t, int32(2), provider.calls.Load())
}

func TestFineTuningWorkerBacksOffOnProviderFailure(t *testing.T) {
	prepareFineTuningWorkerTest(t)
	job := &model.FineTuningJob{ID: "ftjob-worker-failure", TenantID: "tenant", UserID: 1, TrainingFileID: "file-a", Model: "gpt-test", ProviderType: 99}
	require.NoError(t, model.CreateFineTuningJob(job))
	provider := &stubFineTuningProvider{err: assert.AnError}
	now := time.Unix(time.Now().Unix(), 0)
	worker := &FineTuningWorker{ProviderFor: func(int) (FineTuningProvider, error) { return provider, nil }, BatchSize: 10, Now: func() time.Time { return now }}
	result := worker.RunOnce(t.Context(), "runner-a")
	assert.Equal(t, 1, result.Failed)
	reloaded, err := model.GetFineTuningJobByID(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, reloaded.PollAttempts)
	assert.Greater(t, reloaded.PollAfter, now.Unix())
	assert.Empty(t, reloaded.PollLockedBy)
}

func prepareFineTuningWorkerTest(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.FineTuningJob{}, &model.FineTuningEvent{}))
	require.NoError(t, model.DB.Exec("DELETE FROM fine_tuning_events").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM fine_tuning_jobs").Error)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM fine_tuning_events")
		model.DB.Exec("DELETE FROM fine_tuning_jobs")
	})
}
