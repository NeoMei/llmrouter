package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// TaskFetchAdaptor is the polling half of an async (video) task adaptor.
type TaskFetchAdaptor interface {
	FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error)
}

const (
	videoProbePrompt    = "A white circle slowly rotates on a black background."
	videoProbeBudget    = 5 * time.Minute
	videoProbePollDelay = 10 * time.Second
)

// testVideoChannel submits one minimal clip through the channel's own async
// adaptor and polls until the supplier reports a terminal state. Polling is free;
// the supplier bills the generated clip, so this runs once per test.
func testVideoChannel(ctx context.Context, channel *model.Channel, modelName, key string) testResult {
	adaptor := getChannelTaskAdapter(channel)
	if adaptor == nil {
		return testResult{localErr: fmt.Errorf("video probe: channel %d has no async task adaptor", channel.Id)}
	}
	if isImageToVideoModel(modelName) {
		// Image-to-video needs a real seed frame; a synthetic one would silently
		// change what is being verified, so the probe refuses instead of guessing.
		return simpleTestResult(fmt.Errorf("video probe: %s is image-to-video and needs a seed image, so it cannot be auto-tested", modelName))
	}
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = constant.ChannelBaseURLs[channel.Type]
	}
	if baseURL == "" {
		return simpleTestResult(fmt.Errorf("video probe: channel %d has no base URL", channel.Id))
	}
	body, err := common.Marshal(map[string]any{"model": modelName, "prompt": videoProbePrompt})
	if err != nil {
		return testResult{localErr: err}
	}
	storage, err := common.CreateBodyStorage(body)
	if err != nil {
		return testResult{localErr: err}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(common.KeyBodyStorage, storage)
	c.Set("channel_test_probe", true)
	c.Set("platform", fmt.Sprintf("%d", channel.Type))
	// RelayInfo needs the full inner metadata structs initialized; the task
	// validator writes info.Action through RelayInfo.
	info := &relaycommon.RelayInfo{
		ChannelMeta:    &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ChannelBaseUrl: baseURL, ApiKey: key},
		PriceData:      types.PriceData{},
		TokenCountMeta: relaycommon.TokenCountMeta{},
	}
	info.UpstreamModelName = modelName
	info.OriginModelName = modelName
	info.RelayMode = relayconstant.RelayModeVideoSubmit
	// The task validator stores the parsed request on TaskRelayInfo, which
	// GenRelayInfo normally allocates; a probe must provide it too.
	info.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
	// The adaptor caches base URL and key at Init time.
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		return simpleTestResult(taskErr.Error)
	}
	requestBody, err := adaptor.BuildRequestBody(c, info)
	if err != nil {
		return testResult{localErr: err}
	}
	url, err := adaptor.BuildRequestURL(info)
	if err != nil || url == "" {
		return testResult{localErr: fmt.Errorf("video probe: channel %d has no submit URL (%v)", channel.Id, err)}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, requestBody)
	if err != nil {
		return testResult{localErr: err}
	}
	if err := adaptor.BuildRequestHeader(c, request, info); err != nil {
		return testResult{localErr: err}
	}
	resp, err := service.GetHttpClient().Do(request)
	if err != nil {
		return simpleTestResult(fmt.Errorf("video probe: submit failed (%v)", err))
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return simpleTestResult(fmt.Errorf("video probe: submit HTTP %d %s", resp.StatusCode, string(raw)))
	}
	taskID, _, taskErr := adaptor.DoResponse(c, resp, info)
	if taskErr != nil {
		return simpleTestResult(taskErr.Error)
	}
	state, err := videoProbeTaskState(ctx, adaptor, baseURL, key, taskID)
	if err != nil {
		return simpleTestResult(err)
	}
	switch state {
	case "succeed", "succeeded", "success", "completed":
		return testResult{}
	}
	return simpleTestResult(fmt.Errorf("video probe: task %s ended %q", taskID, state))
}

// getChannelTaskAdapter resolves the async adaptor that serves this channel type.
func getChannelTaskAdapter(channel *model.Channel) channel.TaskAdaptor {
	return relay.GetTaskAdaptor(constant.TaskPlatform(fmt.Sprintf("%d", channel.Type)))
}

// simpleTestResult keeps video probe failures on the managed renderer, which
// needs a context, and reports the probe message through the API error.
func simpleTestResult(err error) testResult {
	return testResult{localErr: err, newAPIError: types.NewError(err, types.ErrorCodeBadResponse)}
}

// isImageToVideoModel reports the Wan families whose request requires a source image.
func isImageToVideoModel(modelName string) bool {
	name := lowerTrim(modelName)
	return strings.Contains(name, "i2v") || strings.Contains(name, "image2video")
}

// videoProbeTaskState polls an existing task id through the adaptor's fetch wire
// until it reaches a terminal state.
func videoProbeTaskState(ctx context.Context, adaptor TaskFetchAdaptor, baseURL, key, taskID string) (string, error) {
	deadline := time.Now().Add(videoProbeBudget)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(videoProbePollDelay):
		}
		state, err := fetchVideoTaskState(adaptor, baseURL, key, taskID)
		if err != nil {
			return "", err
		}
		switch state {
		case "succeed", "succeeded", "success", "completed", "failed", "canceled", "cancelled":
			return state, nil
		}
		if time.Now().After(deadline) {
			return state, fmt.Errorf("video task %s still %q after %s", taskID, state, videoProbeBudget)
		}
	}
}

func fetchVideoTaskState(adaptor TaskFetchAdaptor, baseURL, key, taskID string) (string, error) {
	resp, err := adaptor.FetchTask(baseURL, key, map[string]any{"task_id": taskID}, "")
	if err != nil || resp == nil {
		return "", fmt.Errorf("video task %s status unavailable (%v)", taskID, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("video task %s status HTTP %d", taskID, resp.StatusCode)
	}
	return videoTaskState(raw), nil
}

// videoTaskState reads either the native Wan envelope or an OpenAI-style status.
func videoTaskState(raw []byte) string {
	var payload struct {
		Output struct {
			TaskStatus string `json:"task_status"`
		} `json:"output"`
		Status string `json:"status"`
	}
	if common.UnmarshalJsonStr(string(raw), &payload) != nil {
		return ""
	}
	if payload.Output.TaskStatus != "" {
		return lowerTrim(payload.Output.TaskStatus)
	}
	return lowerTrim(payload.Status)
}

func lowerTrim(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r != ' ' {
			out = append(out, r)
		}
	}
	return string(out)
}
