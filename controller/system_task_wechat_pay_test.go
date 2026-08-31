package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setWeChatReconcileHandlerDependencies(
	t *testing.T,
	provider service.WeChatPayProvider,
	config service.WeChatPayConfig,
	configErr error,
) {
	t.Helper()
	originalConfig := loadReadyWeChatPayReconcileConfig
	originalProvider := loadWeChatPayReconcileProvider
	loadReadyWeChatPayReconcileConfig = func() (service.WeChatPayConfig, error) {
		return config, configErr
	}
	loadWeChatPayReconcileProvider = func(context.Context) (service.WeChatPayProvider, service.WeChatPayConfig, error) {
		if configErr != nil {
			return nil, service.WeChatPayConfig{}, configErr
		}
		return provider, config, nil
	}
	t.Cleanup(func() {
		loadReadyWeChatPayReconcileConfig = originalConfig
		loadWeChatPayReconcileProvider = originalProvider
	})
}

func TestWeChatPayReconcileSystemTaskEnablementAndCadence(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
	config := validControllerWeChatPayConfig()
	setWeChatReconcileHandlerDependencies(t, &fakeControllerWeChatPayProvider{}, config, nil)
	handler := weChatPayReconcileHandler{}

	assert.Equal(t, model.SystemTaskTypeWeChatPayReconcile, handler.Type())
	assert.Equal(t, 30*time.Second, handler.Interval())
	assert.False(t, handler.Enabled())

	insertWeChatPayControllerUser(t, 950, 0)
	topUp := createControllerWeChatTopUp(t, 950, "WXNa123456789abcdef0123456789abd")
	topUp.NextReconcileTime = common.GetTimestamp()
	require.NoError(t, model.DB.Save(topUp).Error)
	assert.True(t, handler.Enabled())

	setWeChatReconcileHandlerDependencies(t, nil, config, errors.New("credentials unavailable"))
	assert.False(t, handler.Enabled())
}

func TestWeChatPayReconcileSystemTaskPersistsSummary(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
	insertWeChatPayControllerUser(t, 951, 0)
	topUp := createControllerWeChatTopUp(t, 951, "WXNb123456789abcdef0123456789abd")
	topUp.NextReconcileTime = common.GetTimestamp()
	require.NoError(t, model.DB.Save(topUp).Error)

	config := validControllerWeChatPayConfig()
	provider := &fakeControllerWeChatPayProvider{queryResult: service.WeChatPayQueryResult{
		Transaction: service.WeChatPayTransaction{
			AppID:         config.AppID,
			MchID:         config.MchID,
			OutTradeNo:    topUp.TradeNo,
			TransactionID: "4200000000202608310000000095",
			TradeType:     "NATIVE",
			TradeState:    "SUCCESS",
			Currency:      "CNY",
			AmountMinor:   topUp.PaidAmountMinor,
		},
	}}
	setWeChatReconcileHandlerDependencies(t, provider, config, nil)

	task, err := model.CreateSystemTask(model.SystemTaskTypeWeChatPayReconcile, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "wechat-runner", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, ok)

	weChatPayReconcileHandler{}.Run(context.Background(), claimed, "wechat-runner")
	reloaded, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, model.SystemTaskStatusSucceeded, reloaded.Status)
	result := service.WeChatPayReconcileSummary{}
	require.NoError(t, common.Unmarshal([]byte(reloaded.Result), &result))
	assert.Equal(t, 1, result.Claimed)
	assert.Equal(t, 1, result.Settled)
}
