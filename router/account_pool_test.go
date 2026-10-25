package router

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/crypto"
	"github.com/QuantumNous/new-api/relay/channel/gateway"
	"github.com/QuantumNous/new-api/relay/profile"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type poolHTTPFixture struct {
	db      *gorm.DB
	engine  *gin.Engine
	channel model.Channel
	tokens  map[string]string
	users   map[string]int
}
type poolHTTPResponse struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Data    struct {
		PoolID    string         `json:"pool_id"`
		Status    string         `json:"status"`
		ProfileID int            `json:"profile_id"`
		Counts    map[string]int `json:"counts"`
		Members   []struct {
			MemberID     string                 `json:"member_id"`
			GenerationID string                 `json:"generation_id"`
			State        string                 `json:"state"`
			QuotaGroup   string                 `json:"quota_group"`
			Indices      []int                  `json:"indices"`
			Blockers     []model.AccountBlocker `json:"blockers"`
		} `json:"members"`
	} `json:"data"`
}

func newPoolHTTPFixture(t *testing.T) *poolHTTPFixture {
	t.Helper()
	previousDB, previousLog := model.DB, model.LOG_DB
	previousType, previousRedis, previousMaster := common.MainDatabaseType(), common.RedisEnabled, common.IsMasterNode
	previousSel, previousLoader := gateway.GlobalKeySel, gateway.GlobalLoader
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.User{}, &model.UserSession{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.Log{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.ProviderProfile{}, &model.ProviderModel{}, &model.ProviderKeyPool{}))
	model.DB, model.LOG_DB = db, db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.IsMasterNode = true
	require.NoError(t, authz.Init(db))
	f := &poolHTTPFixture{db: db, tokens: map[string]string{}, users: map[string]int{}}
	for _, name := range []string{"read", "operate", "write", "none", "common", "root"} {
		token := "pool-admin-" + name
		role := common.RoleAdminUser
		if name == "common" {
			role = common.RoleCommonUser
		}
		if name == "root" {
			role = common.RoleRootUser
		}
		user := model.User{Username: name, Password: "fixture", Role: role, Status: common.UserStatusEnabled, Group: "default", AccessToken: &token, AuthVersion: 1, AffCode: name}
		require.NoError(t, db.Create(&user).Error)
		f.tokens[name] = token
		f.users[name] = user.Id
		require.NoError(t, authz.SetUserPermissions(user.Id, authz.PermissionsMap{authz.ResourceChannel: {authz.ActionRead: name == "read", authz.ActionOperate: name == "operate", authz.ActionWrite: name == "write"}}))
	}
	f.channel = model.Channel{Type: constant.ChannelTypeOpenAI, Key: "private-A\nprivate-B\nprivate-B", Status: common.ChannelStatusEnabled, Models: "fixture-model", ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled}, MultiKeyDisabledReason: map[int]string{0: "SECRET-error-body"}}}
	require.NoError(t, db.Create(&f.channel).Error)
	gin.SetMode(gin.TestMode)
	f.engine = gin.New()
	registerChannelRoutes(f.engine.Group("/api"))
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, time.Second*5, time.Millisecond)
		model.DB, model.LOG_DB = previousDB, previousLog
		common.SetMainDatabaseType(previousType)
		common.RedisEnabled = previousRedis
		common.IsMasterNode = previousMaster
		gateway.GlobalKeySel, gateway.GlobalLoader = previousSel, previousLoader
		if previousDB != nil {
			require.NoError(t, authz.Init(previousDB))
		}
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})
	return f
}
func (f *poolHTTPFixture) request(t *testing.T, user, action string, payload map[string]interface{}) (*httptest.ResponseRecorder, poolHTTPResponse) {
	t.Helper()
	method := http.MethodPost
	url := fmt.Sprintf("/api/channel/%d/account-pool", f.channel.Id)
	var body []byte
	var err error
	if action == "" {
		method = http.MethodGet
		url += "?operation=audio.speech.create&model=fixture-model"
	} else {
		url += "/" + action
		body, err = common.Marshal(payload)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+f.tokens[user])
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	var response poolHTTPResponse
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	return w, response
}
func poolPayload(r poolHTTPResponse, index int) map[string]interface{} {
	return map[string]interface{}{"operation": "audio.speech.create", "model": "fixture-model", "pool_id": r.Data.PoolID, "profile_id": r.Data.ProfileID, "member_id": r.Data.Members[index].MemberID, "generation_id": r.Data.Members[index].GenerationID, "target_scope": "account_global"}
}
func TestAccountPoolHTTPPermissionsAndAudit(t *testing.T) {
	f := newPoolHTTPFixture(t)
	w, snapshot := f.request(t, "read", "", nil)
	require.Equal(t, 200, w.Code)
	require.True(t, snapshot.Success)
	require.Len(t, snapshot.Data.Members, 2)
	assert.Equal(t, 1, snapshot.Data.Counts["legacy_blocked"])
	assert.Equal(t, 1, snapshot.Data.Counts["available"])
	for _, secret := range []string{"private-A", "private-B", "SECRET-error-body", "identity_digest", "fingerprint", "encrypted_key", "last_error"} {
		assert.NotContains(t, w.Body.String(), secret)
	}
	payload := poolPayload(snapshot, 1)
	for _, user := range []string{"", "common", "none", "read", "operate", "write", "root"} {
		t.Run(user, func(t *testing.T) {
			w, _ := f.request(t, user, "", nil)
			expected := 403
			if user == "" {
				expected = 401
			}
			if user == "read" || user == "root" {
				expected = 200
			}
			assert.Equal(t, expected, w.Code)
			for _, action := range []string{"pause", "resume", "recover", "recover-group", "quota-group"} {
				payload["quota_group"] = "shared"
				payload["expected_quota_group"] = ""
				w, _ = f.request(t, user, action, payload)
				allowed := user == "root" || (action == "quota-group" && user == "write") || (action != "quota-group" && user == "operate")
				if !allowed {
					expected = 403
					if user == "" {
						expected = 401
					}
					assert.Equal(t, expected, w.Code)
				} else {
					assert.NotEqual(t, 403, w.Code)
					assert.NotEqual(t, 500, w.Code)
				}
			}
		})
	}
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, time.Second*5, time.Millisecond)
	var logs []model.Log
	require.NoError(t, f.db.Where("type = ?", model.LogTypeManage).Find(&logs).Error)
	// Every authenticated admin write gets exactly one success/failure/denial audit.
	require.Len(t, logs, 25)
	success, denied, failed := false, false, false
	for _, log := range logs {
		assert.NotContains(t, log.Other, "private-")
		assert.NotContains(t, log.Other, "SECRET-error-body")
		success = success || strings.Contains(log.Other, `"result":"success"`)
		denied = denied || strings.Contains(log.Other, `"status":403`)
		failed = failed || strings.Contains(log.Other, `"success":false`)
	}
	assert.True(t, success)
	assert.True(t, denied)
	assert.True(t, failed)
}
func TestAccountPoolHTTPNativeActionsCurrentIdentity(t *testing.T) {
	f := newPoolHTTPFixture(t)
	ctx := context.Background()
	_, snapshot := f.request(t, "read", "", nil)
	payload := poolPayload(snapshot, 1)
	// Read/denied writes never advance selection or pause a member.
	w, _ := f.request(t, "read", "pause", payload)
	require.Equal(t, 403, w.Code)
	_, after := f.request(t, "read", "", nil)
	assert.Equal(t, "available", after.Data.Members[1].State)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel.Id).Update("key", "private-B\nprivate-A").Error)
	w, _ = f.request(t, "operate", "pause", payload)
	require.Equal(t, 200, w.Code)
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, payload["member_id"], after.Data.Members[0].MemberID)
	assert.Equal(t, "paused", after.Data.Members[0].State)
	w, _ = f.request(t, "operate", "resume", payload)
	require.Equal(t, 200, w.Code)
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, "legacy_blocked", after.Data.Members[0].State) // legacy index0 remains disabled
	current := f.channel
	require.NoError(t, f.db.First(&current, f.channel.Id).Error)
	rows, err := model.ChannelAccountManagement(ctx, f.db, &current)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, model.RecordUpstreamAccountFailure(ctx, f.db, model.UpstreamAccountSelection{Ref: rows[0].Ref}, model.UpstreamAccountScope{}, model.UpstreamAccountFailure{Kind: model.UpstreamAccountInvalid, Reason: "invalid_credential"}, now))
	payload["target_scope"] = "selected"
	w, _ = f.request(t, "operate", "recover", payload)
	require.Equal(t, 200, w.Code)
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, "isolated", after.Data.Members[0].State)
	payload["target_scope"] = "account_global"
	w, _ = f.request(t, "operate", "recover", payload)
	require.Equal(t, 200, w.Code)
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, "legacy_blocked", after.Data.Members[0].State)
	payload["quota_group"] = "team-A"
	payload["expected_quota_group"] = ""
	w, _ = f.request(t, "write", "quota-group", payload)
	require.Equal(t, 200, w.Code)
	payload["quota_group"] = "old-group"
	w, _ = f.request(t, "operate", "recover-group", payload)
	assert.Equal(t, 409, w.Code)
	payload["expected_quota_group"] = "team-A"
	payload["quota_group"] = ""
	w, _ = f.request(t, "write", "quota-group", payload)
	require.Equal(t, 200, w.Code)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel.Id).Update("key", "private-C").Error)
	w, _ = f.request(t, "operate", "pause", payload)
	assert.Equal(t, 409, w.Code)
	_, after = f.request(t, "read", "", nil)
	require.Len(t, after.Data.Members, 1)
	assert.NotEqual(t, payload["member_id"], after.Data.Members[0].MemberID)
	info := model.ChannelInfo{IsMultiKey: false}
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel.Id).Update("channel_info", info).Error)
	w, after = f.request(t, "read", "", nil)
	assert.Equal(t, 400, w.Code)
	assert.Equal(t, "not_pool", after.Code)
	w, after = f.request(t, "operate", "pause", payload)
	assert.Equal(t, 400, w.Code)
	assert.Equal(t, "not_pool", after.Code)
}
func TestAccountPoolHTTPGatewayGenerationAndUnavailable(t *testing.T) {
	f := newPoolHTTPFixture(t)
	c, err := crypto.NewAESGCM([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	gateway.GlobalKeySel = gateway.NewKeySelector(f.db, c)
	gateway.GlobalLoader = profile.NewLoader(nil, model.NewGormProfileQuerierForDB(f.db))
	profile := model.ProviderProfile{Name: "fixture", Status: "active"}
	require.NoError(t, f.db.Create(&profile).Error)
	require.NoError(t, f.db.Create(&model.ProviderModel{ProfileId: profile.Id, ModelId: "fixture-model", Status: "active"}).Error)
	encrypted, err := c.Encrypt([]byte("gateway-secret-A"))
	require.NoError(t, err)
	row := model.ProviderKeyPool{ProfileId: profile.Id, EncryptedKey: encrypted, Status: "inactive", LastError: "vendor-secret-body"}
	require.NoError(t, f.db.Create(&row).Error)
	require.NoError(t, f.db.Create(&model.ProviderKeyPool{ProfileId: profile.Id, EncryptedKey: []byte("broken"), Status: "active"}).Error)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel.Id).Updates(map[string]interface{}{"type": constant.ChannelTypeGateway, "channel_info": model.ChannelInfo{}}).Error)
	w, snapshot := f.request(t, "read", "", nil)
	require.Equal(t, 200, w.Code)
	require.Len(t, snapshot.Data.Members, 2)
	assert.Equal(t, "legacy_blocked", snapshot.Data.Members[0].State)
	assert.Equal(t, "unavailable", snapshot.Data.Members[1].State)
	assert.NotContains(t, w.Body.String(), "gateway-secret")
	assert.NotContains(t, w.Body.String(), "vendor-secret")
	payload := poolPayload(snapshot, 0)
	w, _ = f.request(t, "operate", "pause", payload)
	require.Equal(t, 200, w.Code)
	payload["quota_group"] = "global-team"
	payload["expected_quota_group"] = ""
	w, _ = f.request(t, "write", "quota-group", payload)
	require.Equal(t, 200, w.Code)
	encrypted, err = c.Encrypt([]byte("gateway-secret-B"))
	require.NoError(t, err)
	require.NoError(t, f.db.Model(&row).Update("encrypted_key", encrypted).Error)
	_, after := f.request(t, "read", "", nil)
	assert.Equal(t, snapshot.Data.Members[0].MemberID, after.Data.Members[0].MemberID)
	assert.NotEqual(t, snapshot.Data.Members[0].GenerationID, after.Data.Members[0].GenerationID)
	assert.Equal(t, "paused", after.Data.Members[0].State)
	assert.Equal(t, "global-team", after.Data.Members[0].QuotaGroup)
	w, _ = f.request(t, "operate", "recover", payload)
	assert.Equal(t, 409, w.Code)
	w, _ = f.request(t, "operate", "resume", payload)
	require.Equal(t, 200, w.Code)
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, "legacy_blocked", after.Data.Members[0].State)
	payload["profile_id"] = profile.Id + 1
	w, _ = f.request(t, "operate", "pause", payload)
	assert.Equal(t, 400, w.Code)
	payload["profile_id"] = profile.Id
	gateway.GlobalKeySel = nil
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, "unavailable", after.Data.Members[0].State)
	assert.Empty(t, after.Data.Members[0].GenerationID)
	require.NoError(t, f.db.Delete(&row).Error)
	w, _ = f.request(t, "operate", "pause", payload)
	assert.Equal(t, 409, w.Code)
}

func TestAccountPoolHTTPScopedCountsFailuresAndValidation(t *testing.T) {
	f := newPoolHTTPFixture(t)
	var failStateWrite, failStateRead atomic.Bool
	// Install callbacks before requests can enqueue asynchronous audit writes.
	// Toggle only the fault flags while the shared GORM processors are in use.
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register("admin_fixture_write_failure", func(tx *gorm.DB) {
		if failStateWrite.Load() && tx.Statement.Table == "upstream_account_states" {
			tx.AddError(fmt.Errorf("PRIVATE database diagnostic"))
		}
	}))
	require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("admin_fixture_read_failure", func(tx *gorm.DB) {
		if failStateRead.Load() && tx.Statement.Table == "upstream_account_states" {
			tx.AddError(fmt.Errorf("PRIVATE database diagnostic"))
		}
	}))
	ctx := context.Background()
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel.Id).Updates(map[string]interface{}{"key": "private-A\nprivate-B", "channel_info": model.ChannelInfo{IsMultiKey: true}}).Error)
	var ch model.Channel
	require.NoError(t, f.db.First(&ch, f.channel.Id).Error)
	rows, err := model.ChannelAccountManagement(ctx, f.db, &ch)
	require.NoError(t, err)
	now := time.Now().UTC()
	short := now.Add(time.Minute)
	long := now.Add(3 * time.Minute)
	scope := model.UpstreamAccountScope{Operation: "audio.speech.create", Model: "fixture-model"}
	require.NoError(t, model.RecordUpstreamAccountFailure(ctx, f.db, model.UpstreamAccountSelection{Ref: rows[0].Ref}, scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: short}, now))
	require.NoError(t, model.RecordUpstreamAccountFailure(ctx, f.db, model.UpstreamAccountSelection{Ref: rows[0].Ref}, model.UpstreamAccountScope{Operation: scope.Operation}, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: long}, now))
	require.NoError(t, model.RecordUpstreamAccountFailure(ctx, f.db, model.UpstreamAccountSelection{Ref: rows[1].Ref}, scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: short}, now))
	w, snapshot := f.request(t, "read", "", nil)
	require.Equal(t, 200, w.Code)
	assert.Equal(t, 2, snapshot.Data.Counts["cooling"])
	var timed struct {
		Data struct {
			ServerTime time.Time `json:"server_time"`
			RetryAt    time.Time `json:"retry_at"`
			Members    []struct {
				RetryAt time.Time `json:"retry_at"`
			} `json:"members"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &timed))
	assert.Equal(t, short, timed.Data.RetryAt)
	assert.Equal(t, long, timed.Data.Members[0].RetryAt)
	assert.WithinDuration(t, now, timed.Data.ServerTime, time.Second*5)
	payload := poolPayload(snapshot, 0)
	payload["target_scope"] = "selected"
	w, _ = f.request(t, "operate", "recover", payload)
	require.Equal(t, 200, w.Code)
	_, after := f.request(t, "read", "", nil)
	assert.Equal(t, "cooling", after.Data.Members[0].State)
	payload["target_scope"] = "operation"
	w, _ = f.request(t, "operate", "recover", payload)
	require.Equal(t, 200, w.Code)
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, "available", after.Data.Members[0].State)
	payload["target_scope"] = "account_global"
	for _, change := range []map[string]interface{}{{"member_id": strings.ToUpper(rows[0].Ref.MemberID)}, {"pool_id": "wrong:pool"}, {"profile_id": 123}, {"operation": "made.up"}, {"model": "other-model"}, {"target_scope": ""}, {"generation_id": rows[1].Ref.MemberID}} {
		original := poolPayload(snapshot, 0)
		for k, v := range change {
			original[k] = v
		}
		action := "pause"
		if _, ok := change["generation_id"]; ok {
			action = "recover"
		}
		w, _ = f.request(t, "operate", action, original)
		assert.GreaterOrEqual(t, w.Code, 400)
		assert.Less(t, w.Code, 500)
	}
	// A failed state write must not be reported as applied or mutate the pause.
	failStateWrite.Store(true)
	w, failure := f.request(t, "operate", "pause", payload)
	assert.Equal(t, 503, w.Code)
	assert.Equal(t, "state_unavailable", failure.Code)
	assert.NotContains(t, w.Body.String(), "PRIVATE")
	failStateWrite.Store(false)
	_, after = f.request(t, "read", "", nil)
	assert.Equal(t, "available", after.Data.Members[0].State)
	failStateRead.Store(true)
	w, failure = f.request(t, "read", "", nil)
	assert.Equal(t, 503, w.Code)
	assert.Equal(t, "state_unavailable", failure.Code)
	assert.NotContains(t, w.Body.String(), "PRIVATE")
	failStateRead.Store(false)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel.Id).Update("key", "").Error)
	w, after = f.request(t, "read", "", nil)
	require.Equal(t, 200, w.Code)
	assert.Empty(t, after.Data.Members)
	assert.Equal(t, "credential_unavailable", after.Data.Status)
}

func TestAccountPoolMappedScopesMatchLegacyAndCatalogIdentity(t *testing.T) {
	f := newPoolHTTPFixture(t)
	require.NoError(t, f.db.Model(&f.channel).Updates(map[string]interface{}{"models": "alias,second,distinct,self,cycle-a", "model_mapping": `{"alias":"middle","middle":"canonical","second":"canonical","self":"self","cycle-a":"cycle-b","cycle-b":"cycle-a"}`}).Error)
	for _, tc := range []struct {
		model   string
		allowed bool
	}{
		{"canonical", true}, {"middle", true}, {"distinct", true}, {"self", true}, {"cycle-b", true}, {"cycle-a", false}, {"alias", false}, {"unconfigured", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			req := httptest.NewRequest("GET", fmt.Sprintf("/api/channel/%d/account-pool?operation=audio.speech.create&model=%s", f.channel.Id, tc.model), nil)
			req.Header.Set("Authorization", "Bearer "+f.tokens["read"])
			w := httptest.NewRecorder()
			f.engine.ServeHTTP(w, req)
			var result poolHTTPResponse
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
			assert.Equal(t, tc.allowed, result.Success, w.Body.String())
			if !tc.allowed {
				assert.Equal(t, "model_not_in_channel", result.Code)
			}
		})
	}
	require.NoError(t, f.db.Model(&f.channel).Update("model_mapping", `{"alias":1}`).Error)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/channel/%d/account-pool?operation=audio.speech.create&model=canonical", f.channel.Id), nil)
	req.Header.Set("Authorization", "Bearer "+f.tokens["read"])
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	var result poolHTTPResponse
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
	assert.False(t, result.Success)
	assert.Equal(t, "configuration_unavailable", result.Code)
}
