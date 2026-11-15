package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUnconfirmedUsageHoldsPreconsumeWithoutCleanLog(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	seedUser(t, 901, 10_000)
	seedToken(t, 902, 901, "unconfirmed-usage", 10_000)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 903, Name: "unconfirmed-usage"}).Error)
	info := &relaycommon.RelayInfo{
		UserId: 901, TokenId: 902, TokenKey: "unconfirmed-usage",
		OriginModelName: "unconfirmed-model", StartTime: time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 903},
		ForcePreConsume: true, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
		PriceData: types.PriceData{ModelRatio: 1, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
	}
	require.Nil(t, PreConsumeBilling(c, 100, info))
	var user model.User
	require.NoError(t, model.DB.First(&user, 901).Error)
	require.Equal(t, 9_900, user.Quota)

	PostTextConsumeQuota(c, info, nil, nil)
	require.True(t, info.BillingSettlementFailed)
	require.NoError(t, model.DB.First(&user, 901).Error)
	require.Equal(t, 9_900, user.Quota)
	var logs int64
	require.NoError(t, model.DB.Model(&model.Log{}).Count(&logs).Error)
	require.Zero(t, logs)
	require.False(t, info.Billing.IsSettled())

	info2 := *info
	info2.BillingSettlementFailed = false
	info2.Billing = nil
	estimated := &dto.Usage{PromptTokens: 10, BillingUsage: dto.NewEstimatedGeminiChatBillingUsage(&dto.Usage{PromptTokens: 10})}
	PostTextConsumeQuota(c, &info2, estimated, nil)
	require.True(t, info2.BillingSettlementFailed)
	require.NoError(t, model.DB.First(&user, 901).Error)
	require.Equal(t, 9_900, user.Quota)
}

func TestMissingSupplierUsageNeverUsesEstimatedCountersAsValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	info := &relaycommon.RelayInfo{
		OriginModelName: "missing-usage",
		PriceData:       types.PriceData{UsePrice: true, ModelPrice: 1},
	}
	estimated := &dto.Usage{}
	// An explicit local fallback would carry estimated prompt tokens even when
	// the vendor returned no usage object at all.
	localEstimate := &dto.Usage{PromptTokens: 42, TotalTokens: 42}
	require.False(t, hasValidatedSupplierUsage(info, nil, localEstimate))
	require.False(t, hasValidatedSupplierUsage(info, nil, estimated))
	require.True(t, hasValidatedSupplierUsage(info, &dto.Usage{TotalTokens: 1}, &dto.Usage{TotalTokens: 1}))
}

var _ = types.RelayFormatOpenAI
