package mcpbridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const maxHTTPResponseBytes = 512 * 1024

type HTTPClient struct {
	origin  *url.URL
	token   string
	models  []string
	allowed map[string]struct{}
	timeout time.Duration
	http    *http.Client
}

func NewHTTPClient(config Config, client *http.Client) (*HTTPClient, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	origin, err := url.Parse(config.Origin)
	if err != nil {
		return nil, fmt.Errorf("credential origin is invalid")
	}
	origin.Path = ""
	if client == nil {
		client = http.DefaultClient
	}
	allowed := make(map[string]struct{}, len(config.Models))
	for _, model := range config.Models {
		allowed[model] = struct{}{}
	}
	return &HTTPClient{
		origin: origin, token: config.Token, models: append([]string(nil), config.Models...),
		allowed: allowed, timeout: time.Duration(config.TimeoutSeconds) * time.Second, http: client,
	}, nil
}

func (client *HTTPClient) ListModels(ctx context.Context) ([]string, error) {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := client.do(ctx, http.MethodGet, "/v1/models", nil, &response); err != nil {
		return nil, err
	}
	available := make(map[string]struct{}, len(response.Data))
	for _, item := range response.Data {
		if item.ID == "" || len(item.ID) > 512 || hasControl(item.ID) {
			return nil, fmt.Errorf("model response is invalid")
		}
		available[item.ID] = struct{}{}
	}
	models := make([]string, 0, len(client.models))
	for _, model := range client.models {
		if _, exists := available[model]; exists {
			models = append(models, model)
		}
	}
	return models, nil
}

func (client *HTTPClient) Chat(ctx context.Context, request ChatRequest) (string, error) {
	if client == nil {
		return "", fmt.Errorf("chat client is not configured")
	}
	if _, allowed := client.allowed[request.Model]; !allowed {
		return "", fmt.Errorf("chat model is not configured")
	}
	if request.Prompt == "" || len(request.Prompt) > MaxPromptBytes || hasControlExceptWhitespace(request.Prompt) {
		return "", fmt.Errorf("chat prompt is invalid")
	}
	if request.MaxOutputTokens < 1 || request.MaxOutputTokens > 256 {
		return "", fmt.Errorf("chat output token limit is invalid")
	}
	payload, err := common.Marshal(map[string]any{
		"model": request.Model, "max_tokens": request.MaxOutputTokens, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": request.Prompt}},
	})
	if err != nil || len(payload) > MaxLineBytes {
		return "", fmt.Errorf("chat request cannot be encoded within bounds")
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := client.do(ctx, http.MethodPost, "/v1/chat/completions", payload, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("chat response has no choices")
	}
	output := response.Choices[0].Message.Content
	if len(output) > MaxOutputBytes {
		return "", fmt.Errorf("chat output exceeds 64 KiB")
	}
	return output, nil
}

func (client *HTTPClient) do(ctx context.Context, method, path string, body []byte, output any) error {
	if client == nil || client.origin == nil || client.http == nil || client.timeout <= 0 || client.timeout > 30*time.Second {
		return fmt.Errorf("HTTP client is not configured")
	}
	requestContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	endpoint := *client.origin
	endpoint.Path = path
	request, err := http.NewRequestWithContext(requestContext, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create HTTP request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("User-Agent", "llmapi-mcp-bridge/0.1.0")
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("LLMAPI request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxHTTPResponseBytes))
		return fmt.Errorf("LLMAPI status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPResponseBytes+1))
	if err != nil || len(raw) > maxHTTPResponseBytes {
		return fmt.Errorf("LLMAPI response is unreadable or too large")
	}
	if err := common.UnmarshalUseNumber(raw, output); err != nil {
		return fmt.Errorf("LLMAPI response is invalid")
	}
	return nil
}

func hasControlExceptWhitespace(value string) bool {
	for _, character := range value {
		if character < 0x20 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
		if character == 0x7f {
			return true
		}
	}
	return false
}

var _ Client = (*HTTPClient)(nil)
