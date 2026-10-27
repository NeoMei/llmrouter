package service

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProductSubscriptionOnlyRetainsOriginalEntitlementWithoutSecondWalletCharge(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/subscription.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.ProductFundingAccount{}, &model.ProductRuntimeCall{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, db.Create(&model.User{Id: 42, Username: "subscriber", Status: common.UserStatusEnabled, Quota: 0}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 7, UserId: 42, Key: "original-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}).Error)
	require.NoError(t, db.Create(&model.ProductFundingAccount{UserID: 42, AccountID: "payer", State: "central"}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPlan{Id: 1, Title: "original-plan"}).Error)
	require.NoError(t, db.Create(&model.UserSubscription{Id: 1, UserId: 42, PlanId: 1, Status: "active", AmountTotal: 100, StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix()}).Error)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{RequestId: "subscription-call", UserId: 42, TokenId: 7, TokenKey: "original-key", OriginModelName: "m", UserSetting: dto.UserSetting{BillingPreference: "subscription_only"}, ForcePreConsume: true}
	require.Nil(t, PreConsumeBilling(ctx, 40, info))
	require.Equal(t, BillingSourceSubscription, info.BillingSource)
	require.NoError(t, info.Billing.Settle(20))
	var sub model.UserSubscription
	require.NoError(t, db.First(&sub, 1).Error)
	require.EqualValues(t, 20, sub.AmountUsed)
	var token model.Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 80, token.RemainQuota)
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	require.Zero(t, user.Quota)
}
