package controller

import (
	"encoding/base64"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// paddleOCRProbeFixture serves the hosted jobs contract: submit returns a job id,
// status polls return a terminal state without touching billing or health.
func paddleOCRProbeFixture(t *testing.T, state string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"probe-job"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/probe-job") {
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"jobId":"probe-job","state":%q}}`, state)))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"msg":"missing"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// paddleOCRProbeFixtureSequence returns the given states in order, repeating the last.
func paddleOCRProbeFixtureSequence(t *testing.T, states ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"probe-job"}}`))
			return
		}
		index := int(polls.Add(1)) - 1
		if index >= len(states) {
			index = len(states) - 1
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"jobId":"probe-job","state":%q}}`, states[index])))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func setupPaddleOCRChannel(t *testing.T, baseURL, key string) (*gorm.DB, *model.User, *model.Channel) {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	common.MemoryCacheEnabled = false
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	user := &model.User{Username: "ocr-probe-user", Group: "default", Status: common.UserStatusEnabled, Quota: 1000000, UsedQuota: 123, RequestCount: 4}
	require.NoError(t, db.Create(user).Error)
	service.InitHttpClient()
	channel := &model.Channel{
		Type: constant.ChannelTypePaddleOCR, Name: "native OCR jobs", Key: key, Models: "PaddleOCR-VL-1.6", Group: "default",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1), ResponseTime: 987, TestTime: 1234, UsedQuota: 456,
	}
	require.NoError(t, db.Create(channel).Error)
	return db, user, channel
}

func TestPaddleOCRChannelProbeSubmitsAndPolls(t *testing.T) {
	requests := &atomic.Int32{}
	server := paddleOCRProbeFixture(t, "success", requests)
	db, user, channel := setupPaddleOCRChannel(t, server.URL, "fixture-key")

	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.NoError(t, result.localErr)

	assert.Equal(t, int32(2), requests.Load(), "probe must submit once and poll until a terminal state")
	var logs int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logs).Error)
	assert.Zero(t, logs, "a connection probe must not write billing or request logs")
	assertPaddleOCRProbeStateUnchanged(t, db, user, channel)
}

func TestPaddleOCRChannelProbeWaitsForTerminalState(t *testing.T) {
	server, requests := paddleOCRProbeFixtureSequence(t, "running", "success")
	_, user, channel := setupPaddleOCRChannel(t, server.URL, "fixture-key")

	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.NoError(t, result.localErr, "a running job must not report success before extraction finishes")
	assert.Equal(t, int32(3), requests.Load(), "one submit plus one poll per observed state")
}

func TestPaddleOCRChannelProbeReportsFailedState(t *testing.T) {
	requests := &atomic.Int32{}
	server := paddleOCRProbeFixture(t, "failed", requests)
	_, user, channel := setupPaddleOCRChannel(t, server.URL, "fixture-key")

	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.ErrorContains(t, result.localErr, "failed")
	assert.NotZero(t, requests.Load())
}

func TestPaddleOCRChannelProbeRequiresKey(t *testing.T) {
	requests := &atomic.Int32{}
	server := paddleOCRProbeFixture(t, "success", requests)
	_, user, channel := setupPaddleOCRChannel(t, server.URL, "")

	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.ErrorContains(t, result.localErr, "no available key")
	assert.Zero(t, requests.Load(), "a keyless channel must never reach the supplier")
}

func TestPaddleOCRManualProbeReturnsSuccessAndHealth(t *testing.T) {
	requests := &atomic.Int32{}
	server := paddleOCRProbeFixture(t, "success", requests)
	db, user, channel := setupPaddleOCRChannel(t, server.URL, "fixture-key")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
	ctx.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/channel/test/%d", channel.Id), nil)
	ctx.Set("id", user.Id)
	TestChannel(ctx)

	var response struct {
		Success bool    `json:"success"`
		Message string  `json:"message"`
		Time    float64 `json:"time"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, response.Success, response.Message)
	assert.Empty(t, response.Message)
	var logs int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logs).Error)
	assert.Zero(t, logs, "a manual probe must not write billing or request logs")
	var storedUser model.User
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	assert.Equal(t, user.Quota, storedUser.Quota)
}

func TestPaddleOCRBulkProbeSkipsHealthCountsAndWrites(t *testing.T) {
	for _, mode := range []string{operation_setting.ChannelTestModeScheduledAll, operation_setting.ChannelTestModePassiveRecovery} {
		t.Run(mode, func(t *testing.T) {
			requests := &atomic.Int32{}
			server := paddleOCRProbeFixture(t, "success", requests)
			db, user, channel := setupPaddleOCRChannel(t, server.URL, "fixture-key")
			oldDisable, oldEnable := common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled
			common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = true, true
			t.Cleanup(func() {
				common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = oldDisable, oldEnable
			})
			channels := []*model.Channel{}
			expectedChannels := []model.Channel{}
			for _, status := range []int{common.ChannelStatusEnabled, common.ChannelStatusAutoDisabled, common.ChannelStatusManuallyDisabled} {
				candidate := *channel
				candidate.Id = 0
				candidate.Status = status
				require.NoError(t, db.Create(&candidate).Error)
				channels = append(channels, &candidate)
				expectedChannels = append(expectedChannels, candidate)
			}
			selected := selectChannelsForAutomaticTest(channels, mode)
			summary := performChannelTests(context.Background(), selected, user.Id, true, nil)
			assert.Equal(t, channelTestSummary{}, summary, "scheduled OCR runs must stay out of health counts")
			assert.Zero(t, requests.Load())
			for index := range expectedChannels {
				assertPaddleOCRProbeStateUnchanged(t, db, user, &expectedChannels[index])
			}
		})
	}
}

func assertPaddleOCRProbeStateUnchanged(t *testing.T, db *gorm.DB, user *model.User, channel *model.Channel) {
	t.Helper()
	var stored model.Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, channel.Status, stored.Status)
	assert.Equal(t, channel.ResponseTime, stored.ResponseTime)
	assert.Equal(t, channel.TestTime, stored.TestTime)
	assert.Equal(t, channel.UsedQuota, stored.UsedQuota)
	var storedUser model.User
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	assert.Equal(t, user.Quota, storedUser.Quota)
	assert.Equal(t, user.UsedQuota, storedUser.UsedQuota)
	assert.Equal(t, user.RequestCount, storedUser.RequestCount)
	var logs int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logs).Error)
	assert.Zero(t, logs)
}

func TestChannelTestDetectsImageGenerationModels(t *testing.T) {
	channel := &model.Channel{Type: constant.ChannelTypeAli}
	assert.Equal(t, string(constant.EndpointTypeImageGeneration), normalizeChannelTestEndpoint(channel, "qwen-image-2.0-pro", ""))
	assert.Equal(t, string(constant.EndpointTypeOpenAIVideo), normalizeChannelTestEndpoint(channel, "wan2.6-t2v", ""), "video families use the video endpoint, never the image probe")
	request := buildTestRequest("qwen-image-2.0", string(constant.EndpointTypeImageGeneration), channel, false)
	imageRequest, ok := request.(*dto.ImageRequest)
	require.True(t, ok, "image endpoint must build an image request")
	assert.Equal(t, "qwen-image-2.0", imageRequest.Model)
}

func TestChannelTestBuildsAudioProbes(t *testing.T) {
	channel := &model.Channel{Type: constant.ChannelTypeAli}
	speech, ok := buildTestRequest("qwen3-tts-flash", string(constant.EndpointTypeOpenAI), channel, false).(*dto.AudioRequest)
	require.True(t, ok, "speech models must not be probed through chat completions")
	assert.Equal(t, "Cherry", speech.Voice, "speech requires a voice parameter")
	assert.NotEmpty(t, speech.Input)

	transcription, ok := buildTestRequest("qwen3-asr-flash", string(constant.EndpointTypeOpenAI), channel, false).(*dto.AudioRequest)
	require.True(t, ok, "transcription models must not be probed through chat completions")
	assert.Equal(t, "json", transcription.ResponseFormat)

	wav, err := channelProbeWAV()
	require.NoError(t, err)
	assert.Equal(t, 44+32000, len(wav), "one second of 16 kHz mono 16-bit PCM")
	fact, err := relayhelper.DecodeMediaFact("audio", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(wav))
	require.NoError(t, err)
	assert.Equal(t, "wav", fact.Format, "the probe audio must pass the relay WAV decoder")
	assert.InDelta(t, 1.0, fact.Seconds, 0.05)
	body, contentType := channelProbeASRBody("qwen3-asr-flash")
	assert.Contains(t, contentType, "multipart/form-data")
	assert.Contains(t, string(body), "probe.wav", "multipart body carries the WAV file")
}
