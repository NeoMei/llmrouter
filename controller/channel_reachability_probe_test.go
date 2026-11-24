/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReachabilityProbeKeepsOriginalSignedQuery(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	var receivedRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
	}))
	t.Cleanup(server.Close)
	baseURL := server.URL + "/compatible-mode/v1?b=two%20words&a=1&a=2"
	channel := &model.Channel{
		Type: constant.ChannelTypeAli, Name: "query auth", Key: "fixture-key", BaseURL: &baseURL,
		Status: common.ChannelStatusEnabled,
	}
	service.InitHttpClient()

	result := probeChannelReachability(context.Background(), channel)

	require.True(t, result.success, result.err)
	assert.Equal(t, "b=two%20words&a=1&a=2", receivedRawQuery, "the provider must receive the exact configured signed query")
	request, err := buildChannelReachabilityRequest(channel)
	require.NoError(t, err)
	appended, err := url.Parse(request.url)
	require.NoError(t, err)
	assert.Equal(t, "/compatible-mode/v1/models", appended.Path)
	assert.Equal(t, "b=two%20words&a=1&a=2", appended.RawQuery)
}

func TestReachabilityProbeRejectsAliServiceSpecificAndVersionedPaths(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		baseURL string
	}{
		{name: "video service path", baseURL: "https://dashscope.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis"},
		{name: "generic api version path", baseURL: "https://dashscope.aliyuncs.com/api/v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			channel := &model.Channel{
				Type: constant.ChannelTypeAli, Name: "unsupported base URL", Key: "fixture-key",
				BaseURL: &testCase.baseURL, Status: common.ChannelStatusEnabled,
			}
			_, err := buildChannelReachabilityRequest(channel)
			require.Error(t, err, "a service-specific or duplicate version path must never be appended blindly")
			assert.NotContains(t, err.Error(), "compatible-mode/v1/models")
		})
	}
}

func TestChannel107ProductionAliBaseURLUsesSingleCompatibleModePath(t *testing.T) {
	baseURL := "https://dashscope.aliyuncs.com"
	channel := &model.Channel{
		Type: constant.ChannelTypeAli, Name: "channel 107", Key: "fixture-key",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled,
	}
	request, err := buildChannelReachabilityRequest(channel)
	require.NoError(t, err)
	assert.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1/models", request.url)
}

func TestReachabilityProbeSendsAdvancedCustomQueryCredential(t *testing.T) {
	var receivedRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
	}))
	t.Cleanup(server.Close)
	config := dto.AdvancedCustomConfig{
		Routes: []dto.AdvancedCustomRoute{{
			IncomingPath: dto.AdvancedCustomModelListPath,
			UpstreamPath: server.URL + "/v1/models?keep=original",
			Auth: &dto.AdvancedCustomRouteAuth{
				Type:  dto.AdvancedCustomAuthTypeQuery,
				Name:  "access_token",
				Value: "{api_key}",
			},
		}},
	}
	settings := dto.ChannelOtherSettings{AdvancedCustom: &config}
	channel := &model.Channel{
		Type: constant.ChannelTypeAdvancedCustom, Name: "query auth", Key: "upstream-secret",
		Status: common.ChannelStatusEnabled,
	}
	channel.SetOtherSettings(settings)
	service.InitHttpClient()

	result := probeChannelReachability(context.Background(), channel)

	require.True(t, result.success, result.err)
	query, err := url.ParseQuery(receivedRawQuery)
	require.NoError(t, err)
	assert.Equal(t, "original", query.Get("keep"))
	assert.Equal(t, "upstream-secret", query.Get("access_token"))
	assert.NotContains(t, receivedRawQuery, "[REDACTED]")
}

func TestReachabilityProbeRedactsCredentialFromNetworkError(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	config := dto.AdvancedCustomConfig{
		Routes: []dto.AdvancedCustomRoute{{
			IncomingPath: dto.AdvancedCustomModelListPath,
			UpstreamPath: "http://127.0.0.1:1/v1/models?keep=original",
			Auth: &dto.AdvancedCustomRouteAuth{
				Type:  dto.AdvancedCustomAuthTypeQuery,
				Name:  "access_token",
				Value: "{api_key}",
			},
		}},
	}
	settings := dto.ChannelOtherSettings{AdvancedCustom: &config}
	channel := &model.Channel{
		Type: constant.ChannelTypeAdvancedCustom, Name: "query auth", Key: "upstream-secret",
		Status: common.ChannelStatusEnabled,
	}
	channel.SetOtherSettings(settings)
	service.InitHttpClient()

	result := probeChannelReachability(context.Background(), channel)

	require.Error(t, result.err)
	assert.NotContains(t, result.err.Error(), "upstream-secret")
	assert.Contains(t, result.err.Error(), "REDACTED")
}

func TestReachabilityProbeRequiresStructuredModelListResponse(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	for _, testCase := range []struct {
		name        string
		contentType string
		body        string
		wantSuccess bool
		wantNeutral bool
	}{
		{name: "openai models", contentType: "application/json", body: `{"data":[]}`, wantSuccess: true},
		{name: "wrong provider schema", contentType: "application/json", body: `{"models":[]}`, wantNeutral: true},
		{name: "login page", contentType: "text/html", body: `<html>sign in</html>`, wantNeutral: true},
		{name: "unrelated json", contentType: "application/json", body: `{"ok":true}`, wantNeutral: true},
		{name: "malformed json", contentType: "application/json", body: `{"data":`, wantNeutral: true},
		{name: "failed status", contentType: "application/json", body: `{"success":false,"data":[]}`, wantNeutral: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", testCase.contentType)
				_, _ = w.Write([]byte(testCase.body))
			}))
			t.Cleanup(server.Close)
			channel := &model.Channel{
				Type: constant.ChannelTypeOpenAI, Name: "model list", Key: "fixture-key", BaseURL: &server.URL,
				Status: common.ChannelStatusEnabled,
			}
			service.InitHttpClient()
			result := probeChannelReachability(context.Background(), channel)
			assert.Equal(t, testCase.wantSuccess, result.success, result.err)
			assert.Equal(t, testCase.wantNeutral, result.neutral, result.err)
		})
	}
}

func TestReachabilityProbeNeverFollowsRedirect(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	var requestCount int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		w.Header().Set("Location", "https://login.example.invalid/sign-in")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(`{"data":[{"id":"unrelated"}]}`))
	}))
	t.Cleanup(server.Close)
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "redirect", Key: "fixture-key", BaseURL: &server.URL,
		Status: common.ChannelStatusEnabled,
	}
	service.InitHttpClient()
	result := probeChannelReachability(context.Background(), channel)
	assert.False(t, result.success)
	assert.True(t, result.neutral)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, requestCount, "redirect responses must never trigger a second request")
}

func TestReachabilityProbeRejectsRedirectResponses(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://login.example.invalid/sign-in")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(`{"data":[{"id":"unrelated"}]}`))
	}))
	t.Cleanup(server.Close)
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "redirect", Key: "fixture-key", BaseURL: &server.URL,
		Status: common.ChannelStatusEnabled,
	}
	service.InitHttpClient()
	result := probeChannelReachability(context.Background(), channel)
	assert.False(t, result.success)
	assert.True(t, result.neutral)
}

func TestReachabilityProbeRejectsOversizedBodies(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), channelReachabilityBodyLimit+1))
	}))
	t.Cleanup(server.Close)
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "oversized", Key: "fixture-key", BaseURL: &server.URL,
		Status: common.ChannelStatusEnabled,
	}
	service.InitHttpClient()
	result := probeChannelReachability(context.Background(), channel)
	assert.False(t, result.success)
	assert.True(t, result.neutral)
}

func TestReachabilityProbeRejectsValidJSONPrefixWithOversizedTail(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
		_, _ = w.Write(bytes.Repeat([]byte(" "), channelReachabilityBodyLimit))
	}))
	t.Cleanup(server.Close)
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "oversized tail", Key: "fixture-key", BaseURL: &server.URL,
		Status: common.ChannelStatusEnabled,
	}
	service.InitHttpClient()
	result := probeChannelReachability(context.Background(), channel)
	assert.False(t, result.success)
	assert.True(t, result.neutral)
}

func TestReachabilityProbeSkipsUnsupportedChannelTypes(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeCodex, constant.ChannelTypeAws, constant.ChannelTypeVertexAi, constant.ChannelTypeDify} {
		channel := &model.Channel{Type: channelType, Name: "unsupported", Key: "fixture-key", Status: common.ChannelStatusEnabled}
		result := probeChannelReachability(context.Background(), channel)
		assert.True(t, result.skipped)
		require.Error(t, result.err)
		assert.Contains(t, result.err.Error(), "no safe read-only metadata endpoint")
	}
}

func TestReachabilityMetadataPaths(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		channel    int
		baseURL    string
		wantURL    string
		wantFormat string
	}{
		{name: "OpenAI version prefix", channel: constant.ChannelTypeOpenAI, baseURL: "https://provider.example/v1?z=1", wantURL: "https://provider.example/v1/models?z=1"},
		{name: "Ollama api prefix", channel: constant.ChannelTypeOllama, baseURL: "http://localhost:11434/api", wantURL: "http://localhost:11434/api/tags", wantFormat: "ollama"},
		{name: "Ollama bare host", channel: constant.ChannelTypeOllama, baseURL: "http://localhost:11434", wantURL: "http://localhost:11434/api/tags", wantFormat: "ollama"},
		{name: "Ali DashScope", channel: constant.ChannelTypeAli, baseURL: "https://dashscope.aliyuncs.com", wantURL: "https://dashscope.aliyuncs.com/compatible-mode/v1/models"},
		{name: "Ali compatible-mode prefix", channel: constant.ChannelTypeAli, baseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", wantURL: "https://dashscope.aliyuncs.com/compatible-mode/v1/models"},
		{name: "Zhipu coding plan", channel: constant.ChannelTypeZhipu_v4, baseURL: "glm-coding-plan", wantURL: "https://open.bigmodel.cn/api/coding/paas/v4/models"},
		{name: "VolcEngine usage-based", channel: constant.ChannelTypeVolcEngine, baseURL: "https://ark.cn-beijing.volces.com", wantURL: "https://ark.cn-beijing.volces.com/api/v3/models"},
		{name: "Doubao Video usage-based", channel: constant.ChannelTypeDoubaoVideo, baseURL: "https://ark.cn-beijing.volces.com", wantURL: "https://ark.cn-beijing.volces.com/api/v3/models"},
		{name: "VolcEngine coding plan", channel: constant.ChannelTypeVolcEngine, baseURL: "doubao-coding-plan", wantURL: "https://ark.cn-beijing.volces.com/api/coding/v3/models"},
		{name: "Moonshot coding plan", channel: constant.ChannelTypeMoonshot, baseURL: "kimi-coding-plan", wantURL: "https://api.kimi.com/coding/v1/models"},
		{name: "Gemini", channel: constant.ChannelTypeGemini, baseURL: "https://generativelanguage.googleapis.com", wantURL: "https://generativelanguage.googleapis.com/v1beta/models", wantFormat: "gemini"},
		{name: "Anthropic", channel: constant.ChannelTypeAnthropic, baseURL: "https://api.anthropic.com", wantURL: "https://api.anthropic.com/v1/models"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			channel := &model.Channel{Type: testCase.channel, Key: "fixture-key", BaseURL: &testCase.baseURL}
			request, err := buildChannelReachabilityRequest(channel)
			require.NoError(t, err)
			assert.Equal(t, testCase.wantURL, request.url)
			if testCase.wantFormat != "" {
				assert.Equal(t, testCase.wantFormat, request.responseFormat)
			}
		})
	}
}

func TestReachabilityProbeRespectsCancellationAndTimeout(t *testing.T) {
	oldRelayTimeout := common.RelayTimeout
	t.Cleanup(func() {
		common.RelayTimeout = oldRelayTimeout
		service.InitHttpClient()
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "slow", Key: "fixture-key", BaseURL: &server.URL,
		Status: common.ChannelStatusEnabled,
	}
	service.InitHttpClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := probeChannelReachability(ctx, channel)
	require.Error(t, result.err)
	assert.False(t, result.success)
	assert.Contains(t, result.err.Error(), "context canceled")
}

func TestReachabilityProbeKeepsRawQueryOnMalformedEscapes(t *testing.T) {
	const baseURL = "https://provider.example/v1?broken=%zz&token=secret"
	assert.Equal(t,
		"https://provider.example/v1/models?broken=%zz&token=secret",
		appendModelListPath(baseURL, "/models"),
	)
	parsed, err := url.Parse(appendModelListPath(baseURL, "/models"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(parsed.RawQuery, "broken=%zz"))
}
