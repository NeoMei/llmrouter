package service

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	fineTuningPollInterval = 15 * time.Second
	fineTuningPollLease    = 60 * time.Second
)

type FineTuningWorker struct {
	ProviderFor func(providerType int) (FineTuningProvider, error)
	Settle      func(context.Context, *model.FineTuningJob, FineTuningPollResult) error
	BatchSize   int
	Now         func() time.Time
}

type FineTuningWorkerResult struct {
	Scanned   int `json:"scanned"`
	Claimed   int `json:"claimed"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

func NewFineTuningWorker() *FineTuningWorker {
	return &FineTuningWorker{ProviderFor: FineTuningProviderFor, BatchSize: 20, Now: time.Now}
}

func (worker *FineTuningWorker) RunOnce(ctx context.Context, runnerID string) FineTuningWorkerResult {
	result := FineTuningWorkerResult{}
	if worker == nil {
		result.Failed = 1
		return result
	}
	nowFunc := worker.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc().Unix()
	jobs, err := model.ListPollableFineTuningJobs(now, worker.BatchSize)
	if err != nil {
		result.Failed = 1
		return result
	}
	result.Scanned = len(jobs)
	for index := range jobs {
		if ctx.Err() != nil {
			break
		}
		claimed, err := model.ClaimFineTuningJob(jobs[index].ID, runnerID, now, now+int64(fineTuningPollLease.Seconds()))
		if err != nil || !claimed {
			if err != nil {
				result.Failed++
			}
			continue
		}
		result.Claimed++
		claimedJob, err := model.GetFineTuningJobByID(jobs[index].ID)
		if err != nil {
			result.Failed++
			continue
		}
		providerFor := worker.ProviderFor
		if providerFor == nil {
			providerFor = FineTuningProviderFor
		}
		provider, err := providerFor(claimedJob.ProviderType)
		if err != nil {
			_ = model.FailFineTuningPoll(claimedJob.ID, runnerID, nextFineTuningPoll(now, claimedJob.PollAttempts))
			result.Failed++
			continue
		}
		pollResult, err := provider.PollFineTuningJob(ctx, claimedJob)
		if err == nil {
			err = validateFineTuningPollResult(pollResult)
		}
		if err != nil {
			_ = model.FailFineTuningPoll(claimedJob.ID, runnerID, nextFineTuningPoll(now, claimedJob.PollAttempts))
			result.Failed++
			continue
		}
		nextPollAt := now + int64(fineTuningPollInterval.Seconds())
		if model.IsFineTuningTerminal(pollResult.Status) {
			nextPollAt = 0
		}
		if err := model.CompleteFineTuningPoll(claimedJob.ID, runnerID, pollResult.Status, pollResult.ResultModel, pollResult.FailureJSON, pollResult.Events, nextPollAt); err != nil {
			result.Failed++
			continue
		}
		result.Completed++
		if model.IsFineTuningTerminal(pollResult.Status) {
			worker.settleOnce(ctx, claimedJob.ID, pollResult)
		}
	}
	return result
}

func (worker *FineTuningWorker) settleOnce(ctx context.Context, id string, pollResult FineTuningPollResult) {
	claimed, err := model.ClaimFineTuningSettlement(id)
	if err != nil || !claimed {
		return
	}
	job, err := model.GetFineTuningJobByID(id)
	if err == nil && worker.Settle != nil {
		err = worker.Settle(ctx, job, pollResult)
	}
	_ = model.FinishFineTuningSettlement(id, err == nil)
}

func nextFineTuningPoll(now int64, attempts int) int64 {
	delay := int64(fineTuningPollInterval.Seconds())
	for step := 0; step < attempts && delay < 300; step++ {
		delay *= 2
	}
	if delay > 300 {
		delay = 300
	}
	return now + delay
}

type FineTuningPollSystemTaskHandler struct {
	Worker *FineTuningWorker
}

func NewFineTuningPollSystemTaskHandler() *FineTuningPollSystemTaskHandler {
	return &FineTuningPollSystemTaskHandler{Worker: NewFineTuningWorker()}
}

func (*FineTuningPollSystemTaskHandler) Type() string            { return model.SystemTaskTypeFineTuningPoll }
func (*FineTuningPollSystemTaskHandler) Enabled() bool           { return model.HasPollableFineTuningJobs() }
func (*FineTuningPollSystemTaskHandler) Interval() time.Duration { return fineTuningPollInterval }
func (*FineTuningPollSystemTaskHandler) NewPayload() any         { return nil }

func (handler *FineTuningPollSystemTaskHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	worker := handler.Worker
	if worker == nil {
		worker = NewFineTuningWorker()
	}
	result := worker.RunOnce(ctx, runnerID+":"+task.TaskID)
	status := model.SystemTaskStatusSucceeded
	errorMessage := ""
	if ctx.Err() != nil {
		status = model.SystemTaskStatusFailed
		errorMessage = ctx.Err().Error()
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, errorMessage); err != nil {
		common.SysLog(fmt.Sprintf("fine-tuning poll task %s failed to persist result: %v", task.TaskID, err))
	}
}
