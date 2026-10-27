package relay

import (
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProductMidjourneyReservesBeforeOriginalSupplierSend(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "funds-denied"}[deny], func(t *testing.T) {
			service.InitHttpClient()
			db, e := gorm.Open(sqlite.Open(t.TempDir()+"/mj.db"), &gorm.Config{})
			require.NoError(t, e)
			sqlDB, e := db.DB()
			require.NoError(t, e)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousLog := model.DB, model.LOG_DB
			previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
			previousRedis, previousBatch := common.RedisEnabled, common.BatchUpdateEnabled
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
			common.RedisEnabled, common.BatchUpdateEnabled = false, false
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLog
				common.SetDatabaseTypes(previousType, previousLogType)
				common.RedisEnabled, common.BatchUpdateEnabled = previousRedis, previousBatch
				_ = sqlDB.Close()
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.Midjourney{}))
			require.NoError(t, model.RegisterProductFundingModels(db))
			require.NoError(t, model.RegisterProductRefundModels(db))
			require.NoError(t, db.Create(&model.User{Id: 42, Username: "mj-user", Status: common.UserStatusEnabled}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 7, UserId: 42, Key: "mj-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000}).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 8, Name: "mj-original"}).Error)
			var held atomic.Int64
			var call userkit.RuntimeCall
			central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/internal/v1/product-runtime")
				switch {
				case strings.HasSuffix(path, "/balance"):
					_ = json.NewEncoder(w).Encode(userkit.RuntimeBalance{ProductID: "llmrouter", InstanceID: "default", AccountID: "payer", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, Available: "500"})
				case path == "/calls/reserve":
					if deny {
						w.WriteHeader(402)
						return
					}
					var in userkit.RuntimeReserveRequest
					_ = json.NewDecoder(r.Body).Decode(&in)
					call = userkit.RuntimeCall{RuntimeReserveRequest: in, ProductID: "llmrouter", InstanceID: "default", Held: in.Amount, Status: "RESERVED"}
					held.Store(50)
					_ = json.NewEncoder(w).Encode(call)
				case strings.HasSuffix(path, "/settle"):
					call.Status = "SETTLED"
					call.Charged = "50"
					_ = json.NewEncoder(w).Encode(call)
				case strings.HasPrefix(path, "/calls/"):
					if deny {
						w.WriteHeader(404)
						return
					}
					_ = json.NewEncoder(w).Encode(call)
				default:
					w.WriteHeader(404)
				}
			}))
			defer central.Close()
			client, e := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: central.URL, ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32)})
			require.NoError(t, e)
			require.NoError(t, db.Create(&model.ProductFundingAccount{UserID: 42, AccountID: "payer", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, State: "central", ScopeDigest: model.NewProductFundingStore(db, client).ScopeDigest()}).Error)
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
			t.Setenv("UNIHUB_ORIGIN", central.URL)
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
			t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
			t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
			prices := ratio_setting.ModelPrice2JSONString()
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(prices)) })
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString("{\"mj_imagine\":0.0001}"))
			var supplierCalls atomic.Int64
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				supplierCalls.Add(1)
				if held.Load() != 50 {
					t.Error("supplier sent before central funds were reserved")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{\"code\":1,\"description\":\"accepted\",\"result\":\"original-supplier-id\"}"))
			}))
			defer supplier.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/mj/submit/imagine", strings.NewReader("{\"prompt\":\"original experience\"}"))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("base_url", supplier.URL)
			c.Set("channel_id", 8)
			common.SetContextKey(c, constant.ContextKeyChannelId, 8)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, supplier.URL)
			info := &relaycommon.RelayInfo{RequestId: "mj-financial", UserId: 42, TokenId: 7, TokenKey: "mj-key", OriginModelName: "mj_imagine", UsingGroup: "default", UserGroup: "default", RelayMode: relayconstant.RelayModeMidjourneyImagine}
			result := RelayMidjourneySubmit(c, info)
			if deny {
				require.NotNil(t, result)
				require.Zero(t, supplierCalls.Load())
			} else {
				require.Nil(t, result)
				require.EqualValues(t, 1, supplierCalls.Load())
				var task model.Midjourney
				require.NoError(t, db.First(&task).Error)
				require.Equal(t, "mj-financial", task.ProductCallID)
				require.Equal(t, "original-supplier-id", task.MjId)
			}
		})
	}
}
