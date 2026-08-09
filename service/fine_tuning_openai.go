package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
)

const fineTuningUpstreamResponseLimit = 4 << 20

type OpenAICompatibleFineTuningProvider struct {
	baseURL      string
	apiKey       string
	organization string
	client       *http.Client
}

type UpstreamFineTuningError struct {
	StatusCode int
	Body       string
}

func (err *UpstreamFineTuningError) Error() string {
	return fmt.Sprintf("fine-tuning upstream returned %d: %s", err.StatusCode, err.Body)
}

func NewOpenAICompatibleFineTuningProvider(channel *model.Channel) (*OpenAICompatibleFineTuningProvider, error) {
	if channel == nil {
		return nil, errors.New("channel is required")
	}
	key, _, keyErr := channel.GetNextEnabledKey()
	if keyErr != nil {
		return nil, keyErr
	}
	baseURL := strings.TrimRight(strings.TrimSpace(channel.GetBaseURL()), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("channel has an invalid base URL")
	}
	organization := ""
	if channel.OpenAIOrganization != nil {
		organization = *channel.OpenAIOrganization
	}
	return &OpenAICompatibleFineTuningProvider{
		baseURL: baseURL, apiKey: key, organization: organization,
		client: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func NewOpenAICompatibleFineTuningProviderForTest(baseURL, apiKey string, client *http.Client) (*OpenAICompatibleFineTuningProvider, error) {
	return NewOpenAICompatibleFineTuningProviderFromConfig(baseURL, apiKey, "", client)
}

func NewOpenAICompatibleFineTuningProviderFromConfig(baseURL, apiKey, organization string, client *http.Client) (*OpenAICompatibleFineTuningProvider, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid base URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &OpenAICompatibleFineTuningProvider{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, organization: organization, client: client}, nil
}

func (provider *OpenAICompatibleFineTuningProvider) CreateFineTuningJob(ctx context.Context, input FineTuningCreateInput) (FineTuningCreateResult, error) {
	trainingID, err := provider.uploadFile(ctx, input.TrainingFile)
	if err != nil {
		return FineTuningCreateResult{}, err
	}
	uploaded := []string{trainingID}
	cleanup := func() {
		for _, id := range uploaded {
			_, _ = provider.do(ctx, http.MethodDelete, "/v1/files/"+url.PathEscape(id), nil, "")
		}
	}
	validationID := ""
	if input.ValidationFile != nil {
		validationID, err = provider.uploadFile(ctx, *input.ValidationFile)
		if err != nil {
			cleanup()
			return FineTuningCreateResult{}, err
		}
		uploaded = append(uploaded, validationID)
	}
	payload := map[string]any{"model": input.Model, "training_file": trainingID}
	if validationID != "" {
		payload["validation_file"] = validationID
	}
	if input.Suffix != "" {
		payload["suffix"] = input.Suffix
	}
	if len(input.Hyperparameters) > 0 {
		payload["hyperparameters"] = input.Hyperparameters
	}
	body, err := json.Marshal(payload)
	if err != nil {
		cleanup()
		return FineTuningCreateResult{}, err
	}
	response, err := provider.do(ctx, http.MethodPost, "/v1/fine_tuning/jobs", bytes.NewReader(body), "application/json")
	if err != nil {
		cleanup()
		return FineTuningCreateResult{}, err
	}
	var upstream struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response, &upstream); err != nil || upstream.ID == "" {
		cleanup()
		return FineTuningCreateResult{}, errors.New("invalid fine-tuning create response")
	}
	return FineTuningCreateResult{UpstreamID: upstream.ID, Status: normalizeFineTuningStatus(upstream.Status)}, nil
}

func (provider *OpenAICompatibleFineTuningProvider) PollFineTuningJob(ctx context.Context, job *model.FineTuningJob) (FineTuningPollResult, error) {
	if job == nil || job.UpstreamID == "" {
		return FineTuningPollResult{}, errors.New("upstream fine-tuning id is required")
	}
	body, err := provider.do(ctx, http.MethodGet, "/v1/fine_tuning/jobs/"+url.PathEscape(job.UpstreamID), nil, "")
	if err != nil {
		return FineTuningPollResult{}, err
	}
	var upstream struct {
		Status         string          `json:"status"`
		FineTunedModel string          `json:"fine_tuned_model"`
		Error          json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return FineTuningPollResult{}, err
	}
	result := FineTuningPollResult{Status: normalizeFineTuningStatus(upstream.Status), ResultModel: upstream.FineTunedModel, FailureJSON: string(upstream.Error)}
	eventBody, eventErr := provider.do(ctx, http.MethodGet, "/v1/fine_tuning/jobs/"+url.PathEscape(job.UpstreamID)+"/events?limit=100", nil, "")
	if eventErr == nil {
		var page struct {
			Data []struct {
				ID        string          `json:"id"`
				CreatedAt int64           `json:"created_at"`
				Level     string          `json:"level"`
				Message   string          `json:"message"`
				Data      json.RawMessage `json:"data"`
			} `json:"data"`
		}
		if json.Unmarshal(eventBody, &page) == nil {
			for _, event := range page.Data {
				result.Events = append(result.Events, model.FineTuningEvent{UpstreamEventID: event.ID, CreatedAt: event.CreatedAt, Level: event.Level, Message: event.Message, DataJSON: string(event.Data)})
			}
		}
	}
	return result, nil
}

func (provider *OpenAICompatibleFineTuningProvider) CancelFineTuningJob(ctx context.Context, upstreamID string) (string, error) {
	body, err := provider.do(ctx, http.MethodPost, "/v1/fine_tuning/jobs/"+url.PathEscape(upstreamID)+"/cancel", nil, "application/json")
	if err != nil {
		return "", err
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}
	return normalizeFineTuningStatus(response.Status), nil
}

func (provider *OpenAICompatibleFineTuningProvider) DeleteFineTunedModel(ctx context.Context, modelID string) error {
	_, err := provider.do(ctx, http.MethodDelete, "/v1/models/"+url.PathEscape(modelID), nil, "")
	return err
}

func (provider *OpenAICompatibleFineTuningProvider) uploadFile(ctx context.Context, upload FineTuningUpload) (string, error) {
	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	contentType := writer.FormDataContentType()
	go func() {
		err := writer.WriteField("purpose", "fine-tune")
		if err == nil {
			var part io.Writer
			part, err = writer.CreateFormFile("file", path.Base(upload.Filename))
			if err == nil {
				_, err = io.Copy(part, upload.Reader)
			}
		}
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		_ = pipeWriter.CloseWithError(err)
	}()
	response, err := provider.do(ctx, http.MethodPost, "/v1/files", pipeReader, contentType)
	if err != nil {
		return "", err
	}
	var file struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response, &file); err != nil || file.ID == "" {
		return "", errors.New("invalid upstream file response")
	}
	return file.ID, nil
}

func (provider *OpenAICompatibleFineTuningProvider) do(ctx context.Context, method, endpoint string, body io.Reader, contentType string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, provider.endpoint(endpoint), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+provider.apiKey)
	if provider.organization != "" {
		request.Header.Set("OpenAI-Organization", provider.organization)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, fineTuningUpstreamResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(responseBody) > fineTuningUpstreamResponseLimit {
		return nil, errors.New("fine-tuning upstream response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &UpstreamFineTuningError{StatusCode: response.StatusCode, Body: string(responseBody)}
	}
	return responseBody, nil
}

func (provider *OpenAICompatibleFineTuningProvider) endpoint(endpoint string) string {
	base := strings.TrimRight(provider.baseURL, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(endpoint, "/v1/") {
		return base + strings.TrimPrefix(endpoint, "/v1")
	}
	return base + endpoint
}

func normalizeFineTuningStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending", "validating_files":
		return model.FineTuningStatusQueued
	case "running":
		return model.FineTuningStatusRunning
	case "succeeded":
		return model.FineTuningStatusSucceeded
	case "failed":
		return model.FineTuningStatusFailed
	case "cancelled", "canceled":
		return model.FineTuningStatusCancelled
	default:
		return status
	}
}
