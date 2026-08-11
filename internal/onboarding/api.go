package onboarding

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	apiTimeout       = 10 * time.Second
	maxResponseBytes = 1 << 20
	probeMaxTokens   = 16
)

type APIClient struct {
	origin *url.URL
	token  Secret
	http   *http.Client
}

func NewAPIClient(origin *url.URL, token Secret, client *http.Client) *APIClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &APIClient{origin: cloneURL(origin), token: token, http: client}
}

func (client *APIClient) Health(ctx context.Context) error {
	return client.do(ctx, http.MethodGet, "/healthz", nil, false, nil)
}

func (client *APIClient) ListModels(ctx context.Context) ([]string, error) {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := client.do(ctx, http.MethodGet, "/v1/models", nil, true, &response); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(response.Data))
	seen := make(map[string]struct{}, len(response.Data))
	for _, item := range response.Data {
		if item.ID == "" || hasControl(item.ID) {
			continue
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		models = append(models, item.ID)
	}
	return models, nil
}

func (client *APIClient) ProbeResponses(ctx context.Context, model string) error {
	return client.probe(ctx, "/v1/responses", map[string]any{
		"model": model, "input": "Reply with OK.", "max_output_tokens": probeMaxTokens, "stream": false,
	}, nil)
}

func (client *APIClient) ProbeMessages(ctx context.Context, model string) error {
	return client.probe(ctx, "/v1/messages", map[string]any{
		"model": model, "max_tokens": probeMaxTokens, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "Reply with OK."}},
	}, map[string]string{"Anthropic-Version": "2023-06-01"})
}

func (client *APIClient) ProbeChatCompletions(ctx context.Context, model string) error {
	return client.probe(ctx, "/v1/chat/completions", map[string]any{
		"model": model, "max_tokens": probeMaxTokens, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "Reply with OK."}},
	}, nil)
}

func (client *APIClient) probe(ctx context.Context, path string, payload map[string]any, headers map[string]string) error {
	model, _ := payload["model"].(string)
	if model == "" || hasControl(model) {
		return fmt.Errorf("%w: probe model is required", ErrUsage)
	}
	body, err := common.Marshal(payload)
	if err != nil || len(body) > 4096 {
		return fmt.Errorf("%w: cannot encode bounded probe", ErrUsage)
	}
	return client.doWithHeaders(ctx, http.MethodPost, path, body, true, nil, headers)
}

func (client *APIClient) do(ctx context.Context, method, path string, body []byte, authenticated bool, output any) error {
	return client.doWithHeaders(ctx, method, path, body, authenticated, output, nil)
}

func (client *APIClient) doWithHeaders(ctx context.Context, method, path string, body []byte, authenticated bool, output any, headers map[string]string) error {
	if client == nil || client.origin == nil || client.http == nil {
		return fmt.Errorf("%w: API client is not configured", ErrUsage)
	}
	normalized, err := NormalizeServerURL(client.origin.String())
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	endpoint := *normalized
	endpoint.Path = path
	request, err := http.NewRequestWithContext(requestCtx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: create API request", ErrNetwork)
	}
	request.Header.Set("User-Agent", "llmapi-onboard/"+OnboardingVersion)
	request.Header.Set("Accept", "application/json")
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+client.token.Reveal())
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: API request failed", ErrNetwork)
	}
	defer response.Body.Close()
	requestID := cleanRequestID(firstNonEmpty(response.Header.Get("X-Request-Id"), response.Header.Get("Request-Id")))
	if secret := client.token.Reveal(); secret != "" && strings.Contains(requestID, secret) {
		requestID = ""
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		if requestID != "" {
			return fmt.Errorf("%w: API status %d (request %s)", ErrNetwork, response.StatusCode, requestID)
		}
		return fmt.Errorf("%w: API status %d", ErrNetwork, response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("%w: read API response", ErrNetwork)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("%w: API response is too large", ErrNetwork)
	}
	if output != nil {
		if err := common.Unmarshal(data, output); err != nil {
			return fmt.Errorf("%w: invalid API response", ErrNetwork)
		}
	}
	return nil
}

func cleanRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || hasControl(value) {
		return ""
	}
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
