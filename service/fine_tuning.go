package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/QuantumNous/new-api/model"
)

type FineTuningPollResult struct {
	Status      string
	ResultModel string
	FailureJSON string
	Events      []model.FineTuningEvent
}

// FineTuningProvider is the provider-specific read side required by the
// durable polling worker. Lifecycle operations extend this interface in the
// controller without weakening the worker contract.
type FineTuningProvider interface {
	PollFineTuningJob(ctx context.Context, job *model.FineTuningJob) (FineTuningPollResult, error)
}

type FineTuningUpload struct {
	Filename string
	Reader   io.Reader
}

type FineTuningCreateInput struct {
	Model           string
	Suffix          string
	Hyperparameters map[string]any
	TrainingFile    FineTuningUpload
	ValidationFile  *FineTuningUpload
}

type FineTuningCreateResult struct {
	UpstreamID string
	Status     string
}

type FineTuningLifecycleProvider interface {
	FineTuningProvider
	CreateFineTuningJob(ctx context.Context, input FineTuningCreateInput) (FineTuningCreateResult, error)
	CancelFineTuningJob(ctx context.Context, upstreamID string) (string, error)
	DeleteFineTunedModel(ctx context.Context, modelID string) error
}

var (
	fineTuningProvidersMu sync.RWMutex
	fineTuningProviders   = map[int]FineTuningProvider{}
)

func RegisterFineTuningProvider(providerType int, provider FineTuningProvider) {
	fineTuningProvidersMu.Lock()
	defer fineTuningProvidersMu.Unlock()
	if provider == nil {
		delete(fineTuningProviders, providerType)
		return
	}
	fineTuningProviders[providerType] = provider
}

func FineTuningProviderFor(providerType int) (FineTuningProvider, error) {
	fineTuningProvidersMu.RLock()
	defer fineTuningProvidersMu.RUnlock()
	provider := fineTuningProviders[providerType]
	if provider == nil {
		return nil, fmt.Errorf("fine-tuning provider %d is not registered", providerType)
	}
	return provider, nil
}

func FineTuningProviderForJob(job *model.FineTuningJob) (FineTuningProvider, error) {
	if job == nil {
		return nil, errors.New("fine-tuning job is required")
	}
	if provider, err := FineTuningProviderFor(job.ProviderType); err == nil {
		return provider, nil
	}
	channel, err := model.GetChannelById(job.ChannelID, true)
	if err != nil {
		return nil, err
	}
	return NewOpenAICompatibleFineTuningProvider(channel)
}

func validateFineTuningPollResult(result FineTuningPollResult) error {
	switch result.Status {
	case model.FineTuningStatusQueued, model.FineTuningStatusRunning,
		model.FineTuningStatusSucceeded, model.FineTuningStatusFailed, model.FineTuningStatusCancelled:
		return nil
	default:
		return errors.New("provider returned an invalid fine-tuning status")
	}
}
