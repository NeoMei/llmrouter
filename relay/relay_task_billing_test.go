package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelayTaskSubmitRetryReservesBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	for _, tc := range []struct {
		name                    string
		wallet                  int
		retryGroup              string
		wantQuota               int
		failRetry, insufficient bool
	}{
		{name: "higher group settled", wallet: 100000, retryGroup: "expensive", wantQuota: 40000},
		{name: "insufficient balance prevents retry send", wallet: 30000, retryGroup: "expensive", insufficient: true},
		{name: "lower group refunds unused reservation", wallet: 100000, retryGroup: "cheap", wantQuota: 10000},
		{name: "failed retry refunds full reservation", wallet: 100000, retryGroup: "expensive", failRetry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedDB, savedLogDB := model.DB, model.LOG_DB
			savedRedis, savedBatch := common.RedisEnabled, common.BatchUpdateEnabled
			savedDBType, savedLogDBType := common.MainDatabaseType(), common.LogDatabaseType()
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
			common.RedisEnabled, common.BatchUpdateEnabled = false, false
			t.Cleanup(func() {
				model.DB, model.LOG_DB = savedDB, savedLogDB
				common.RedisEnabled, common.BatchUpdateEnabled = savedRedis, savedBatch
				common.SetDatabaseTypes(savedDBType, savedLogDBType)
				_ = sqlDB.Close()
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
			require.NoError(t, db.Create(&model.User{Id: 951, Username: "task-retry", Quota: tc.wallet}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 952, UserId: 951, Key: "task-retry", RemainQuota: 100000}).Error)
			savedPrices := ratio_setting.ModelPrice2JSONString()
			savedGroups := ratio_setting.GroupRatio2JSONString()
			savedSpecialGroups := ratio_setting.GroupGroupRatio2JSONString()
			t.Cleanup(func() {
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(savedGroups))
				require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(savedSpecialGroups))
			})
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"task-retry-model":0.04}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"expensive":2,"cheap":0.5}`))
			require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{}`))
			var mu sync.Mutex
			var sent, held []int
			var info *relaycommon.RelayInfo
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				sent = append(sent, len(sent)+1)
				held = append(held, info.Billing.GetPreConsumedQuota())
				attempt := len(sent)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if attempt == 1 || tc.failRetry {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"retry"}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":"success","data":"job-1"}`))
			}))
			defer server.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/suno/submit/LYRICS", strings.NewReader(`{"prompt":"hello"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = gin.Params{{Key: "action", Value: constant.SunoActionLyrics}}
			c.Set("platform", string(constant.TaskPlatformSuno))
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeSunoAPI)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			info = &relaycommon.RelayInfo{
				TaskRelayInfo: &relaycommon.TaskRelayInfo{},
				UserId:        951, TokenId: 952, TokenKey: "task-retry",
				OriginModelName: "task-retry-model", UserGroup: "default", UsingGroup: "default",
				UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
			}
			_, taskErr := RelayTaskSubmit(c, info)
			require.NotNil(t, taskErr)
			require.Equal(t, 500, taskErr.StatusCode)
			require.Equal(t, 20000, info.Billing.GetPreConsumedQuota()) // .04 * 500000 * 1
			c.Set("auto_group", tc.retryGroup)
			result, taskErr := RelayTaskSubmit(c, info)
			mu.Lock()
			actualSent, actualHeld := append([]int{}, sent...), append([]int{}, held...)
			mu.Unlock()
			if tc.insufficient {
				require.NotNil(t, taskErr)
				assert.Equal(t, "insufficient_user_quota", taskErr.Code)
				assert.Equal(t, 403, taskErr.StatusCode)
				assert.Len(t, actualSent, 1, "unfunded retry must never reach upstream")
				assert.Equal(t, 20000, info.Billing.GetPreConsumedQuota())
			} else {
				require.Len(t, actualSent, 2)
				if tc.retryGroup == "expensive" {
					assert.Equal(t, 40000, actualHeld[1], "reserve .04 * 500000 * 2 before sending")
				}
				if tc.failRetry {
					require.NotNil(t, taskErr)
				} else {
					require.Nil(t, taskErr)
					require.NotNil(t, result)
					assert.Equal(t, tc.wantQuota, result.Quota)
					require.NoError(t, info.Billing.Settle(result.Quota))
				}
			}
			if tc.failRetry || tc.insufficient {
				info.Billing.Refund(c)
				require.Eventually(t, func() bool {
					var user model.User
					var token model.Token
					return db.First(&user, 951).Error == nil && db.First(&token, 952).Error == nil && user.Quota == tc.wallet && token.RemainQuota == 100000 && token.UsedQuota == 0
				}, 2*time.Second, 10*time.Millisecond)
			} else {
				var user model.User
				var token model.Token
				require.NoError(t, db.First(&user, 951).Error)
				require.NoError(t, db.First(&token, 952).Error)
				assert.Equal(t, tc.wallet-tc.wantQuota, user.Quota)
				assert.Equal(t, 100000-tc.wantQuota, token.RemainQuota)
				assert.Equal(t, tc.wantQuota, token.UsedQuota)
				assert.False(t, info.Billing.NeedsRefund())
			}
		})
	}
}
