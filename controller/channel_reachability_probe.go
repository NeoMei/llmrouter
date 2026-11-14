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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/advancedcustom"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
)

const (
	channelReachabilityTimeout   = 5 * time.Second
	channelReachabilityBodyLimit = 64 * 1024
	channelDeepTestHeader        = "X-Channel-Deep-Test"
)

// channelReachabilityResult describes one read-only model-list probe. neutral
// means the upstream answered but does not support this metadata endpoint;
// neither neutral nor transient results may change channel health.
type channelReachabilityResult struct {
	reachable bool
	success   bool
	neutral   bool
	transient bool
	skipped   bool
	err       error
}

// channelReachabilityRequest is deliberately a bodyless GET. Scheduled
// keepalive, queued channel_test tasks and "test all channels" can only reach
// this function; they never have a way to request a generation probe.
type channelReachabilityRequest struct {
	url            string
	headers        http.Header
	key            string
	responseFormat string
}

func firstEnabledChannelKey(channel *model.Channel) string {
	for index, key := range channel.GetKeys() {
		if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled {
			continue
		}
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// appendModelListPath preserves the configured raw query, including its order
// and escaping, because some providers or gateway proxies authenticate with
// signed query parameters. It only appends the expected suffix to the path.
func appendModelListPath(baseURL, suffix string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	normalizedSuffix := "/" + strings.TrimLeft(suffix, "/")
	trimmedPath := strings.TrimRight(parsed.Path, "/")
	// Only recognize a version-prefix base URL when its path actually ends in
	// the version segment, such as /v1 -> /v1/models or /api -> /api/tags.
	versionPrefix := strings.TrimSuffix(strings.TrimSuffix(normalizedSuffix, "/models"), "/tags")
	if versionPrefix != "" && strings.HasSuffix(trimmedPath, strings.TrimSuffix(versionPrefix, "/")) {
		parsed.Path = trimmedPath + strings.TrimPrefix(normalizedSuffix, strings.TrimSuffix(versionPrefix, "/"))
	} else {
		parsed.Path = trimmedPath + normalizedSuffix
	}
	parsed.RawPath = ""
	return parsed.String()
}

// redactReachabilityURL is used only in user-visible errors and logs. The
// outbound request must always receive the original, unmodified URL.
func redactReachabilityURL(rawURL, key string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil {
		return "[REDACTED]"
	}
	if key = strings.TrimSpace(key); key != "" {
		if parsed.RawQuery != "" {
			// Query parameters can contain the key, a derived signature, or a
			// secret in the parameter name. The whole query is redacted in errors.
			parsed.RawQuery = "REDACTED"
		}
		parsed.Path = strings.ReplaceAll(parsed.Path, key, "[REDACTED]")
		parsed.User = nil
		parsed.Fragment = ""
	}
	parsed.RawPath = ""
	return parsed.String()
}

// preserveConfiguredRawQuery transfers the exact signed query from an operator
// configured base URL to a fixed provider metadata endpoint. Never forward a
// plan URL's query: the channel configuration is the authenticated source.
func preserveConfiguredRawQuery(providerURL, configuredBaseURL string) string {
	configured, err := url.Parse(strings.TrimSpace(configuredBaseURL))
	if err != nil || configured == nil || configured.RawQuery == "" {
		return providerURL
	}
	provider, err := url.Parse(providerURL)
	if err != nil || provider == nil {
		return providerURL
	}
	provider.RawQuery = configured.RawQuery
	provider.RawPath = ""
	return provider.String()
}

func buildChannelReachabilityRequest(channel *model.Channel) (channelReachabilityRequest, error) {
	request := channelReachabilityRequest{responseFormat: "openai"}
	key := firstEnabledChannelKey(channel)
	if key == "" {
		return request, errors.New("no available channel key")
	}
	request.key = key
	baseURL := strings.TrimSpace(channel.GetBaseURL())
	if baseURL == "" && channel.Type >= 0 && channel.Type < len(constant.ChannelBaseURLs) {
		baseURL = constant.ChannelBaseURLs[channel.Type]
	}

	switch channel.Type {
	case constant.ChannelTypeCodex, constant.ChannelTypeGateway:
		return request, errors.New("this channel type has no safe read-only metadata endpoint")
	case constant.ChannelTypeAdvancedCustom:
		info := &relaycommon.RelayInfo{
			RelayFormat:    types.RelayFormatOpenAI,
			RelayMode:      relayconstant.RelayModeUnknown,
			RequestURLPath: dto.AdvancedCustomModelListPath,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:          constant.ChannelTypeAdvancedCustom,
				ChannelBaseUrl:       baseURL,
				ApiKey:               key,
				ChannelOtherSettings: channel.GetOtherSettings(),
			},
		}
		modelListURL, headers, err := (&advancedcustom.Adaptor{}).BuildModelListRequest(info)
		if err != nil {
			return request, sanitizeFetchModelsError(err, key)
		}
		if err := applyFetchModelsHeaderOverrides(channel, key, headers); err != nil {
			return request, sanitizeFetchModelsError(err, key)
		}
		parsed, err := url.Parse(modelListURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || strings.TrimSpace(parsed.Path) == "" {
			return request, errors.New("advanced custom model-list URL is invalid")
		}
		request.url = modelListURL
		request.headers = headers
		return request, nil
	}

	headers, err := buildFetchModelsHeaders(channel, key)
	if err != nil {
		return request, sanitizeFetchModelsError(err, key)
	}
	switch channel.Type {
	case constant.ChannelTypeGemini:
		request.url = appendModelListPath(baseURL, "/v1beta/models")
		headers.Set("x-goog-api-key", key)
		request.responseFormat = "gemini"
	case constant.ChannelTypeOllama:
		request.url = appendModelListPath(baseURL, "/api/tags")
		request.responseFormat = "ollama"
	case constant.ChannelTypeAli:
		parsedBase, parseErr := url.Parse(strings.TrimSpace(baseURL))
		if parseErr != nil || parsedBase == nil || parsedBase.Scheme == "" || parsedBase.Host == "" {
			return request, errors.New("channel has no valid model-list base URL")
		}
		path := strings.TrimRight(parsedBase.Path, "/")
		if strings.Contains(path, "/services/") {
			return request, errors.New("this channel type has no safe read-only metadata endpoint for a service-specific base URL")
		}
		if path != "" && path != "/" && !strings.HasSuffix(path, "/compatible-mode/v1") {
			return request, errors.New("channel has an unsupported model-list base URL path")
		}
		parsedBase.Path = "/compatible-mode/v1/models"
		parsedBase.RawPath = ""
		request.url = parsedBase.String()
	case constant.ChannelTypeZhipu_v4:
		if plan, ok := constant.ChannelSpecialBases[baseURL]; ok && plan.OpenAIBaseURL != "" {
			request.url = preserveConfiguredRawQuery(appendModelListPath(plan.OpenAIBaseURL, "/models"), baseURL)
		} else {
			request.url = appendModelListPath(baseURL, "/api/paas/v4/models")
		}
	case constant.ChannelTypeVolcEngine:
		if plan, ok := constant.ChannelSpecialBases[baseURL]; ok && plan.OpenAIBaseURL != "" {
			request.url = preserveConfiguredRawQuery(appendModelListPath(plan.OpenAIBaseURL, "/models"), baseURL)
		} else {
			request.url = appendModelListPath(baseURL, "/v1/models")
		}
	case constant.ChannelTypeMoonshot:
		if plan, ok := constant.ChannelSpecialBases[baseURL]; ok && plan.OpenAIBaseURL != "" {
			request.url = preserveConfiguredRawQuery(appendModelListPath(plan.OpenAIBaseURL, "/v1/models"), baseURL)
		} else {
			request.url = appendModelListPath(baseURL, "/v1/models")
		}
	case constant.ChannelTypeAws, constant.ChannelTypeVertexAi, constant.ChannelTypeBaidu,
		constant.ChannelTypeBaiduV2, constant.ChannelTypeTencent, constant.ChannelTypeXunfei,
		constant.ChannelTypeSunoAPI, constant.ChannelTypeDify, constant.ChannelTypeCoze,
		constant.ChannelTypeKling, constant.ChannelTypeJimeng, constant.ChannelTypeVidu,
		constant.ChannelTypeSora, constant.ChannelTypeMidjourney, constant.ChannelTypeMidjourneyPlus:
		return request, errors.New("this channel type has no safe read-only metadata endpoint")
	default:
		request.url = appendModelListPath(baseURL, "/v1/models")
	}
	if !hasKnownModelListBaseURL(channel, baseURL) {
		return request, errors.New("this channel type has no verified read-only model-list endpoint")
	}
	if request.url == "" {
		return request, errors.New("channel has no valid model-list base URL")
	}
	request.headers = headers
	return request, nil
}

// hasKnownModelListBaseURL limits unattended probes to the provider families
// whose model-list endpoint is supported by the product's own discovery code.
// A custom base URL is still allowed for OpenAI-compatible channels because it
// is an explicit operator setting.
func hasKnownModelListBaseURL(channel *model.Channel, baseURL string) bool {
	if baseURL == "" {
		return false
	}
	switch channel.Type {
	case constant.ChannelTypeOpenAI, constant.ChannelTypeCustom, constant.ChannelTypeAnthropic,
		constant.ChannelTypeAli, constant.ChannelTypeGemini, constant.ChannelTypeOllama,
		constant.ChannelTypeZhipu_v4, constant.ChannelTypeVolcEngine, constant.ChannelTypeMoonshot,
		constant.ChannelTypeOpenRouter, constant.ChannelTypeSiliconFlow, constant.ChannelTypeDeepSeek,
		constant.ChannelTypeMistral, constant.ChannelTypeXai, constant.ChannelTypeCohere,
		constant.ChannelTypeMiniMax, constant.ChannelTypePerplexity, constant.ChannelTypeLingYiWanWu,
		constant.ChannelTypeFastGPT, constant.ChannelTypeAIProxy, constant.ChannelTypeOhMyGPT,
		constant.ChannelTypeOpenAIMax, constant.ChannelTypeAILS, constant.ChannelTypeAPI2GPT,
		constant.ChannelTypeAIGC2D, constant.ChannelTypeAIProxyLibrary, constant.ChannelTypePaLM,
		constant.ChannelType360, constant.ChannelTypeZhipu, constant.ChannelTypeMokaAI,
		constant.ChannelTypeSubmodel, constant.ChannelTypeXinference, constant.ChannelTypeReplicate,
		constant.ChannelTypePaddleOCR:
		return true
	default:
		return false
	}
}

// hasValidModelListBody rejects HTML login pages, proxy notices and unrelated
// JSON from being treated as authoritative provider metadata.
func hasValidModelListBody(format string, body []byte) bool {
	var response struct {
		Data    *[]OpenAIModel `json:"data"`
		Models  *[]struct{}    `json:"models"`
		Success *bool          `json:"success"`
	}
	if err := common.Unmarshal(body, &response); err != nil {
		return false
	}
	if response.Success != nil && !*response.Success {
		return false
	}
	switch format {
	case "gemini":
		return response.Models != nil
	case "ollama":
		return response.Models != nil
	default:
		return response.Data != nil
	}
}

func probeChannelReachability(ctx context.Context, channel *model.Channel) channelReachabilityResult {
	probe, err := buildChannelReachabilityRequest(channel)
	if err != nil {
		return channelReachabilityResult{skipped: true, err: sanitizeFetchModelsError(err, probe.key)}
	}
	if probe.url == "" {
		return channelReachabilityResult{skipped: true, err: errors.New("configured model-list URL is invalid")}
	}
	parsed, parseErr := url.Parse(probe.url)
	if parseErr != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path == "" {
		return channelReachabilityResult{skipped: true, err: errors.New("configured model-list URL is invalid")}
	}
	requestCtx, cancel := context.WithTimeout(ctx, channelReachabilityTimeout)
	defer cancel()
	// Send the exact configured URL: redacting the URL before the request would
	// replace real query credentials and break signed query parameters.
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, probe.url, nil)
	if err != nil {
		return channelReachabilityResult{err: sanitizeFetchModelsError(err, probe.key)}
	}
	for name, values := range probe.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
		if strings.EqualFold(name, "Host") {
			request.Host = probe.headers.Get(name)
		}
	}
	client, err := service.GetHttpClientWithProxy(channel.GetSetting().Proxy)
	if err != nil {
		return channelReachabilityResult{err: sanitizeFetchModelsError(err, probe.key)}
	}
	// A shared client is never mutated globally. No redirects can turn a
	// provider check into a successful response from an unrelated page.
	probeClient := *client
	probeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := probeClient.Do(request)
	if err != nil {
		return channelReachabilityResult{err: fmt.Errorf("upstream request failed for %s: %s",
			redactReachabilityURL(probe.url, probe.key),
			sanitizeFetchModelsError(err, probe.key).Error(),
		)}
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, channelReachabilityBodyLimit+1))
	if readErr != nil {
		return channelReachabilityResult{err: fmt.Errorf("upstream response read failed for %s: %s",
			redactReachabilityURL(probe.url, probe.key),
			sanitizeFetchModelsError(readErr, probe.key).Error(),
		)}
	}
	if len(body) > channelReachabilityBodyLimit {
		return channelReachabilityResult{reachable: true, neutral: true}
	}
	switch {
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return channelReachabilityResult{neutral: true}
	case response.StatusCode >= 200 && response.StatusCode < 300:
		if contentType := response.Header.Get("Content-Type"); contentType != "" && !strings.Contains(strings.ToLower(contentType), "json") {
			return channelReachabilityResult{reachable: true, neutral: true}
		}
		if !hasValidModelListBody(probe.responseFormat, bytes.TrimSpace(body)) {
			return channelReachabilityResult{reachable: true, neutral: true}
		}
		return channelReachabilityResult{reachable: true, success: true}
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return channelReachabilityResult{err: fmt.Errorf("upstream authentication failed (HTTP %d)", response.StatusCode)}
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented:
		return channelReachabilityResult{reachable: true, neutral: true}
	case response.StatusCode == http.StatusTooManyRequests:
		return channelReachabilityResult{reachable: true, transient: true}
	default:
		return channelReachabilityResult{err: fmt.Errorf("upstream returned HTTP %d", response.StatusCode)}
	}
}

// performChannelReachabilityChecks is the only unattended channel-test loop.
// It records response times but never enables, disables, bills or generates
// anything. Neutral, transient and unsupported outcomes are kept distinct from
// authoritative success in the task summary.
func performChannelReachabilityChecks(ctx context.Context, channels []*model.Channel, report func(processed, total int)) channelTestSummary {
	summary := channelTestSummary{}
	total := len(channels)
	if ctx == nil {
		ctx = context.Background()
	}
	for index, channel := range channels {
		if ctx.Err() != nil {
			break
		}
		if report != nil {
			report(index, total)
		}
		if channel.Status == common.ChannelStatusManuallyDisabled {
			continue
		}
		tik := time.Now()
		result := probeChannelReachability(ctx, channel)
		if ctx.Err() != nil {
			break
		}
		summary.Tested++
		switch {
		case result.skipped:
			summary.Inconclusive++
		case result.err != nil:
			summary.Failed++
		case result.success:
			summary.Succeeded++
			channel.UpdateResponseTime(time.Since(tik).Milliseconds())
		default:
			summary.Inconclusive++
		}
		if common.RequestInterval > 0 {
			wait := common.RequestInterval
			if wait > channelReachabilityTimeout {
				wait = channelReachabilityTimeout
			}
			select {
			case <-ctx.Done():
				return summary
			case <-time.After(wait):
			}
		}
	}
	if report != nil && ctx.Err() == nil {
		report(total, total)
	}
	return summary
}
