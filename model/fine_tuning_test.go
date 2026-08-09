package model

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFineTuningTransitionCASAndOwnership(t *testing.T) {
	prepareFineTuningModelTest(t)
	job := &FineTuningJob{ID: "ftjob-cas", TenantID: "tenant-a", UserID: 5, TrainingFileID: "file-a", Model: "gpt-test"}
	require.NoError(t, CreateFineTuningJob(job))

	owned, err := GetOwnedFineTuningJob("tenant-a", 5, job.ID)
	require.NoError(t, err)
	assert.Equal(t, FineTuningStatusQueued, owned.Status)
	_, err = GetOwnedFineTuningJob("tenant-a", 6, job.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	changed, err := TransitionFineTuningJob(job.ID, []string{FineTuningStatusQueued}, FineTuningStatusRunning, nil)
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = TransitionFineTuningJob(job.ID, []string{FineTuningStatusQueued}, FineTuningStatusCancelled, nil)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestFineTuningEventsAreStableAndIdempotent(t *testing.T) {
	prepareFineTuningModelTest(t)
	job := &FineTuningJob{ID: "ftjob-events", TenantID: "tenant", UserID: 1, TrainingFileID: "file-a", Model: "gpt-test"}
	require.NoError(t, CreateFineTuningJob(job))

	first, created, err := AppendFineTuningEvent(&FineTuningEvent{JobID: job.ID, UpstreamEventID: "up-1", Level: "info", Message: "queued"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, int64(1), first.Sequence)
	duplicate, created, err := AppendFineTuningEvent(&FineTuningEvent{JobID: job.ID, UpstreamEventID: "up-1", Message: "duplicate"})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, duplicate.ID)
	second, created, err := AppendFineTuningEvent(&FineTuningEvent{JobID: job.ID, UpstreamEventID: "up-2", Level: "info", Message: "running"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, int64(2), second.Sequence)

	events, err := ListFineTuningEvents("tenant", 1, job.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, []int64{1, 2}, []int64{events[0].Sequence, events[1].Sequence})
	after, err := ListFineTuningEvents("tenant", 1, job.ID, 1, 100)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, int64(2), after[0].Sequence)
}

func TestFineTuningPollClaimAllowsOnlyOneRunner(t *testing.T) {
	prepareFineTuningModelTest(t)
	job := &FineTuningJob{ID: "ftjob-claim", TenantID: "tenant", UserID: 1, TrainingFileID: "file-a", Model: "gpt-test"}
	require.NoError(t, CreateFineTuningJob(job))
	now := common.GetTimestamp()
	var claims atomic.Int32
	var wg sync.WaitGroup
	for _, runner := range []string{"runner-a", "runner-b"} {
		wg.Add(1)
		go func(runner string) {
			defer wg.Done()
			claimed, err := ClaimFineTuningJob(job.ID, runner, now, now+60)
			if err == nil && claimed {
				claims.Add(1)
			}
		}(runner)
	}
	wg.Wait()
	assert.Equal(t, int32(1), claims.Load())
}

func TestFineTuningSettlementCAS(t *testing.T) {
	prepareFineTuningModelTest(t)
	job := &FineTuningJob{ID: "ftjob-settle", TenantID: "tenant", UserID: 1, TrainingFileID: "file-a", Model: "gpt-test", Status: FineTuningStatusSucceeded}
	require.NoError(t, CreateFineTuningJob(job))
	claimed, err := ClaimFineTuningSettlement(job.ID)
	require.NoError(t, err)
	assert.True(t, claimed)
	claimed, err = ClaimFineTuningSettlement(job.ID)
	require.NoError(t, err)
	assert.False(t, claimed)
	require.NoError(t, FinishFineTuningSettlement(job.ID, true))
}

func prepareFineTuningModelTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&FineTuningJob{}, &FineTuningEvent{}))
	require.NoError(t, DB.Exec("DELETE FROM fine_tuning_events").Error)
	require.NoError(t, DB.Exec("DELETE FROM fine_tuning_jobs").Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM fine_tuning_events")
		DB.Exec("DELETE FROM fine_tuning_jobs")
	})
}
