package router

import (
	"bytes"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPlatformMeteredSyncActualUsageAndReplay(t *testing.T) {
	for _, tc := range []struct{ op, path, body, response, unit, key, policy string }{
		{"embedding", "/embeddings", `{"model":"platform-test","input":"hello"}`, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"platform-test","usage":{"prompt_tokens":1,"total_tokens":1}}`, "token", "input", "sum"},
		{"speech", "/audio/speech", `{"model":"platform-test","input":"hello","voice":"alloy"}`, "audio-bytes", "character", "characters", "unicode_scalar_v1"},
		{"image_generation", "/images/generations", `{"model":"platform-test","prompt":"hello","n":1}`, `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`, "image", "images", "successful_outputs_v1"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			require.NoError(t, i18n.Init())
			gin.SetMode(gin.TestMode)
			sends, settles := 0, 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer up.Close()
			price := metering.Price{Version: "metered_v2", Operation: tc.op, Policy: tc.policy, Terms: []metering.Term{{Key: tc.key, Unit: tc.unit, Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
			rate := platformbilling.Rate{ID: "rate", Model: "platform-test", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
			var remote platformbilling.RemoteCall
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var out any
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					out = platformbilling.Claims{ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "origin", Models: []string{"platform-test"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
				case "/internal/v1/rates":
					out = []platformbilling.Rate{rate}
				case "/internal/v1/calls/reserve":
					var in map[string]string
					require.NoError(t, common.DecodeJson(r.Body, &in))
					remote = platformbilling.RemoteCall{CallID: "call", ActorID: "actor", AccountID: "account", OriginProductID: "origin", CredentialID: "credential", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
					out = remote
				case "/internal/v1/calls/call/settle":
					settles++
					remote.Status = "SETTLED"
					out = remote
				case "/internal/v1/calls/call/release":
					remote.Status = "RELEASED"
					out = remote
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				b, e := common.Marshal(out)
				require.NoError(t, e)
				_, _ = w.Write(b)
			}))
			defer hub.Close()
			db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
			require.NoError(t, e)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
			oldDB, oldLog, oldCache, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
			model.DB = db
			model.LOG_DB = db
			common.MemoryCacheEnabled = true
			common.RedisEnabled = false
			defer func() {
				model.DB = oldDB
				model.LOG_DB = oldLog
				common.MemoryCacheEnabled = oldCache
				common.RedisEnabled = oldRedis
			}()
			user := model.User{Id: 1, Username: "platform", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, RequestCount: 3, Group: "platform"}
			require.NoError(t, db.Create(&user).Error)
			base := up.URL
			require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "key", Status: common.ChannelStatusEnabled, Name: "platform", BaseURL: &base, Models: "platform-test", Group: "platform"}).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "platform-test", ChannelId: 1, Enabled: true}).Error)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"platform-test":0}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
			model.InitChannelCache()
			service.InitHttpClient()
			service.InitTokenEncoders()
			a, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CREDIT/6": {"platform-test": "rate"}}}})
			require.NoError(t, e)
			engine := gin.New()
			engine.Use(middlewareBodyCleanupForTest())
			SetPlatformRouter(engine, a)
			send := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/platform/v1"+tc.path, bytes.NewBufferString(tc.body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer uh_model_token")
				r.Header.Set("Idempotency-Key", "call")
				r.Header.Set("X-UniHub-Task-ID", "task")
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, r)
				return w
			}
			w := send()
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, 1, sends)
			var call platformbilling.Call
			require.NoError(t, db.First(&call).Error)
			assert.Equal(t, "SETTLED", call.Status, call.Evidence)
			assert.Equal(t, 1, settles)
			w = send()
			assert.Equal(t, 409, w.Code)
			assert.Equal(t, 1, sends)
			var actual model.User
			require.NoError(t, db.First(&actual, 1).Error)
			assert.Equal(t, user.Quota, actual.Quota)
			assert.Equal(t, user.UsedQuota, actual.UsedQuota)
			assert.Equal(t, user.RequestCount, actual.RequestCount)
		})
	}
}
