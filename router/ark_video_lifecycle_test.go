package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestArkVideoLifecycleThroughAuthenticatedRoutes(t *testing.T) {
	enableNativeCompatibility(t)
	require.NoError(t, i18n.Init())
	service.InitHttpClient()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{},
		&model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Task{}, &model.Log{}, &model.UniHubIdentityMapping{}))

	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldDBType, oldLogDBType := common.MainDatabaseType(), common.LogDatabaseType()
	oldMemory, oldRedis, oldBatch, oldLog := common.MemoryCacheEnabled, common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
	oldAdapter := service.GetTaskAdaptorFunc
	oldTaskQueryLimit := constant.TaskQueryLimit
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	model.InitDatabaseColumns()
	common.MemoryCacheEnabled, common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, false, true
	constant.TaskQueryLimit = 10
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
		return relay.GetTaskAdaptor(platform)
	}
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldDBType, oldLogDBType)
		model.InitDatabaseColumns()
		common.MemoryCacheEnabled, common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = oldMemory, oldRedis, oldBatch, oldLog
		service.GetTaskAdaptorFunc = oldAdapter
		constant.TaskQueryLimit = oldTaskQueryLimit
	})

	const initial = 5000000
	const hold = 600000
	const final = 240
	const publicModel = "ark-seedance-public"
	const alias = "ark-seedance-alias"
	const upstreamModel = "doubao-seedance-2-5-260628"
	const billingModel = "ark-seedance-billing"
	const taskEndpoint = "/api/v3/contents/generations/tasks"
	oldRatios, oldGroups := ratio_setting.ModelRatio2JSONString(), ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroups))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"ark-seedance-billing":2}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":2}`))

	user := model.User{Id: 901, Username: "ark-video", Group: "default", Status: common.UserStatusEnabled, Quota: initial, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{Id: 902, UserId: user.Id, Key: "arkvideolocalfixture", Name: "ark-video", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: initial, ModelLimitsEnabled: true, ModelLimits: alias + "," + publicModel}
	require.NoError(t, db.Create(&token).Error)

	checkBalance := func(wallet, key int) {
		var account model.User
		var apiKey model.Token
		assert.NoError(t, db.First(&account, user.Id).Error)
		assert.NoError(t, db.First(&apiKey, token.Id).Error)
		assert.Equal(t, wallet, account.Quota)
		assert.Equal(t, key, apiKey.RemainQuota)
	}
	var submits, polls, downloads, wrongChannel atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture.mp4" {
			downloads.Add(1)
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mock Ark video"))
			return
		}
		assert.Equal(t, "Bearer ark-video-fixture", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST " + taskEndpoint:
			n := submits.Add(1)
			var payload struct {
				Model      string `json:"model"`
				Resolution string `json:"resolution"`
				Content    []struct {
					Type string `json:"type"`
				} `json:"content"`
			}
			assert.NoError(t, common.DecodeJson(r.Body, &payload))
			assert.Equal(t, upstreamModel, payload.Model)
			assert.Equal(t, "720p", payload.Resolution)
			if assert.Len(t, payload.Content, 2) {
				assert.Equal(t, "video_url", payload.Content[0].Type)
			}
			available := initial - hold
			if n > 1 {
				available -= final
			}
			checkBalance(available, available)
			_, _ = fmt.Fprintf(w, `{"id":"provider-%d"}`, n)
		case "GET " + taskEndpoint + "/provider-1":
			polls.Add(1)
			_, _ = fmt.Fprintf(w, `{"id":"provider-1","model":%q,"status":"succeeded","content":{"video_url":%q},"usage":{"completion_tokens":100,"total_tokens":100}}`, upstreamModel, "http://"+r.Host+"/fixture.mp4")
		case "GET " + taskEndpoint + "/provider-2":
			polls.Add(1)
			_, _ = fmt.Fprintf(w, `{"id":"provider-2","model":%q,"status":"failed","error":{"code":"GenerationFailed","message":"fixture failure"}}`, upstreamModel)
		case "GET " + taskEndpoint + "/provider-3":
			polls.Add(1)
			_, _ = fmt.Fprintf(w, `{"id":"provider-3","model":%q,"status":"succeeded","content":{"video_url":%q}}`, upstreamModel, "http://"+r.Host+"/fixture.mp4")
		default:
			t.Errorf("unexpected Ark request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	// Exercise artifact download through the real protection and allow only the
	// local fixture's address and port.
	providerHost, providerPort, err := net.SplitHostPort(provider.Listener.Addr().String())
	require.NoError(t, err)
	fetch := system_setting.GetFetchSetting()
	oldFetch := *fetch
	*fetch = system_setting.FetchSetting{
		EnableSSRFProtection: true, AllowPrivateIp: true, IpFilterMode: true,
		IpList: []string{providerHost}, AllowedPorts: []string{providerPort},
	}
	t.Cleanup(func() { *fetch = oldFetch })
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrongChannel.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(decoy.Close)

	ch := model.Channel{Id: 27, Type: constant.ChannelTypeDoubaoVideo, Status: common.ChannelStatusEnabled,
		Group: "default", Models: upstreamModel, Key: "ark-video-fixture", BaseURL: common.GetPointer(provider.URL),
		ModelMapping: common.GetPointer(`{"ark-configured-model":"doubao-seedance-2-5-260628"}`)}
	other := model.Channel{Id: 25, Type: constant.ChannelTypeVolcEngine, Status: common.ChannelStatusEnabled,
		Group: "default", Models: alias, Key: "other-fixture-key", BaseURL: common.GetPointer(decoy.URL)}
	require.NoError(t, db.Create(&ch).Error)
	require.NoError(t, db.Create(&other).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: ch.Id, Group: "default", Model: upstreamModel, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: other.Id, Group: "default", Model: alias, Enabled: true}).Error)
	route := model.ClientModelRoute{PublicId: publicModel, Aliases: `["ark-seedance-alias"]`, Client: "llmrouter", Enabled: true, Metadata: `{"operations":["videos.create"]}`}
	require.NoError(t, db.Create(&route).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{
		RouteId: route.Id, ChannelId: ch.Id, UpstreamModel: "ark-configured-model", BillingModel: billingModel,
		PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	SetVideoRouter(engine)
	request := func(method, path, body string, authenticate bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticate {
			r.Header.Set("Authorization", "Bearer sk-"+token.Key)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	directory := "/v1/models?client=llmrouter"
	unauthorized := request(http.MethodGet, directory, "", false)
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code, unauthorized.Body.String())
	listed := request(http.MethodGet, directory, "", true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var models struct {
		Data []struct {
			ID         string   `json:"id"`
			Operations []string `json:"operations"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(listed.Body.Bytes(), &models))
	var operations []string
	for _, m := range models.Data {
		if m.ID == publicModel {
			operations = m.Operations
		}
	}
	require.Equal(t, []string{"videos.create"}, operations)

	// Use a real client alias and a supplier reference clip to exercise all
	// three model identities and the provider's 42/70 token-rate adjustment.
	body := `{"model":"ark-seedance-alias","prompt":"a fixture prompt","metadata":{"resolution":"720p","content":[{"type":"video_url","role":"reference_video","video_url":{"url":"https://example.test/reference.mp4"}}]}}`
	denied := request(http.MethodPost, "/v1/videos?client=llmrouter", body, false)
	require.Equal(t, http.StatusUnauthorized, denied.Code, denied.Body.String())
	assert.Zero(t, submits.Load())
	for i, scenario := range []struct {
		status        string
		quota         int
		total         int
		pendingReason string
	}{
		{"completed", final, final, ""},
		{"failed", 0, final, ""},
		{"completed", hold, final + hold, "provider_usage_missing"},
	} {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"ark-seedance-billing":2}`))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":2}`))
		created := request(http.MethodPost, "/v1/videos?client=llmrouter", body, true)
		require.Equal(t, http.StatusOK, created.Code, created.Body.String())
		var video dto.OpenAIVideo
		require.NoError(t, common.Unmarshal(created.Body.Bytes(), &video))
		require.NotEmpty(t, video.ID)
		assert.Equal(t, alias, video.Model)
		assert.NotContains(t, created.Body.String(), "provider-")

		task, exists, err := model.GetByTaskId(user.Id, video.ID)
		require.NoError(t, err)
		require.True(t, exists)
		assert.Equal(t, ch.Id, task.ChannelId)
		assert.Equal(t, constant.TaskPlatform(strconv.Itoa(ch.Type)), task.Platform)
		assert.Equal(t, upstreamModel, task.Properties.UpstreamModelName)
		assert.Equal(t, hold, task.Quota)
		require.NotNil(t, task.PrivateData.BillingContext)
		assert.Equal(t, billingModel, task.PrivateData.BillingContext.BillingModelName)
		assert.Equal(t, 0.6, task.PrivateData.BillingContext.OtherRatios["video_input"])

		statusPath := "/v1/videos/" + video.ID
		pending := request(http.MethodGet, statusPath, "", true)
		require.Equal(t, http.StatusOK, pending.Code, pending.Body.String())
		assert.Contains(t, pending.Body.String(), `"status":"queued"`)

		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"ark-seedance-billing":5,"ark-seedance-alias":11}`))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":3}`))
		require.Equal(t, 1, service.RunTaskPollingOnce(context.Background(), nil).UnfinishedTasks)
		stored, exists, err := model.GetByTaskId(user.Id, video.ID)
		require.NoError(t, err)
		require.True(t, exists)
		require.Equal(t, scenario.quota, stored.Quota)
		require.Equal(t, scenario.status, stored.Status.ToVideoStatus())
		require.NotNil(t, stored.PrivateData.BillingContext)
		require.Equal(t, scenario.pendingReason, stored.PrivateData.BillingContext.SettlementPendingReason)

		// Replay a poller snapshot taken before the terminal update. The
		// compare-and-swap must prevent a second settlement or refund.
		providerID := task.GetUpstreamTaskID()
		for range 2 {
			require.NoError(t, service.UpdateVideoTasks(context.Background(), task.Platform, map[int][]string{ch.Id: {providerID}},
				map[string]*model.Task{providerID: task}))
		}
		checkBalance(initial-scenario.total, initial-scenario.total)

		// Another worker pass and repeated public reads must leave billing and
		// the upstream poll count unchanged.
		assert.Zero(t, service.RunTaskPollingOnce(context.Background(), nil).UnfinishedTasks)
		for range 2 {
			read := request(http.MethodGet, statusPath, "", true)
			require.Equal(t, http.StatusOK, read.Code, read.Body.String())
			assert.Contains(t, read.Body.String(), `"status":"`+scenario.status+`"`)
			assert.Contains(t, read.Body.String(), `"model":"`+alias+`"`)
		}
		assert.EqualValues(t, (i+1)*2, polls.Load())
		if scenario.quota == final {
			videoContent := request(http.MethodGet, statusPath+"/content", "", true)
			require.Equal(t, http.StatusOK, videoContent.Code, videoContent.Body.String())
			assert.Equal(t, "video/mp4", videoContent.Header().Get("Content-Type"))
			assert.Equal(t, "mock Ark video", videoContent.Body.String())
		}
	}
	checkBalance(initial-final-hold, initial-final-hold)
	var billedUser model.User
	var billedToken model.Token
	var billedChannel model.Channel
	require.NoError(t, db.First(&billedUser, user.Id).Error)
	require.NoError(t, db.First(&billedToken, token.Id).Error)
	require.NoError(t, db.First(&billedChannel, ch.Id).Error)
	assert.Equal(t, final+hold, billedUser.UsedQuota)
	assert.Equal(t, final+hold, billedToken.UsedQuota)
	assert.EqualValues(t, final+hold, billedChannel.UsedQuota)
	assert.Equal(t, 3, billedUser.RequestCount)
	assert.EqualValues(t, 3, submits.Load())
	assert.EqualValues(t, 6, polls.Load())
	assert.EqualValues(t, 1, downloads.Load())
	assert.Zero(t, wrongChannel.Load())
}
