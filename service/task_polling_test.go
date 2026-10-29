package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taskPollingFetchAdaptor struct {
	mu           sync.Mutex
	taskIDs      []string
	keys         []string
	fetched      chan string
	blockTaskID  string
	blockStarted chan struct{}
	releaseBlock chan struct{}
	blockOnce    sync.Once
}

type sunoFailurePollingAdaptor struct {
	failReason string
}

type wanPlatformVideoAdaptor struct {
	fetches int
	url     string
	body    string
	status  int
}

func (a *wanPlatformVideoAdaptor) Init(*relaycommon.RelayInfo) {}

func (a *wanPlatformVideoAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	a.fetches++
	body := `{"output":{"task_id":"provider-wan","task_status":"SUCCEEDED","video_url":"` + a.url + `"},"usage":{"duration":5,"video_count":1}}`
	if a.body != "" {
		body = a.body
	}
	status := a.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (a *wanPlatformVideoAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, Url: a.url, Progress: "100%"}, nil
}

func (a *wanPlatformVideoAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

func (a *sunoFailurePollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *sunoFailurePollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskIDs, _ := body["ids"].([]string)
	items := make([]dto.SunoDataResponse, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		items = append(items, dto.SunoDataResponse{
			TaskID:     taskID,
			Status:     string(model.TaskStatusFailure),
			FailReason: a.failReason,
			FinishTime: time.Now().Unix(),
		})
	}

	responseBody, err := common.Marshal(dto.TaskResponse[[]dto.SunoDataResponse]{
		Code: dto.TaskSuccessCode,
		Data: items,
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a *sunoFailurePollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (a *sunoFailurePollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingFetchAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *taskPollingFetchAdaptor) FetchTask(_ string, key string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	if taskID == a.blockTaskID && a.releaseBlock != nil {
		a.blockOnce.Do(func() {
			if a.blockStarted != nil {
				close(a.blockStarted)
			}
		})
		<-a.releaseBlock
	}

	a.mu.Lock()
	a.taskIDs = append(a.taskIDs, taskID)
	a.keys = append(a.keys, key)
	a.mu.Unlock()
	if a.fetched != nil {
		select {
		case a.fetched <- taskID:
		default:
		}
	}

	response := dto.TaskResponse[model.Task]{
		Code: dto.TaskSuccessCode,
		Data: model.Task{
			TaskID:   taskID,
			Status:   model.TaskStatusInProgress,
			Progress: "30%",
		},
	}
	responseBody, err := common.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a *taskPollingFetchAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}, nil
}

func (a *taskPollingFetchAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingFetchAdaptor) fetchCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.taskIDs)
}

func (a *taskPollingFetchAdaptor) fetchedTaskIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.taskIDs...)
}

func seedTaskPollingChannel(t *testing.T, id int, disableSleep bool) {
	t.Helper()
	ch := &model.Channel{
		Id:     id,
		Type:   constant.ChannelTypeKling,
		Name:   "polling_channel",
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}
	if disableSleep {
		ch.SetOtherSettings(dto.ChannelOtherSettings{DisableTaskPollingSleep: true})
	}
	require.NoError(t, model.DB.Create(ch).Error)
}

func seedPollingTask(t *testing.T, channelID int, publicID string, upstreamID string) *model.Task {
	t.Helper()
	task := &model.Task{
		TaskID:    publicID,
		Platform:  constant.TaskPlatform("kling"),
		UserId:    1,
		ChannelId: channelID,
		Action:    constant.TaskActionGenerate,
		Status:    model.TaskStatusInProgress,
		Progress:  "30%",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: upstreamID,
		},
	}
	require.NoError(t, model.DB.Create(task).Error)
	return task
}

func TestUpdateVideoTasksDefaultSleepWaitsBetweenTasks(t *testing.T) {
	truncate(t)

	const channelID = 101
	seedTaskPollingChannel(t, channelID, false)
	first := seedPollingTask(t, channelID, "task_public_1", "upstream_1")
	second := seedPollingTask(t, channelID, "task_public_2", "upstream_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		channelID: {
			first.GetUpstreamTaskID(),
			second.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		first.GetUpstreamTaskID():  first,
		second.GetUpstreamTaskID(): second,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, adaptor.fetchCount())
}

func TestUpdateVideoTasksCanSkipPollingSleepPerChannel(t *testing.T) {
	truncate(t)

	const channelID = 102
	seedTaskPollingChannel(t, channelID, true)
	first := seedPollingTask(t, channelID, "task_public_3", "upstream_3")
	second := seedPollingTask(t, channelID, "task_public_4", "upstream_4")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		channelID: {
			first.GetUpstreamTaskID(),
			second.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		first.GetUpstreamTaskID():  first,
		second.GetUpstreamTaskID(): second,
	})

	require.NoError(t, err)
	assert.Equal(t, 2, adaptor.fetchCount())
}

func TestUpdateVideoTasksDefaultSleepDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const firstChannelID = 201
	const secondChannelID = 202
	seedTaskPollingChannel(t, firstChannelID, false)
	seedTaskPollingChannel(t, secondChannelID, false)
	firstChannelFirst := seedPollingTask(t, firstChannelID, "task_public_5", "upstream_a_1")
	firstChannelSecond := seedPollingTask(t, firstChannelID, "task_public_6", "upstream_a_2")
	secondChannelFirst := seedPollingTask(t, secondChannelID, "task_public_7", "upstream_b_1")
	secondChannelSecond := seedPollingTask(t, secondChannelID, "task_public_8", "upstream_b_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		firstChannelID: {
			firstChannelFirst.GetUpstreamTaskID(),
			firstChannelSecond.GetUpstreamTaskID(),
		},
		secondChannelID: {
			secondChannelFirst.GetUpstreamTaskID(),
			secondChannelSecond.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		firstChannelFirst.GetUpstreamTaskID():   firstChannelFirst,
		firstChannelSecond.GetUpstreamTaskID():  firstChannelSecond,
		secondChannelFirst.GetUpstreamTaskID():  secondChannelFirst,
		secondChannelSecond.GetUpstreamTaskID(): secondChannelSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_a_1", "upstream_b_1"}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksSlowChannelDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const slowChannelID = 251
	const fastChannelID = 252
	seedTaskPollingChannel(t, slowChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	slowTask := seedPollingTask(t, slowChannelID, "task_public_slow", "upstream_slow_1")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_fast_1", "upstream_fast_parallel_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_fast_2", "upstream_fast_parallel_2")

	adaptor := &taskPollingFetchAdaptor{
		fetched:      make(chan string, 4),
		blockTaskID:  slowTask.GetUpstreamTaskID(),
		blockStarted: make(chan struct{}),
		releaseBlock: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseBlockedTask := func() {
		releaseOnce.Do(func() {
			close(adaptor.releaseBlock)
		})
	}
	t.Cleanup(releaseBlockedTask)
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	errCh := make(chan error, 1)
	gopool.Go(func() {
		errCh <- UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), map[int][]string{
			slowChannelID: {
				slowTask.GetUpstreamTaskID(),
			},
			fastChannelID: {
				fastFirst.GetUpstreamTaskID(),
				fastSecond.GetUpstreamTaskID(),
			},
		}, map[string]*model.Task{
			slowTask.GetUpstreamTaskID():   slowTask,
			fastFirst.GetUpstreamTaskID():  fastFirst,
			fastSecond.GetUpstreamTaskID(): fastSecond,
		})
	})

	select {
	case <-adaptor.blockStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slow channel did not start blocking")
	}

	require.Eventually(t, func() bool {
		fetchedTaskIDs := adaptor.fetchedTaskIDs()
		return len(fetchedTaskIDs) == 2 &&
			fetchedTaskIDs[0] == fastFirst.GetUpstreamTaskID() &&
			fetchedTaskIDs[1] == fastSecond.GetUpstreamTaskID()
	}, 500*time.Millisecond, 10*time.Millisecond)

	releaseBlockedTask()
	require.NoError(t, <-errCh)
	assert.ElementsMatch(t, []string{
		slowTask.GetUpstreamTaskID(),
		fastFirst.GetUpstreamTaskID(),
		fastSecond.GetUpstreamTaskID(),
	}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksMixedChannelSleepSettings(t *testing.T) {
	truncate(t)

	const sleepyChannelID = 301
	const fastChannelID = 302
	seedTaskPollingChannel(t, sleepyChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	sleepyFirst := seedPollingTask(t, sleepyChannelID, "task_public_9", "upstream_sleepy_1")
	sleepySecond := seedPollingTask(t, sleepyChannelID, "task_public_10", "upstream_sleepy_2")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_11", "upstream_fast_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_12", "upstream_fast_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		sleepyChannelID: {
			sleepyFirst.GetUpstreamTaskID(),
			sleepySecond.GetUpstreamTaskID(),
		},
		fastChannelID: {
			fastFirst.GetUpstreamTaskID(),
			fastSecond.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		sleepyFirst.GetUpstreamTaskID():  sleepyFirst,
		sleepySecond.GetUpstreamTaskID(): sleepySecond,
		fastFirst.GetUpstreamTaskID():    fastFirst,
		fastSecond.GetUpstreamTaskID():   fastSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_sleepy_1", "upstream_fast_1", "upstream_fast_2"}, adaptor.fetchedTaskIDs())
}

func TestUpdateSunoTasksStalePollsRefundExactlyOnce(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 401, 401, 401
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 2_500
	const publicTaskID, upstreamTaskID = "suno_public_refund_once", "suno_upstream_refund_once"

	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-suno-refund-once", initialTokenQuota)
	baseURL := "https://suno.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Type:    constant.ChannelTypeSunoAPI,
		Name:    "suno_refund_once",
		Key:     "sk-suno-channel",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
	}).Error)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = publicTaskID
	task.Platform = constant.TaskPlatformSuno
	task.Status = model.TaskStatusInProgress
	task.Progress = "50%"
	task.SubmitTime = model.TaskRefundLegacyCutoff
	task.PrivateData.UpstreamTaskID = upstreamTaskID
	require.NoError(t, model.DB.Create(task).Error)

	var firstPollTask model.Task
	var staleSecondPollTask model.Task
	require.NoError(t, model.DB.First(&firstPollTask, task.ID).Error)
	require.NoError(t, model.DB.First(&staleSecondPollTask, task.ID).Error)

	adaptor := &sunoFailurePollingAdaptor{failReason: "upstream failed"}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		upstreamTaskID: &firstPollTask,
	}))
	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		upstreamTaskID: &staleSecondPollTask,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)
	assert.Equal(t, initialUserQuota+taskQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota+taskQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestPlatformWanVideoCompletionRequiresValidatedOwnedArtifact(t *testing.T) {
	truncate(t)
	const channelID = 451
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "task_wan_public", "provider-wan")
	task.FundingOwner = "platform"
	task.PlatformCallID = "wan-call"
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		NativePrice: &metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{
			{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "3", Denominator: "5"}},
			{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
		}},
		PlatformCallID:       "wan-call",
		VideoResolution:      "video_720p",
		VideoResourceOrigins: []string{"https://media.example"},
	}
	require.NoError(t, model.DB.Save(task).Error)

	oldMedia := PlatformVideoMedia
	PlatformVideoMedia = func(context.Context, string, string, int, int) (PlatformVideoArtifact, error) {
		return PlatformVideoArtifact{}, errors.New("media runtime unavailable")
	}
	t.Cleanup(func() { PlatformVideoMedia = oldMedia })
	adaptor := &wanPlatformVideoAdaptor{url: "https://media.example/video.mp4"}
	require.Error(t, updateVideoSingleTask(context.Background(), adaptor, &model.Channel{Id: channelID, Type: constant.ChannelTypeAli}, task.GetUpstreamTaskID(), map[string]*model.Task{task.GetUpstreamTaskID(): task}))
	var pending model.Task
	require.NoError(t, model.DB.First(&pending, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), pending.Status)
	assert.Empty(t, pending.NativeEvidence)
	assert.False(t, pending.NativePending)

	PlatformVideoMedia = func(_ context.Context, sourceURL, callID string, width, height int) (PlatformVideoArtifact, error) {
		require.Equal(t, "https://media.example/video.mp4", sourceURL)
		require.Equal(t, "wan-call", callID)
		require.Equal(t, 1280, width)
		require.Equal(t, 720, height)
		return PlatformVideoArtifact{ID: "resource-id", SHA256: strings.Repeat("a", 64), ContentType: "video/mp4", Size: 123, Width: width, Height: height, Seconds: 5}, nil
	}
	reloaded := &pending
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, &model.Channel{Id: channelID, Type: constant.ChannelTypeAli}, reloaded.GetUpstreamTaskID(), map[string]*model.Task{reloaded.GetUpstreamTaskID(): reloaded}))
	var done model.Task
	require.NoError(t, model.DB.First(&done, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), done.Status)
	assert.Equal(t, "resource-id", done.PrivateData.ResultResourceID)
	assert.Equal(t, strings.Repeat("a", 64), done.PrivateData.ResultSHA256)
	assert.Equal(t, int64(123), done.PrivateData.ResultSize)
	assert.Equal(t, "video/mp4", done.PrivateData.ResultContentType)
	assert.NotContains(t, done.GetResultURL(), "media.example")
	assert.True(t, done.NativePending)
	var evidence metering.Usage
	require.NoError(t, common.UnmarshalJsonStr(done.NativeEvidence, &evidence))
	assert.Equal(t, "confirmed", evidence.State)
}

func TestSweepUnrefundedFailedTasksRefundsModernTaskAndSkipsLegacy(t *testing.T) {
	truncate(t)

	const userID = 402
	const initialQuota, modernTaskQuota, legacyTaskQuota = 10_000, 1_200, 1_800
	seedUser(t, userID, initialQuota)

	modernTask := makeTask(userID, 0, modernTaskQuota, 0, BillingSourceWallet, 0)
	modernTask.TaskID = "modern_failed_pending_refund"
	modernTask.Status = model.TaskStatusFailure
	modernTask.Progress = "100%"
	modernTask.SubmitTime = model.TaskRefundLegacyCutoff
	modernTask.UpdatedAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(modernTask).Error)

	legacyTask := makeTask(userID, 0, legacyTaskQuota, 0, BillingSourceWallet, 0)
	legacyTask.TaskID = "legacy_failed_without_refund"
	legacyTask.Status = model.TaskStatusFailure
	legacyTask.Progress = "100%"
	legacyTask.SubmitTime = model.TaskRefundLegacyCutoff - 1
	legacyTask.UpdatedAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(legacyTask).Error)

	sweepUnrefundedFailedTasks(context.Background())
	sweepUnrefundedFailedTasks(context.Background())

	var reloadedModern model.Task
	var reloadedLegacy model.Task
	require.NoError(t, model.DB.First(&reloadedModern, modernTask.ID).Error)
	require.NoError(t, model.DB.First(&reloadedLegacy, legacyTask.ID).Error)
	assert.Zero(t, reloadedModern.Quota)
	assert.Equal(t, legacyTaskQuota, reloadedLegacy.Quota)
	assert.Equal(t, initialQuota+modernTaskQuota, getUserQuota(t, userID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestSweepUnrefundedFailedTasksRestoresMarkerAfterFundingFailure(t *testing.T) {
	truncate(t)

	const userID, subscriptionID, taskQuota = 404, 404, 900
	const subscriptionUsed int64 = 5_000
	seedUser(t, userID, 0)

	task := makeTask(userID, 0, taskQuota, 0, BillingSourceSubscription, subscriptionID)
	task.TaskID = "subscription_failed_pending_refund"
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.SubmitTime = model.TaskRefundLegacyCutoff
	task.UpdatedAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(task).Error)

	sweepUnrefundedFailedTasks(context.Background())

	var afterFailedRefund model.Task
	require.NoError(t, model.DB.First(&afterFailedRefund, task.ID).Error)
	assert.Equal(t, taskQuota, afterFailedRefund.Quota)
	assert.Equal(t, int64(0), countLogs(t))

	seedSubscription(t, subscriptionID, userID, 10_000, subscriptionUsed)
	require.NoError(t, model.DB.Model(&model.Task{}).
		Where("id = ?", task.ID).
		UpdateColumn("updated_at", time.Now().Add(-time.Minute).Unix()).Error)

	sweepUnrefundedFailedTasks(context.Background())

	var afterSuccessfulRetry model.Task
	require.NoError(t, model.DB.First(&afterSuccessfulRetry, task.ID).Error)
	assert.Zero(t, afterSuccessfulRetry.Quota)
	assert.Equal(t, subscriptionUsed-int64(taskQuota), getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestTimedOutWanTaskRemainsUnsettledAndDoesNotBlockNewWork(t *testing.T) {
	truncate(t)
	oldTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 1
	t.Cleanup(func() { constant.TaskTimeoutMinutes = oldTimeout })
	wan := seedPollingTask(t, 991, "wan-timeout", "wan-upstream")
	wan.Platform = constant.TaskPlatform("17")
	wan.FundingOwner = "platform"
	wan.SubmitTime = time.Now().Add(-2 * time.Minute).Unix()
	wan.Quota = 9
	require.NoError(t, model.DB.Save(wan).Error)
	other := seedPollingTask(t, 992, "later-task", "later-upstream")
	other.SubmitTime = time.Now().Unix()
	require.NoError(t, model.DB.Save(other).Error)
	sweepTimedOutTasks(context.Background())
	var got model.Task
	require.NoError(t, model.DB.First(&got, wan.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusReconcile), got.Status)
	assert.Equal(t, 9, got.Quota)
	assert.Empty(t, got.NativeEvidence)
	all := model.GetAllUnFinishSyncTasks(1)
	require.Len(t, all, 1)
	assert.Equal(t, other.ID, all[0].ID)
	assert.Empty(t, model.GetTimedOutUnfinishedTasks(time.Now().Unix()-60, 100))
}

func TestPlatformWanPollRejectsUntrustedReceipt(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"wrong_task":  {200, `{"output":{"task_id":"other","task_status":"SUCCEEDED","video_url":"https://media.example/video.mp4"},"usage":{"duration":5,"video_count":1}}`},
		"failed_http": {500, `{"output":{"task_id":"provider-wan","task_status":"SUCCEEDED","video_url":"https://media.example/video.mp4"},"usage":{"duration":5,"video_count":1}}`},
		"wrapper":     {200, `{"code":"success","data":{"task_id":"provider-wan","status":"SUCCESS"}}`},
		"oversized":   {200, strings.Repeat("x", (1<<20)+1)},
	} {
		t.Run(name, func(t *testing.T) {
			truncate(t)
			const channelID = 988
			seedTaskPollingChannel(t, channelID, true)
			task := seedPollingTask(t, channelID, "wan-private-"+name, "provider-wan")
			task.FundingOwner = "platform"
			task.PrivateData.BillingContext = &model.TaskBillingContext{NativePrice: &metering.Price{Policy: "ali_wan26_t2v_billable_duration_v1"}, VideoResolution: "video_720p"}
			require.NoError(t, model.DB.Save(task).Error)
			adaptor := &wanPlatformVideoAdaptor{status: tc.status, body: tc.body, url: "https://media.example/video.mp4"}
			assert.Error(t, updateVideoSingleTask(context.Background(), adaptor, &model.Channel{Id: channelID, Type: constant.ChannelTypeAli}, task.GetUpstreamTaskID(), map[string]*model.Task{task.GetUpstreamTaskID(): task}))
			var unchanged model.Task
			require.NoError(t, model.DB.First(&unchanged, task.ID).Error)
			assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), unchanged.Status)
			assert.Empty(t, unchanged.NativeEvidence)
		})
	}
}

func TestMissingWanChannelPreservesHoldForReconciliation(t *testing.T) {
	truncate(t)
	const channelID = 979
	task := seedPollingTask(t, channelID, "wan-missing-channel", "wan-provider-id")
	task.Platform = constant.TaskPlatform("17")
	task.FundingOwner = "platform"
	task.Quota = 9
	require.NoError(t, model.DB.Save(task).Error)
	assert.Error(t, updateVideoTasks(context.Background(), task.Platform, channelID, []string{task.GetUpstreamTaskID()}, map[string]*model.Task{task.GetUpstreamTaskID(): task}))
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusReconcile), got.Status)
	assert.Equal(t, 9, got.Quota)
	assert.Empty(t, got.NativeEvidence)
}

func TestReconcileTransientChannelRetriesBoundedAndNeverResubmits(t *testing.T) {
	truncate(t)
	oldLimit := constant.TaskQueryLimit
	constant.TaskQueryLimit = 100
	t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
	task := seedPollingTask(t, 970, "retry-wan", "original-provider-task")
	task.Platform = constant.TaskPlatform("17")
	task.FundingOwner = "platform"
	task.Status = model.TaskStatusReconcile
	task.FailReason = "provider channel temporarily unavailable"
	task.SubmitTime = time.Now().Unix()
	require.NoError(t, model.DB.Save(task).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("updated_at", time.Now().Add(-2*time.Minute).Unix()).Error)
	require.True(t, model.HasTaskPollingWork())
	seedTaskPollingChannel(t, 970, true)
	adaptor := &taskPollingFetchAdaptor{}
	old := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = old })
	RunTaskPollingOnce(context.Background(), nil)
	require.Equal(t, []string{"original-provider-task"}, adaptor.fetchedTaskIDs())
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), got.Status)
	assert.Equal(t, "original-provider-task", got.GetUpstreamTaskID())
}

func TestChannelRecoveryPastTimeoutStillQueriesOriginalTask(t *testing.T) {
	truncate(t)
	oldLimit, oldTimeout := constant.TaskQueryLimit, constant.TaskTimeoutMinutes
	constant.TaskQueryLimit, constant.TaskTimeoutMinutes = 100, 1
	t.Cleanup(func() { constant.TaskQueryLimit, constant.TaskTimeoutMinutes = oldLimit, oldTimeout })
	seedTaskPollingChannel(t, 971, true)
	task := seedPollingTask(t, 971, "retry-timeout", "original-upstream")
	task.Platform = constant.TaskPlatform("17")
	task.FundingOwner = "platform"
	task.Status = model.TaskStatusReconcile
	task.FailReason = model.TaskReconcileChannelReason
	task.SubmitTime = time.Now().Add(-5 * time.Minute).Unix()
	require.NoError(t, model.DB.Save(task).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("updated_at", time.Now().Add(-2*time.Minute).Unix()).Error)
	adaptor := &taskPollingFetchAdaptor{}
	old := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = old })
	RunTaskPollingOnce(context.Background(), nil)
	assert.Equal(t, []string{"original-upstream"}, adaptor.fetchedTaskIDs())
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), got.Status)
}

func TestPollingScopesIdenticalProviderTaskIDsByChannel(t *testing.T) {
	truncate(t)
	oldLimit := constant.TaskQueryLimit
	constant.TaskQueryLimit = 100
	t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
	seedTaskPollingChannel(t, 972, true)
	seedTaskPollingChannel(t, 973, true)
	first := seedPollingTask(t, 972, "public-first", "same-provider-id")
	second := seedPollingTask(t, 973, "public-second", "same-provider-id")
	for _, task := range []*model.Task{first, second} {
		task.SubmitTime = time.Now().Unix()
		task.Progress = "10%"
		require.NoError(t, model.DB.Save(task).Error)
	}
	adaptor := &taskPollingFetchAdaptor{}
	old := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = old })
	RunTaskPollingOnce(context.Background(), nil)
	assert.Len(t, adaptor.fetchedTaskIDs(), 2)
	for _, task := range []*model.Task{first, second} {
		var got model.Task
		require.NoError(t, model.DB.First(&got, task.ID).Error)
		assert.Equal(t, "30%", got.Progress)
	}
}

func TestChannelRetryPrioritizesOldestDueAfterRepeatedFailures(t *testing.T) {
	truncate(t)
	now := time.Now().Unix()
	for i := 0; i < 101; i++ {
		task := seedPollingTask(t, 974, fmt.Sprintf("retry-%03d", i), fmt.Sprintf("upstream-%03d", i))
		task.Status = model.TaskStatusReconcile
		task.FailReason = model.TaskReconcileChannelReason
		require.NoError(t, model.DB.Save(task).Error)
		require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).UpdateColumn("updated_at", now-180).Error)
	}
	retryUnavailableTaskChannels(context.Background())
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id LIKE ? AND status = ?", "retry-%", model.TaskStatusInProgress).Updates(map[string]any{"status": model.TaskStatusReconcile, "fail_reason": model.TaskReconcileChannelReason, "updated_at": now - model.TaskReconcileRetrySeconds - 1}).Error)
	retryUnavailableTaskChannels(context.Background())
	var last model.Task
	require.NoError(t, model.DB.Where("task_id = ?", "retry-100").First(&last).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), last.Status)
}

func TestPollingScopesIdenticalProviderTaskIDsByKeyWithinChannel(t *testing.T) {
	truncate(t)
	oldLimit := constant.TaskQueryLimit
	constant.TaskQueryLimit = 100
	t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
	seedTaskPollingChannel(t, 975, true)
	first := seedPollingTask(t, 975, "same-channel-first", "same-provider-id")
	second := seedPollingTask(t, 975, "same-channel-second", "same-provider-id")
	for i, task := range []*model.Task{first, second} {
		task.SubmitTime = time.Now().Unix()
		task.Progress = "10%"
		task.PrivateData.Key = fmt.Sprintf("key-%d", i)
		require.NoError(t, model.DB.Save(task).Error)
	}
	adaptor := &taskPollingFetchAdaptor{}
	old := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = old })
	RunTaskPollingOnce(context.Background(), nil)
	assert.Equal(t, []string{"same-provider-id", "same-provider-id"}, adaptor.fetchedTaskIDs())
	assert.ElementsMatch(t, []string{"key-0", "key-1"}, adaptor.keys)
	for _, task := range []*model.Task{first, second} {
		var got model.Task
		require.NoError(t, model.DB.First(&got, task.ID).Error)
		assert.Equal(t, "30%", got.Progress)
	}
}

func TestChannelRecoveryQueriesEvenWhenOrdinaryPollPageIsFull(t *testing.T) {
	truncate(t)
	oldLimit, oldTimeout := constant.TaskQueryLimit, constant.TaskTimeoutMinutes
	constant.TaskQueryLimit, constant.TaskTimeoutMinutes = 1, 1
	t.Cleanup(func() { constant.TaskQueryLimit, constant.TaskTimeoutMinutes = oldLimit, oldTimeout })
	seedTaskPollingChannel(t, 976, true)
	seedTaskPollingChannel(t, 977, true)
	other := seedPollingTask(t, 976, "older-normal", "other-provider")
	other.SubmitTime = time.Now().Unix()
	require.NoError(t, model.DB.Save(other).Error)
	task := seedPollingTask(t, 977, "retry-beyond-page", "original-provider")
	task.Platform = constant.TaskPlatform("17")
	task.FundingOwner = "platform"
	task.Status = model.TaskStatusReconcile
	task.FailReason = model.TaskReconcileChannelReason
	task.SubmitTime = time.Now().Add(-5 * time.Minute).Unix()
	require.NoError(t, model.DB.Save(task).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).UpdateColumn("updated_at", time.Now().Add(-2*time.Minute).Unix()).Error)
	adaptor := &taskPollingFetchAdaptor{}
	old := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = old })
	RunTaskPollingOnce(context.Background(), nil)
	assert.Contains(t, adaptor.fetchedTaskIDs(), "original-provider")
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), got.Status)
}

func TestMissingSunoChannelPreservesNativeHold(t *testing.T) {
	truncate(t)
	task := seedPollingTask(t, 978, "suno-missing-channel", "suno-provider-id")
	task.Platform = constant.TaskPlatformSuno
	task.FundingOwner = "native"
	task.Quota = 9
	require.NoError(t, model.DB.Save(task).Error)
	assert.Error(t, updateSunoTasks(context.Background(), 978, []string{task.GetUpstreamTaskID()}, map[string]*model.Task{task.GetUpstreamTaskID(): task}))
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusReconcile), got.Status)
	assert.Equal(t, model.TaskReconcileChannelReason, got.FailReason)
	assert.Equal(t, 9, got.Quota)
}

type sunoFixtureReplyAdaptor struct{ body string }

func (*sunoFixtureReplyAdaptor) Init(*relaycommon.RelayInfo) {}
func (a *sunoFixtureReplyAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(a.body))}, nil
}
func (*sunoFixtureReplyAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}
func (*sunoFixtureReplyAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

func TestSunoRetriesBackoffUnconfirmedResultsIndividually(t *testing.T) {
	for name, response := range map[string]string{
		"non_success":           `{"code":"error","message":"temporary"}`,
		"missing_one":           `{"code":"success","data":[{"task_id":"provider-a","status":"IN_PROGRESS","progress":"40%"}]}`,
		"invalid_status":        `{"code":"success","data":[{"task_id":"provider-a","status":"IN_PROGRESS","progress":"40%"},{"task_id":"provider-b","status":""}]}`,
		"duplicate_conflicting": `{"code":"success","data":[{"task_id":"provider-a","status":"IN_PROGRESS","progress":"40%"},{"task_id":"provider-b","status":"SUCCESS"},{"task_id":"provider-b","status":"FAILURE","fail_reason":"ambiguous"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			truncate(t)
			oldLimit := constant.TaskQueryLimit
			constant.TaskQueryLimit = 100
			t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
			base := "https://suno.invalid"
			require.NoError(t, model.DB.Create(&model.Channel{Id: 979, Type: constant.ChannelTypeSunoAPI, Key: "fixture-key", Status: common.ChannelStatusEnabled, BaseURL: &base}).Error)
			a := seedPollingTask(t, 979, "suno-a", "provider-a")
			b := seedPollingTask(t, 979, "suno-b", "provider-b")
			b.Quota = 125
			for _, task := range []*model.Task{a, b} {
				task.Platform = constant.TaskPlatformSuno
				task.FundingOwner = "native"
				task.SubmitTime = time.Now().Unix()
				task.FailReason = model.TaskReconcileRetryPending
				require.NoError(t, model.DB.Save(task).Error)
			}
			old := GetTaskAdaptorFunc
			GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return &sunoFixtureReplyAdaptor{body: response} }
			t.Cleanup(func() { GetTaskAdaptorFunc = old })
			RunTaskPollingOnce(context.Background(), nil)
			var gotA, gotB model.Task
			require.NoError(t, model.DB.First(&gotA, a.ID).Error)
			require.NoError(t, model.DB.First(&gotB, b.ID).Error)
			assert.Equal(t, model.TaskStatus(model.TaskStatusReconcile), gotB.Status)
			assert.Equal(t, model.TaskReconcileChannelReason, gotB.FailReason)
			assert.Equal(t, 125, gotB.Quota)
			assert.NotEqual(t, "100%", gotB.Progress)
			assert.Empty(t, gotB.NativeEvidence)
			if name == "non_success" {
				assert.Equal(t, model.TaskStatus(model.TaskStatusReconcile), gotA.Status)
			} else {
				assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), gotA.Status)
				assert.NotEqual(t, model.TaskReconcileRetryPending, gotA.FailReason)
			}
		})
	}
}
