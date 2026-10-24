package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/sora"
	"github.com/QuantumNous/new-api/service"
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
