package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDistributorSkipsCooledPoolForAuthorizedBackup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	oldDB, oldLog, oldCache := model.DB, model.LOG_DB, common.MemoryCacheEnabled
	oldType := common.MainDatabaseType()
	model.DB, common.MemoryCacheEnabled = db, false
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.MemoryCacheEnabled = oldDB, oldLog, oldCache
		common.SetMainDatabaseType(oldType)
		require.NoError(t, raw.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	primary := model.Channel{Id: 111, Type: constant.ChannelTypeOpenAI, Key: "A\nB", Models: "gpt-test", Group: "default", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	backup := model.Channel{Id: 112, Type: constant.ChannelTypeOpenAI, Key: "C", Models: "gpt-test", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&primary).Error)
	require.NoError(t, db.Create(&backup).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 111, Group: "default", Model: "gpt-test", Enabled: true, Priority: common.GetPointer(int64(10))}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 112, Group: "default", Model: "gpt-test", Enabled: true, Priority: common.GetPointer(int64(0))}).Error)
	scope := model.UpstreamAccountScope{Operation: "chat.completions.create", Model: "gpt-test"}
	refs, _, err := model.ChannelAccountRefs(context.Background(), db, &primary)
	require.NoError(t, err)
	for _, ref := range refs {
		selected, err := model.SelectUpstreamAccount(context.Background(), db, []model.UpstreamAccountRef{ref}, scope, nil, time.Now())
		require.NoError(t, err)
		require.NoError(t, model.RecordUpstreamAccountFailure(context.Background(), db, selected, scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: time.Now().Add(time.Minute)}, time.Now()))
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		c.Next()
		common.CleanupBodyStorage(c)
	})
	engine.POST("/v1/chat/completions", Distribute(), func(c *gin.Context) {
		assert.Equal(t, 112, c.GetInt("channel_id"))
		assert.Equal(t, "C", c.GetString("channel_key"))
		assert.Zero(t, accountpool.State(c).Sends)
		c.Status(204)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	assert.Equal(t, 204, recorder.Code, recorder.Body.String())
	assert.Empty(t, recorder.Header().Get("Retry-After"))
}

func TestUnsupportedPooledTransportStopsBeforeRelayHandler(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	prior := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = prior; require.NoError(t, raw.Close()) })
	require.NoError(t, db.AutoMigrate(&model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	for _, test := range []struct {
		name, path  string
		channelType int
		awsMode     dto.AwsKeyType
	}{
		{name: "realtime", path: "/v1/realtime", channelType: constant.ChannelTypeOpenAI},
		{name: "midjourney", path: "/mj/submit/imagine", channelType: constant.ChannelTypeMidjourney},
		{name: "midjourney-plus", path: "/mj/submit/imagine", channelType: constant.ChannelTypeMidjourneyPlus},
		{name: "xunfei", path: "/v1/chat/completions", channelType: constant.ChannelTypeXunfei},
		{name: "aws-sdk-default", path: "/v1/messages", channelType: constant.ChannelTypeAws},
		{name: "aws-sdk-explicit", path: "/v1/messages", channelType: constant.ChannelTypeAws, awsMode: dto.AwsKeyTypeAKSK},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, pooled := range []bool{true, false} {
				ch := &model.Channel{Id: 700, Type: test.channelType, Key: "credential", ChannelInfo: model.ChannelInfo{IsMultiKey: pooled}}
				ch.SetOtherSettings(dto.ChannelOtherSettings{AwsKeyType: test.awsMode})
				reachedRelay := false
				engine := gin.New()
				method := http.MethodPost
				if test.path == "/v1/realtime" {
					method = http.MethodGet
				}
				engine.Handle(method, test.path, func(c *gin.Context) {
					if apiErr := SetupContextForSelectedChannel(c, ch, "m"); apiErr != nil {
						assert.Equal(t, types.ErrorCodeUnsupportedOperation, apiErr.GetErrorCode())
						assert.True(t, types.IsSkipRetryError(apiErr))
						c.AbortWithStatus(apiErr.StatusCode)
						return
					}
					c.Next()
				}, func(c *gin.Context) { reachedRelay = true; c.Status(204) })
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, httptest.NewRequest(method, test.path, nil))
				assert.Equal(t, !pooled, reachedRelay, "unsupported pools must stop before a handler can charge or send; single-key is unchanged")
				if pooled {
					assert.Equal(t, 400, response.Code)
				} else {
					assert.Equal(t, 204, response.Code)
				}
			}
		})
	}
	var members int64
	require.NoError(t, db.Model(&model.UpstreamAccountMember{}).Count(&members).Error)
	assert.Zero(t, members, "unsupported pools must reject even before account admission")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	apiKeyChannel := &model.Channel{Id: 701, Type: constant.ChannelTypeAws, Key: "api-key|us-east-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	apiKeyChannel.SetOtherSettings(dto.ChannelOtherSettings{AwsKeyType: dto.AwsKeyTypeApiKey})
	require.Nil(t, SetupContextForSelectedChannel(c, apiKeyChannel, "m"), "AWS API-key HTTP pools remain eligible")
	require.NotNil(t, accountpool.State(c).Selected)
	assert.Equal(t, "api-key|us-east-1", common.GetContextKeyString(c, constant.ContextKeyChannelKey))
}
