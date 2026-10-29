package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	taskali "github.com/QuantumNous/new-api/relay/channel/task/ali"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type wanVideoGatewayFixture struct {
	engine                                      *gin.Engine
	adapter                                     *platformbilling.Adapter
	db                                          *gorm.DB
	cfg                                         platformbilling.Config
	sends, fetches, downloads, cancels, settles int
	variant                                     string
	denySettlementRead                          bool
	mp4                                         []byte
}

func wanTierPrice() metering.Price {
	return metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_cny6_v2", Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "600000", Denominator: "1"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1000000", Denominator: "1"}},
	}}
}

func newWanVideoGatewayFixture(t *testing.T, variant string) *wanVideoGatewayFixture {
	t.Helper()
	return newWanVideoGatewayFixtureWithHub(t, variant, "", "", "")
}

func newWanVideoGatewayFixtureWithHub(t *testing.T, variant, hubOrigin, serviceToken, dbPath string) *wanVideoGatewayFixture {
	t.Helper()
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	f := &wanVideoGatewayFixture{variant: variant}
	if variant == "1080" {
		f.mp4 = wanVideoTestFile(t, "wan1080.mp4")
	} else {
		f.mp4 = wanVideoTestFile(t, "wan720.mp4")
	}
	if variant == "wrong-resolution" {
		f.mp4 = wanVideoTestFile(t, "wan1080.mp4")
	}
	if dbPath == "" {
		dbPath = t.TempDir() + "/wan-video.db"
	}
	db, err := gorm.Open(sqlite.Open("file:"+dbPath), &gorm.Config{})
	require.NoError(t, err)
	f.db = db
	for _, entry := range []any{&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Task{}, &model.RelayMediaResource{}} {
		require.NoError(t, db.AutoMigrate(entry))
	}
	oldDB, oldLog, oldMemory, oldRedis, oldRuntime := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, controller.RelayMediaRuntime
	model.DB, model.LOG_DB = db, db
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemory, oldRedis
		controller.RelayMediaRuntime = oldRuntime
	})
	require.NoError(t, model.InitLogDB())
	require.NoError(t, db.Save(&model.User{Id: 1, Username: "platform", AffCode: "platform", Status: common.UserStatusEnabled, Quota: 12345, Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.User{Id: 2, Username: "native", AffCode: "native", Status: common.UserStatusEnabled, Quota: 22222, Group: "default"}).Error)
	require.NoError(t, db.Save(&model.Token{Id: 1, UserId: 2, Key: "synthetic-native", Status: common.TokenStatusEnabled, RemainQuota: 54321, ExpiredTime: -1}).Error)

	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services/aigc/video-generation/video-synthesis":
			f.sends++
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "enable", r.Header.Get("X-DashScope-Async"))
			assert.Equal(t, "Bearer synthetic-wan-key", r.Header.Get("Authorization"))
			var call platformbilling.Call
			require.NoError(t, db.First(&call, "call_id = ?", "wan-call").Error)
			assert.Equal(t, "UNKNOWN", call.Status)
			var attempt platformbilling.Attempt
			require.NoError(t, db.First(&attempt, "call_id = ?", call.CallID).Error)
			assert.Equal(t, 16, attempt.ChannelID)
			raw, e := io.ReadAll(r.Body)
			require.NoError(t, e)
			var request map[string]any
			require.NoError(t, common.UnmarshalUnique(raw, &request))
			assert.Equal(t, "wan2.6-t2v", request["model"])
			assert.Equal(t, "hello async world", request["input"].(map[string]any)["prompt"])
			parameters := request["parameters"].(map[string]any)
			assert.Equal(t, "5", parameters["duration"].(json.Number).String())
			expectedSize := "1280*720"
			if variant == "1080" {
				expectedSize = "1920*1080"
			}
			assert.Equal(t, expectedSize, parameters["size"])
			_, _ = w.Write([]byte(`{"output":{"task_id":"provider-wan","task_status":"PENDING"},"request_id":"submit-1"}`))
		case "/api/v1/tasks/provider-wan":
			f.fetches++
			assert.Equal(t, "Bearer synthetic-wan-key", r.Header.Get("Authorization"))
			videoURL := provider.URL + "/video.mp4?Expires=1717171717&OSSAccessKeyId=test&Signature=valid"
			switch variant {
			case "query-forgery":
				videoURL = provider.URL + "/video.mp4?Expires=1717171717&OSSAccessKeyId=test&Signature=forged"
			case "uncertified-origin":
				videoURL = "https://evil.example/video.mp4?Expires=1717171717&Signature=valid"
			case "redirect":
				videoURL = provider.URL + "/redirect?Expires=1717171717&Signature=valid"
			case "userinfo":
				videoURL = "http://user:secret@" + provider.Listener.Addr().String() + "/video.mp4?Signature=valid"
			case "fragment":
				videoURL = provider.URL + "/video.mp4?Signature=valid#fragment"
			case "non-https":
				videoURL = "ftp://" + provider.Listener.Addr().String() + "/video.mp4?Signature=valid"
			}
			if variant == "missing-usage" {
				fmt.Fprintf(w, `{"output":{"task_id":"provider-wan","task_status":"SUCCEEDED","video_url":%q},"usage":{"duration":5},"request_id":"status-1"}`, videoURL)
				return
			}
			if variant == "conflicting-output" {
				fmt.Fprintf(w, `{"output":{"task_id":"provider-wan","task_status":"SUCCEEDED","video_url":%q,"results":[{},{"url":"other"}]},"usage":{"duration":5,"video_count":1}}`, videoURL)
				return
			}
			sr := 720
			if variant == "1080" {
				sr = 1080
			}
			size := "1280*720"
			if sr == 1080 {
				size = "1920*1080"
			}
			fmt.Fprintf(w, `{"output":{"task_id":"provider-wan","task_status":"SUCCEEDED","video_url":%q},"usage":{"duration":5,"input_video_duration":0,"output_video_duration":5,"video_count":1,"SR":%d,"size":%q},"request_id":"status-1"}`, videoURL, sr, size)
		case "/api/v1/tasks/provider-wan/cancel":
			f.cancels++
			if variant == "running-cancel" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"UnsupportedOperation","message":"task is running","request_id":"cancel-running"}`))
				return
			}
			_, _ = w.Write([]byte(`{"output":{"task_id":"provider-wan","task_status":"CANCELED","results":[]},"usage":{"video_count":0},"request_id":"cancel-pending"}`))
		case "/video.mp4":
			f.downloads++
			assert.Empty(t, r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "video/mp4")
			if variant == "query-forgery" && r.URL.Query().Get("Signature") != "valid" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if variant == "wrong-codec" {
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = w.Write([]byte("not an mp4"))
				return
			}
			if variant == "oversize" {
				w.Header().Set("Content-Length", "50000001")
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write(f.mp4)
		case "/redirect":
			f.downloads++
			http.Redirect(w, r, "https://evil.example/video.mp4", http.StatusFound)
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(provider.Close)

	require.NoError(t, db.Save(&model.Channel{Id: 16, Name: "wan", Type: constant.ChannelTypeAli, Key: "synthetic-wan-key", Status: common.ChannelStatusEnabled, BaseURL: &provider.URL, Models: "wan2.6-t2v", Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.Ability{Group: "platform", Model: "wan2.6-t2v", ChannelId: 16, Enabled: true}).Error)
	oldRatio := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"platform":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatio)) })
	price := wanTierPrice()
	rate := platformbilling.Rate{ID: "wan-rate", Model: "wan2.6-t2v", Asset: "CNY/6", Status: "published", Semantics: "metered_v2", Metered: &price}
	var remote platformbilling.RemoteCall
	var final *taskSettlementInput
	if hubOrigin == "" {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var out any
			switch r.URL.Path {
			case "/internal/v1/model-credentials/verify":
				out = platformbilling.Claims{CredentialKind: "task", ID: "original-credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", Models: []string{"wan2.6-t2v"}, Operations: map[string][]string{"wan2.6-t2v": {"videos.create"}}, TaskID: "wan-task", ExpiresAt: time.Now().Add(time.Hour), Asset: "CNY/6", FundingMode: "prepaid_cash"}
			case "/internal/v1/rates":
				out = []platformbilling.Rate{rate}
			case "/internal/v1/calls/reserve":
				var in map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &in))
				remote = platformbilling.RemoteCall{WireOperation: in["wire_operation"], CallID: in["call_id"], ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", CredentialID: "original-credential", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
				out = remote
			case "/internal/v1/calls/wan-call/settle":
				f.settles++
				var settlement taskSettlementInput
				require.NoError(t, common.DecodeJson(r.Body, &settlement))
				amount, e := metering.Calculate(*rate.Metered, *settlement.Usage.Metered)
				require.NoError(t, e)
				remote.Status, remote.Charged = "SETTLED", amount.RatString()
				final = &settlement
				out = remote
			case "/internal/v1/calls/wan-call/release":
				remote.Status = "RELEASED"
				out = remote
			case "/internal/v1/calls/wan-call":
				receipt := remote
				if f.denySettlementRead {
					receipt.Status = "SETTLE_PENDING"
				}
				out = authorityReceipt(t, receipt, "wan-task", final)
			case "/internal/v1/call-reads/verify":
				var row platformbilling.Call
				require.NoError(t, db.First(&row, "call_id = ?", "wan-call").Error)
				out = platformbilling.OwnedReadClaims{CredentialKind: "owned_call_read_v1", Audience: "llmrouter-read", ClientID: "sw", CallID: row.CallID, RequestDigest: row.Digest, Model: row.Model, WireOperation: row.WireOperation, TaskID: row.TaskID, CredentialID: row.CredentialID, OriginalCredentialKind: "task", ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, InstanceID: row.InstanceID, ExpiresAt: time.Now().Add(4 * time.Minute)}
			default:
				t.Errorf("unexpected hub path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			raw, e := common.Marshal(out)
			require.NoError(t, e)
			_, _ = w.Write(raw)
		}))
		t.Cleanup(hub.Close)
		hubOrigin = hub.URL
		serviceToken = strings.Repeat("s", 32)
	}

	objects, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)
	media, err := relaymedia.NewService(db, relaymedia.Config{PublicOrigin: "https://owned.example", GrantSecret: bytes.Repeat([]byte{7}, 32), Objects: objects})
	require.NoError(t, err)
	controller.RelayMediaRuntime = &relaymedia.Runtime{Service: media}
	fetch := system_setting.GetFetchSetting()
	oldFetch := *fetch
	*fetch = system_setting.FetchSetting{EnableSSRFProtection: true, AllowPrivateIp: true, AllowedPorts: []string{"1-65535"}, ApplyIPFilterForDomain: true}
	t.Cleanup(func() { *fetch = oldFetch })
	service.InitHttpClient()
	model.InitChannelCache()
	oldAdaptor := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(constant.TaskPlatform) service.TaskPollingAdaptor { return &taskali.TaskAdaptor{} }
	t.Cleanup(func() { service.GetTaskAdaptorFunc = oldAdaptor })

	keyHash := sha256.Sum256([]byte("synthetic-wan-key"))
	policy := platformbilling.VideoPolicy{Version: "video_v2", PublicModel: "wan2.6-t2v", WireOperation: "videos.create", ChannelID: 16, ChannelType: constant.ChannelTypeAli, UpstreamModel: "wan2.6-t2v", ChannelKeySHA256: hex.EncodeToString(keyHash[:]), Origin: "https://dashscope.aliyuncs.com", APIVersion: "dashscope-video-v1", UsagePolicy: "ali_wan26_t2v_cny6_v2", MaxSeconds: 10, MaxVideoBytes: 64 << 20, Resolutions: []string{"720P", "1080P"}, ResourceOrigins: []string{provider.URL}, AllowLoopbackHTTP: true, RateIDs: map[string]string{"CNY/6": "wan-rate"}}
	f.cfg = platformbilling.Config{Enabled: true, Origin: hubOrigin, ServiceToken: serviceToken, PrincipalID: 1, Group: "platform", MaxBodyBytes: 1 << 20, MaxInputTokens: 1000, MaxOutputTokens: 100, VideoPolicies: map[string]map[string]platformbilling.VideoPolicy{"videos.create": {"wan2.6-t2v": policy}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"video": {"wan2.6-t2v": {MaxQuantity: 10, Policy: "ali_wan26_t2v_cny6_v2"}}}, OperationRateIDs: map[string]map[string]map[string]string{"video": {"CNY/6": {"wan2.6-t2v": "wan-rate"}}}}
	f.adapter, err = platformbilling.New(db, f.cfg)
	require.NoError(t, err)
	f.engine = gin.New()
	SetPlatformRouter(f.engine, f.adapter)
	return f
}

func TestWanAsyncSignedQueryArtifactBoundaries(t *testing.T) {
	for _, variant := range []string{
		"query-forgery", "uncertified-origin", "redirect", "userinfo", "fragment", "non-https",
		"wrong-codec", "oversize", "wrong-resolution",
	} {
		t.Run(variant, func(t *testing.T) {
			f := newWanVideoGatewayFixture(t, variant)
			require.Equal(t, http.StatusOK, f.submit(t, "720P").Code)
			oldLimit := constant.TaskQueryLimit
			constant.TaskQueryLimit = 10
			t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
			_ = service.RunTaskPollingOnce(context.Background(), nil)

			var task model.Task
			require.NoError(t, f.db.First(&task, "platform_call_id = ?", "wan-call").Error)
			assert.Equal(t, model.TaskStatus(model.TaskStatusNotStart), task.Status)
			assert.Empty(t, task.PrivateData.ResultResourceID)
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call, "call_id = ?", "wan-call").Error)
			assert.Equal(t, "UNKNOWN", call.Status)
			assert.Equal(t, "3000000", call.Amount)
			switch variant {
			case "uncertified-origin", "userinfo", "fragment", "non-https":
				assert.Zero(t, f.downloads)
			default:
				assert.Equal(t, 1, f.downloads)
			}
		})
	}
}

func wanVideoTestFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../service/relaymedia/testdata/" + name)
	require.NoError(t, err)
	return data
}

func (f *wanVideoGatewayFixture) submit(t *testing.T, resolution string) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`{"model":"wan2.6-t2v","prompt":"hello async world","seconds":"5","size":"1280*720"}`)
	if resolution == "1080P" {
		body = []byte(`{"model":"wan2.6-t2v","prompt":"hello async world","seconds":"5","size":"1920*1080"}`)
	}
	r := httptest.NewRequest(http.MethodPost, "/platform/v1/videos", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	r.Header.Set("X-UniHub-Task-ID", "wan-task")
	r.Header.Set("Idempotency-Key", "wan-call")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}

func (f *wanVideoGatewayFixture) read(t *testing.T, suffix, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/platform/v1/videos/wan-call"+suffix, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if !strings.HasPrefix(token, "uh_read_") {
		r.Header.Set("X-UniHub-Task-ID", "wan-task")
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}

func TestWanAsyncActualGatewayLifecycle(t *testing.T) {
	for _, tc := range []struct{ variant, resolution, charged string }{{"720", "720P", "3000000"}, {"1080", "1080P", "5000000"}} {
		t.Run(tc.resolution, func(t *testing.T) {
			f := newWanVideoGatewayFixture(t, tc.variant)
			w := f.submit(t, tc.resolution)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, 1, f.sends)
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call, "call_id = ?", "wan-call").Error)
			require.Equal(t, "UNKNOWN", call.Status)
			require.Equal(t, "provider-wan", call.ProviderTaskID)
			oldLimit := constant.TaskQueryLimit
			constant.TaskQueryLimit = 10
			t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
			_ = service.RunTaskPollingOnce(context.Background(), nil)
			require.NoError(t, f.db.First(&call, "call_id = ?", "wan-call").Error)
			assert.Equal(t, "SETTLED", call.Status)
			assert.Equal(t, tc.charged, call.Charged)
			assert.Equal(t, 1, f.fetches)
			assert.Equal(t, 1, f.downloads)
			var task model.Task
			require.NoError(t, f.db.First(&task, "platform_call_id = ?", "wan-call").Error)
			require.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
			sum := sha256.Sum256(f.mp4)
			assert.Equal(t, hex.EncodeToString(sum[:]), task.PrivateData.ResultSHA256)
			assert.EqualValues(t, len(f.mp4), task.PrivateData.ResultSize)
			result := f.read(t, "/result", "uh_model_"+strings.Repeat("a", 64))
			require.Equal(t, http.StatusOK, result.Code, result.Body.String())
			assert.Equal(t, f.mp4, result.Body.Bytes())
			assert.Equal(t, hex.EncodeToString(sum[:]), result.Header().Get("X-Content-Sha256"))
			owned := f.read(t, "/result", "uh_read_"+strings.Repeat("b", 64))
			require.Equal(t, http.StatusOK, owned.Code, owned.Body.String())
			assert.Equal(t, f.mp4, owned.Body.Bytes())
			replay := f.submit(t, tc.resolution)
			assert.Equal(t, http.StatusConflict, replay.Code)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
		})
	}
}

func TestWanAsyncHoldsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		variant string
		pending bool
	}{{"missing-usage", false}, {"conflicting-output", false}, {"running-cancel", false}, {"pending-cancel", true}} {
		t.Run(tc.variant, func(t *testing.T) {
			variant := tc.variant
			f := newWanVideoGatewayFixture(t, variant)
			require.Equal(t, http.StatusOK, f.submit(t, "720P").Code)
			if variant == "missing-usage" {
				oldLimit := constant.TaskQueryLimit
				constant.TaskQueryLimit = 10
				t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
				_ = service.RunTaskPollingOnce(context.Background(), nil)
				var call platformbilling.Call
				require.NoError(t, f.db.First(&call, "call_id = ?", "wan-call").Error)
				assert.Equal(t, "UNKNOWN", call.Status)
				assert.Equal(t, "3000000", call.Amount)
				assert.Zero(t, f.settles)
				return
			}
			if variant == "conflicting-output" {
				oldLimit := constant.TaskQueryLimit
				constant.TaskQueryLimit = 10
				t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
				_ = service.RunTaskPollingOnce(context.Background(), nil)
				var task model.Task
				require.NoError(t, f.db.First(&task, "platform_call_id = ?", "wan-call").Error)
				assert.Equal(t, model.TaskStatus(model.TaskStatusNotStart), task.Status)
				return
			}
			r := httptest.NewRequest(http.MethodPost, "/platform/v1/videos/wan-call/cancel", nil)
			r.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
			r.Header.Set("X-UniHub-Task-ID", "wan-task")
			w := httptest.NewRecorder()
			f.engine.ServeHTTP(w, r)
			expected := http.StatusConflict
			if tc.pending {
				expected = http.StatusOK
			} else {
				require.Contains(t, w.Body.String(), "provider_cancellation_unsupported")
			}
			require.Equal(t, expected, w.Code, w.Body.String())
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call, "call_id = ?", "wan-call").Error)
			if tc.pending {
				assert.Equal(t, "RELEASED", call.Status)
				var canceled model.Task
				require.NoError(t, f.db.First(&canceled, "platform_call_id = ?", "wan-call").Error)
				assert.Equal(t, model.TaskStatus(model.TaskStatusFailure), canceled.Status)
			} else {
				assert.Equal(t, "UNKNOWN", call.Status)
				assert.Equal(t, "3000000", call.Amount)
			}
		})
	}
}

func TestWanVideoLocalLiveFixture(t *testing.T) {
	if os.Getenv("UNIHUB_WAN_VIDEO_E2E") != "1" {
		t.Skip("explicit isolated Wan video fixture")
	}
	dir := os.Getenv("UNIHUB_WAN_VIDEO_E2E_DIR")
	require.True(t, filepath.IsAbs(dir))
	token, err := os.ReadFile(filepath.Join(dir, "service-token"))
	require.NoError(t, err)
	f := newWanVideoGatewayFixtureWithHub(t, os.Getenv("UNIHUB_WAN_VIDEO_CASE"), os.Getenv("UNIHUB_WAN_VIDEO_ORIGIN"), string(token), filepath.Join(dir, "gateway.db"))
	status := func() gin.H {
		var call platformbilling.Call
		var task model.Task
		var user, native model.User
		var token model.Token
		require.NoError(t, f.db.First(&call, "call_id = ?", "wan-call").Error)
		require.NoError(t, f.db.First(&task, "platform_call_id = ?", "wan-call").Error)
		require.NoError(t, f.db.First(&user, 1).Error)
		require.NoError(t, f.db.First(&native, 2).Error)
		require.NoError(t, f.db.First(&token, 1).Error)
		return gin.H{"provider_calls": f.sends, "fetches": f.fetches, "downloads": f.downloads, "cancellations": f.cancels, "settles": f.settles, "quota": user.Quota, "used_quota": user.UsedQuota, "native_quota": native.Quota, "native_token_quota": token.RemainQuota, "status": call.Status, "charged": call.Charged, "amount": call.Amount, "wire": call.WireOperation, "task_status": task.Status, "resource_sha256": task.PrivateData.ResultSHA256, "resource_size": task.PrivateData.ResultSize}
	}
	f.engine.GET("/__fixture/status", func(c *gin.Context) { c.JSON(http.StatusOK, status()) })
	f.engine.POST("/__fixture/poll", func(c *gin.Context) {
		oldLimit := constant.TaskQueryLimit
		constant.TaskQueryLimit = 100
		defer func() { constant.TaskQueryLimit = oldLimit }()
		_ = service.RunTaskPollingOnce(c.Request.Context(), nil)
		c.JSON(http.StatusOK, status())
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: f.engine}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready"), []byte("http://"+listener.Addr().String()), 0600))
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	<-signals
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))
	require.ErrorIs(t, <-done, http.ErrServerClosed)
}

func TestWanCashAdmissionRequiresFrozenProducerCertificate(t *testing.T) {
	f := newWanVideoGatewayFixture(t, "720")
	f.cfg.VideoPolicies = nil
	adapter, err := platformbilling.New(f.db, f.cfg)
	require.NoError(t, err)
	f.engine = gin.New()
	SetPlatformRouter(f.engine, adapter)
	response := f.submit(t, "720P")
	require.NotEqual(t, http.StatusOK, response.Code, response.Body.String())
	assert.Zero(t, f.sends, "supplier dispatch must not occur without a bound video certificate")
}
