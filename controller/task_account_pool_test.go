package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/sora"
	"github.com/QuantumNous/new-api/relay/channel/task/suno"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnedTaskPollingUsesOriginalAccountAfterKeyReorder(t *testing.T) {
	db := setupTieredRelayBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	oldCache, oldFactory := common.MemoryCacheEnabled, service.GetTaskAdaptorFunc
	common.MemoryCacheEnabled = false
	service.GetTaskAdaptorFunc = func(constant.TaskPlatform) service.TaskPollingAdaptor { return &sora.TaskAdaptor{} }
	t.Cleanup(func() { common.MemoryCacheEnabled = oldCache; service.GetTaskAdaptorFunc = oldFactory })
	service.InitHttpClient()
	var mu sync.Mutex
	keys := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		mu.Unlock()
		assert.Equal(t, "/v1/videos/owned-task", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"owned-task","status":"in_progress","progress":30}`))
	}))
	defer upstream.Close()
	channel := &model.Channel{Id: 971, Type: constant.ChannelTypeSora, Key: "account-A\naccount-B", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	channel.SetOtherSettings(dto.ChannelOtherSettings{DisableTaskPollingSleep: true})
	require.NoError(t, db.Create(channel).Error)
	refs, _, err := model.ChannelAccountRefs(context.Background(), db, channel)
	require.NoError(t, err)
	task := &model.Task{TaskID: "public-owned-task", ChannelId: 971, UserId: 1, Status: model.TaskStatusInProgress, Progress: "20%", PrivateData: model.TaskPrivateData{Account: &refs[0], UpstreamTaskID: "owned-task"}}
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, db.Model(channel).Update("key", "account-B\naccount-A").Error)
	require.NoError(t, service.UpdateVideoTasks(context.Background(), constant.TaskPlatform("sora"), map[int][]string{971: {"owned-task"}}, map[string]*model.Task{"owned-task": task}))
	mu.Lock()
	assert.Equal(t, []string{"Bearer account-A"}, keys)
	mu.Unlock()
	require.NoError(t, db.Model(channel).Update("key", "account-B\nreplaced-A").Error)
	require.NoError(t, service.UpdateVideoTasks(context.Background(), constant.TaskPlatform("sora"), map[int][]string{971: {"owned-task"}}, map[string]*model.Task{"owned-task": task}))
	mu.Lock()
	assert.Equal(t, []string{"Bearer account-A"}, keys, "missing owner must not send with B or a key blob")
	mu.Unlock()
	var stored model.Task
	require.NoError(t, db.First(&stored, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), stored.Status)
	assert.Equal(t, refs[0].MemberID, stored.PrivateData.Account.MemberID)
}

func TestOwnedSunoTaskPollingSurvivesSingleKeyModeChange(t *testing.T) {
	db := setupTieredRelayBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	oldCache, oldFactory := common.MemoryCacheEnabled, service.GetTaskAdaptorFunc
	common.MemoryCacheEnabled = false
	service.GetTaskAdaptorFunc = func(constant.TaskPlatform) service.TaskPollingAdaptor { return &suno.TaskAdaptor{} }
	t.Cleanup(func() { common.MemoryCacheEnabled = oldCache; service.GetTaskAdaptorFunc = oldFactory })
	service.InitHttpClient()
	var mu sync.Mutex
	keys := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		mu.Unlock()
		assert.Equal(t, "/suno/fetch", r.URL.Path)
		var body struct {
			IDs []string `json:"ids"`
		}
		assert.NoError(t, common.DecodeJson(r.Body, &body))
		assert.Equal(t, []string{"owned-clip-task"}, body.IDs)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"success","data":[]}`))
	}))
	defer upstream.Close()
	ch := &model.Channel{Id: 972, Type: constant.ChannelTypeSunoAPI, Key: "account-A\naccount-B", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	require.NoError(t, db.Create(ch).Error)
	refs, _, err := model.ChannelAccountRefs(context.Background(), db, ch)
	require.NoError(t, err)
	task := &model.Task{TaskID: "public-suno-task", ChannelId: ch.Id, Status: model.TaskStatusInProgress, PrivateData: model.TaskPrivateData{Account: &refs[0], UpstreamTaskID: "owned-clip-task"}}
	require.NoError(t, db.Create(task).Error)
	// Poll the persisted task, not an in-memory-only owner fixture.
	require.NoError(t, db.First(task, task.ID).Error)
	for _, remaining := range []string{"account-B", "account-A"} {
		ch.Key, ch.ChannelInfo.IsMultiKey = remaining, false
		require.NoError(t, db.Save(ch).Error)
		require.NoError(t, service.UpdateSunoTasks(context.Background(), map[int][]string{ch.Id: {"owned-clip-task"}}, map[string]*model.Task{"owned-clip-task": task}))
		mu.Lock()
		if remaining == "account-B" {
			assert.Empty(t, keys, "a missing persisted owner must not poll with B")
		} else {
			assert.Equal(t, []string{"Bearer account-A"}, keys)
		}
		mu.Unlock()
	}
}

func TestPooledSunoContinuationRejectsBeforeSend(t *testing.T) {
	service.InitHttpClient()
	var sends atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); w.WriteHeader(400) }))
	defer upstream.Close()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/music", strings.NewReader(`{"prompt":"music","mv":"chirp-v3-0","continue_clip_id":"clip-owned-by-A","continue_at":10}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "action", Value: "music"}}
	defer common.CleanupBodyStorage(c)
	accountpool.State(c).Selected = &model.UpstreamAccountSelection{}
	info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}, ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSunoAPI, ChannelBaseUrl: upstream.URL, ApiKey: "account-B"}}
	adaptor := &suno.TaskAdaptor{}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	resp, err := adaptor.DoRequest(c, info, body)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.Zero(t, sends.Load())
	assert.Zero(t, accountpool.State(c).Sends)
	assert.False(t, accountpool.RetryAllowed(c))
}
