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
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keepaliveRequest struct {
	Method string
	Path   string
	Body   string
	Query  string
}

func keepaliveFixture(t *testing.T, status int) (*httptest.Server, *[]keepaliveRequest) {
	t.Helper()
	var mu sync.Mutex
	requests := &[]keepaliveRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		mu.Lock()
		*requests = append(*requests, keepaliveRequest{Method: r.Method, Path: r.URL.Path, Body: string(buf[:n]), Query: r.URL.RawQuery})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/models") {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/videos") {
			_, _ = w.Write([]byte(`{"id":"probe-task","status":"succeeded","output":{"video":"https://fixture.invalid/clip.mp4"}}`))
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "probe-task") {
			_, _ = w.Write([]byte(`{"id":"probe-task","status":"succeeded","output":{"video":"https://fixture.invalid/clip.mp4"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func setupKeepaliveDB(t *testing.T) *model.User {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	db := setupModelListControllerTestDB(t)
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	oldRequestInterval := common.RequestInterval
	oldRelayTimeout := common.RelayTimeout
	oldDisable := common.AutomaticDisableChannelEnabled
	oldEnable := common.AutomaticEnableChannelEnabled
	oldSelfUseMode := operation_setting.SelfUseModeEnabled
	t.Cleanup(func() {
		// setupModelListControllerTestDB replaces package-level DB handles.
		// Restore them so later tests never use this test's closed database.
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.RedisEnabled = previousRedisEnabled
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		common.RequestInterval = oldRequestInterval
		common.RelayTimeout = oldRelayTimeout
		common.AutomaticDisableChannelEnabled = oldDisable
		common.AutomaticEnableChannelEnabled = oldEnable
		operation_setting.SelfUseModeEnabled = oldSelfUseMode
		// Rebuild the shared client with the restored global timeout settings.
		service.InitHttpClient()
	})
	common.MemoryCacheEnabled = false
	common.RequestInterval = 0
	common.RelayTimeout = 0
	common.AutomaticDisableChannelEnabled = false
	common.AutomaticEnableChannelEnabled = false
	operation_setting.SelfUseModeEnabled = false
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}, &model.Log{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	service.InitHttpClient()
	uniqueID := common.GetRandomString(8) + strconv.FormatInt(time.Now().UnixNano(), 10)
	root := &model.User{
		Username: "keepalive-root-" + uniqueID, Role: common.RoleRootUser, Group: "default",
		Status: common.UserStatusEnabled, Quota: 1000000,
		AffCode: uniqueID,
	}
	require.NoError(t, db.Create(root).Error)
	return root
}

func createKeepaliveChannel(t *testing.T, baseURL, modelName string, channelType int) *model.Channel {
	t.Helper()
	testModel := modelName
	channel := &model.Channel{
		Type: channelType, Name: "keepalive " + modelName, Key: "fixture-key", Models: modelName,
		TestModel: &testModel, Group: "default", BaseURL: &baseURL,
		Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1),
	}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel
}

func assertOnlyReadOnlyModelListRequests(t *testing.T, requests *[]keepaliveRequest) {
	t.Helper()
	require.NotEmpty(t, *requests, "the channel interface must be checked")
	for _, request := range *requests {
		assert.Equal(t, http.MethodGet, request.Method, "automatic keepalive must not use %s", request.Method)
		assert.Empty(t, request.Body, "automatic keepalive must not send a request body")
		assert.Contains(t, request.Path, "/models", "unexpected upstream path %q", request.Path)
	}
}

func runClaimedChannelTask(t *testing.T, payload string) {
	t.Helper()
	task := &model.SystemTask{
		TaskID:    "keepalive-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Type:      model.SystemTaskTypeChannelTest,
		Status:    model.SystemTaskStatusRunning,
		Payload:   payload,
		LockedBy:  "keepalive-runner",
		CreatedAt: common.GetTimestamp(),
		UpdatedAt: common.GetTimestamp(),
	}
	require.NoError(t, model.DB.Create(task).Error)
	lock := &model.SystemTaskLock{
		Type: model.SystemTaskTypeChannelTest, TaskID: task.TaskID, LockedBy: task.LockedBy,
		LockedUntil: common.GetTimestamp() + 300, UpdatedAt: common.GetTimestamp(),
	}
	require.NoError(t, model.DB.Create(lock).Error)
	channelTestHandler{}.Run(context.Background(), task, task.LockedBy)
	var stored model.SystemTask
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&stored).Error)
	assert.Equal(t, model.SystemTaskStatusSucceeded, stored.Status, stored.Error)
}

func TestScheduledKeepaliveChecksReadOnlyModelListWithoutGeneration(t *testing.T) {
	for _, modelName := range []string{"wan2.6-i2v", "qwen3-tts-flash", "qwen3-asr-flash", "qwen-image", "PaddleOCR-VL-1.6", "gpt-4o-mini"} {
		t.Run(modelName, func(t *testing.T) {
			server, requests := keepaliveFixture(t, http.StatusOK)
			setupKeepaliveDB(t)
			channelType := constant.ChannelTypeAli
			if modelName == "PaddleOCR-VL-1.6" {
				channelType = constant.ChannelTypePaddleOCR
			}
			createKeepaliveChannel(t, server.URL, modelName, channelType)

			runClaimedChannelTask(t, "")

			assertOnlyReadOnlyModelListRequests(t, requests)
			var logs int64
			require.NoError(t, model.DB.Model(&model.Log{}).Count(&logs).Error)
			assert.Zero(t, logs, "a read-only check must not write billing logs")
		})
	}
}

func TestLegacyAndForgedDeepChannelTaskPayloadsStayReadOnly(t *testing.T) {
	payloads := []string{
		`{"mode":"scheduled_all","notify":true}`,
		`{"mode":"scheduled_all","notify":true,"deep":true}`,
	}
	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			server, requests := keepaliveFixture(t, http.StatusOK)
			setupKeepaliveDB(t)
			createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)

			runClaimedChannelTask(t, payload)

			assertOnlyReadOnlyModelListRequests(t, requests)
		})
	}
}

func TestSingleChannelDefaultTestAndExplicitDeepOptIn(t *testing.T) {
	t.Run("default uses read-only check", func(t *testing.T) {
		server, requests := keepaliveFixture(t, http.StatusOK)
		user := setupKeepaliveDB(t)
		createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)

		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "id", Value: "1"}}
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel/test/1?model=gpt-4o-mini", nil)
		ctx.Set("id", user.Id)
		TestChannel(ctx)

		assert.Equal(t, http.StatusOK, recorder.Code)
		assertOnlyReadOnlyModelListRequests(t, requests)
		var response struct {
			Success   bool   `json:"success"`
			ProbeMode string `json:"probe_mode"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.True(t, response.Success)
		assert.Equal(t, "reachability", response.ProbeMode)
	})

	t.Run("legacy GET deep query is rejected without generation", func(t *testing.T) {
		for _, deep := range []string{"true", "TRUE", "1", "false"} {
			server, requests := keepaliveFixture(t, http.StatusOK)
			setupKeepaliveDB(t)
			createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "id", Value: "1"}}
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel/test/1?model=gpt-4o-mini&deep="+deep, nil)
			TestChannel(ctx)

			assert.Empty(t, *requests, "a legacy GET must never reach any upstream endpoint")
			assert.Equal(t, http.StatusOK, recorder.Code, "business rejections must keep the legacy HTTP 200 envelope")
			var response struct {
				Success   bool   `json:"success"`
				Message   string `json:"message"`
				ProbeMode string `json:"probe_mode"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, "unsupported", response.ProbeMode)
			assert.Equal(t, "paid generation tests require the confirmed POST /deep endpoint", response.Message)
		}
	})

	t.Run("confirmed POST retains model generation probe", func(t *testing.T) {
		server, requests := keepaliveFixture(t, http.StatusOK)
		user := setupKeepaliveDB(t)
		createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)
		operation_setting.SelfUseModeEnabled = true

		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "id", Value: "1"}}
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test/1/deep?model=gpt-4o-mini", strings.NewReader(`{"confirm":true}`))
		ctx.Request.Header.Set(channelDeepTestHeader, "confirmed")
		ctx.Set("id", user.Id)
		TestChannelDeep(ctx)

		assert.Equal(t, http.StatusOK, recorder.Code)
		var response struct {
			Success   bool   `json:"success"`
			ProbeMode string `json:"probe_mode"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.True(t, response.Success)
		assert.Equal(t, "generation", response.ProbeMode)
		require.NotEmpty(t, *requests)
		assert.Equal(t, http.MethodPost, (*requests)[0].Method, "an explicit deep test must still send a generation request")
		assert.NotEmpty(t, (*requests)[0].Body)
	})

	t.Run("deep POST requires both confirmation signals", func(t *testing.T) {
		for _, testCase := range []struct {
			name   string
			header string
			body   string
		}{
			{name: "missing header", body: `{"confirm":true}`},
			{name: "missing body", header: "confirmed", body: `{}`},
			{name: "explicit false", header: "confirmed", body: `{"confirm":false}`},
			{name: "wrong header", header: "unconfirmed", body: `{"confirm":true}`},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				server, requests := keepaliveFixture(t, http.StatusOK)
				setupKeepaliveDB(t)
				createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Params = gin.Params{{Key: "id", Value: "1"}}
				ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test/1/deep?model=gpt-4o-mini", strings.NewReader(testCase.body))
				if testCase.header != "" {
					ctx.Request.Header.Set(channelDeepTestHeader, testCase.header)
				}
				TestChannelDeep(ctx)
				assert.Empty(t, *requests, "an unconfirmed paid test must never reach the upstream")
				assert.Equal(t, http.StatusOK, recorder.Code)
				var response struct {
					Success   bool   `json:"success"`
					Message   string `json:"message"`
					ProbeMode string `json:"probe_mode"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				assert.False(t, response.Success)
				assert.Equal(t, "generation", response.ProbeMode)
				if testCase.header != "confirmed" {
					assert.Equal(t, "paid generation test confirmation header is required", response.Message)
				} else {
					assert.Equal(t, "paid generation test requires confirm: true in the request body", response.Message)
				}
			})
		}
	})
}

func TestReachabilityStatusesNeverFallBackToGeneration(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, requests := keepaliveFixture(t, status)
			setupKeepaliveDB(t)
			createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)

			runClaimedChannelTask(t, "")

			assertOnlyReadOnlyModelListRequests(t, requests)
		})
	}
}

func TestTestAllChannelsEnqueuesANonDeepPayload(t *testing.T) {
	setupKeepaliveDB(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel/test", nil)

	TestAllChannels(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var task model.SystemTask
	require.NoError(t, model.DB.Where("type = ?", model.SystemTaskTypeChannelTest).First(&task).Error)
	var payload map[string]any
	require.NoError(t, common.UnmarshalJsonStr(task.Payload, &payload))
	assert.NotEqual(t, true, payload["deep"], "bulk channel test must never opt into paid generation")
	assert.Equal(t, operation_setting.ChannelTestModeScheduledAll, payload["mode"])
}

func TestReachabilityChecksNeverChangeChannelStatus(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		status     int
		wantTested bool
	}{
		{name: "success", status: http.StatusOK, wantTested: true},
		{name: "not found", status: http.StatusNotFound},
		{name: "method not allowed", status: http.StatusMethodNotAllowed},
		{name: "not implemented", status: http.StatusNotImplemented},
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "server error", status: http.StatusInternalServerError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server, requests := keepaliveFixture(t, testCase.status)
			setupKeepaliveDB(t)
			common.AutomaticDisableChannelEnabled = true
			common.AutomaticEnableChannelEnabled = true
			channel := createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).
				Update("status", common.ChannelStatusAutoDisabled).Error)

			runClaimedChannelTask(t, "")

			assertOnlyReadOnlyModelListRequests(t, requests)
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status, "a read-only check must never restore channel health")
			if testCase.wantTested {
				assert.NotZero(t, stored.TestTime)
			} else {
				assert.Zero(t, stored.TestTime, "an inconclusive probe must not record a successful response time")
			}
			var logs int64
			require.NoError(t, model.DB.Model(&model.Log{}).Count(&logs).Error)
			assert.Zero(t, logs, "reachability checks must never write billing logs")
		})
	}
}

func TestReachabilitySummarySeparatesInconclusiveResults(t *testing.T) {
	_, requests := keepaliveFixture(t, http.StatusTooManyRequests)
	setupKeepaliveDB(t)
	summary := performChannelReachabilityChecks(context.Background(), []*model.Channel{
		{Type: constant.ChannelTypeCodex, Key: "fixture-key", Status: common.ChannelStatusEnabled},
	}, nil)
	assert.Equal(t, 1, summary.Tested)
	assert.Equal(t, 1, summary.Inconclusive)
	assert.Zero(t, summary.Succeeded)
	assert.Zero(t, summary.Failed)
	assert.Empty(t, *requests)
}

func TestScheduledKeepaliveWithLegacyNotifyPayloadLeavesNoWorker(t *testing.T) {
	server, requests := keepaliveFixture(t, http.StatusOK)
	setupKeepaliveDB(t)
	createKeepaliveChannel(t, server.URL, "gpt-4o-mini", constant.ChannelTypeAli)

	runClaimedChannelTask(t, `{"mode":"scheduled_all","notify":true}`)

	assertOnlyReadOnlyModelListRequests(t, requests)
	require.Eventually(t, func() bool {
		return gopool.WorkerCount() == 0
	}, 2*time.Second, time.Millisecond, "a legacy notify payload must not start a permanent cleanup worker")
}
