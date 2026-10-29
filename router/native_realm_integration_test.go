package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// nativeRealmFixtureDB is enabled only by the explicit acceptance test below.
// The operator must supply an empty, disposable database and Redis database.
func nativeRealmFixtureDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	if os.Getenv("NATIVE_REALM_ACTIVE_FIXTURE") != "1" {
		db, e := gorm.Open(sqlite.Open(path), &gorm.Config{})
		require.NoError(t, e)
		return db
	}
	if os.Getenv("NATIVE_REALM_INTEGRATION") != "1" || os.Getenv("NATIVE_REALM_TEST_ISOLATED") != "1" {
		t.Fatal("active integration fixture requires isolated opt-in")
	}
	dsn := os.Getenv("NATIVE_REALM_TEST_POSTGRES_DSN")
	if validateNativeRealmPostgresURL(dsn) != nil {
		t.Fatal("isolated PostgreSQL URL is unsafe")
	}
	parsed, e := url.Parse(dsn)
	if e != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || !nativeRealmLoopback(parsed.Hostname()) || !strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), "native_realm_test_") {
		t.Fatal("isolated PostgreSQL DSN must use postgres:// URL form")
	}
	admin, e := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal("isolated PostgreSQL connection failed")
	}
	schema := "native_realm_" + strings.ReplaceAll(common.GetUUID(), "-", "")
	require.NoError(t, admin.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec(`DROP SCHEMA "`+schema+`" CASCADE`).Error)
		sql, e := admin.DB()
		require.NoError(t, e)
		require.NoError(t, sql.Close())
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, e := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal("isolated PostgreSQL schema connection failed")
	}

	return db
}

func nativeRealmFixtureCache(t *testing.T) {
	t.Helper()
	if os.Getenv("NATIVE_REALM_ACTIVE_FIXTURE") != "1" {
		return
	}
	if os.Getenv("NATIVE_REALM_INTEGRATION") != "1" || os.Getenv("NATIVE_REALM_TEST_ISOLATED") != "1" {
		t.Fatal("active integration fixture requires isolated opt-in")
	}
	parsed, err := url.Parse(os.Getenv("NATIVE_REALM_TEST_REDIS_URL"))
	if err != nil || !nativeRealmLoopback(parsed.Hostname()) {
		t.Fatal("isolated Redis must use a loopback URL")
	}
	opt, e := redis.ParseURL(os.Getenv("NATIVE_REALM_TEST_REDIS_URL"))
	require.NoError(t, e, "invalid isolated Redis URL")
	client := redis.NewClient(opt)
	require.NoError(t, client.Ping(context.Background()).Err(), "isolated Redis unavailable")
	size, e := client.DBSize(context.Background()).Result()
	require.NoError(t, e)
	require.Zero(t, size, "acceptance requires empty disposable Redis database")
	old, enabled, batch, mainType, logType := common.RDB, common.RedisEnabled, common.BatchUpdateEnabled, common.MainDatabaseType(), common.LogDatabaseType()
	common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = client, true, true
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	model.InitDatabaseColumns()
	t.Cleanup(func() {
		require.NoError(t, client.Close())
		common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = old, enabled, batch
		common.SetDatabaseTypes(mainType, logType)
		model.InitDatabaseColumns()
	})
}

// TestNativeRealmRealPostgresRedisBatch is the strict, opt-in end-to-end
// acceptance entry. It uses a single disposable schema and an initially empty
// Redis database, with only loopback providers; it never flushes Redis.
func TestNativeRealmPostgresRedisBatchAcceptance(t *testing.T) {
	if os.Getenv("NATIVE_REALM_INTEGRATION") != "1" {
		t.Skip("explicit isolated integration opt-in required")
	}
	if os.Getenv("NATIVE_REALM_TEST_ISOLATED") != "1" {
		t.Fatal("dedicated isolated services must be explicitly confirmed")
	}
	pgraw, redisraw := os.Getenv("NATIVE_REALM_TEST_POSTGRES_DSN"), os.Getenv("NATIVE_REALM_TEST_REDIS_URL")
	if pgraw == "" || redisraw == "" {
		t.Fatal("both isolated PostgreSQL and Redis settings are required")
	}
	pgurl, err := url.Parse(pgraw)
	if err != nil || validateNativeRealmPostgresURL(pgraw) != nil {
		t.Fatal("PostgreSQL must use a loopback URL")
	}
	redisurl, err := url.Parse(redisraw)
	if err != nil || (redisurl.Scheme != "redis" && redisurl.Scheme != "rediss") || !nativeRealmLoopback(redisurl.Hostname()) {
		t.Fatal("Redis must use a loopback URL")
	}
	options, err := redis.ParseURL(redisraw)
	if err != nil {
		t.Fatal("invalid Redis setting")
	}
	client := redis.NewClient(options)
	if err = client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		t.Fatal("isolated Redis is unavailable")
	}
	size, err := client.DBSize(context.Background()).Result()
	if err != nil || size != 0 {
		_ = client.Close()
		t.Fatal("isolated Redis database must be empty")
	}
	admin, err := gorm.Open(postgres.Open(pgraw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = client.Close()
		t.Fatal("isolated PostgreSQL is unavailable")
	}
	var version int
	if err = admin.Raw("SHOW server_version_num").Scan(&version).Error; err != nil || version < 150000 {
		_ = client.Close()
		t.Fatal("PostgreSQL 15 or later is required")
	}
	schema := "native_accept_" + strings.ReplaceAll(common.GetUUID(), "-", "")
	if err = admin.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		_ = client.Close()
		t.Fatal("cannot create isolated schema")
	}
	query := pgurl.Query()
	query.Set("search_path", schema)
	pgurl.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(pgurl.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		_ = client.Close()
		t.Fatal("cannot open isolated schema")
	}
	dbSQL, err := db.DB()
	if err != nil {
		t.Fatal("cannot open isolated database pool")
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal("cannot open isolated administration pool")
	}
	oldDB, oldLog := model.DB, model.LOG_DB
	oldRedis, oldEnabled, oldBatch := common.RDB, common.RedisEnabled, common.BatchUpdateEnabled
	oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldSecret, oldCache, oldRetry, oldLogConsume := common.SessionSecret, common.MemoryCacheEnabled, common.RetryTimes, common.LogConsumeEnabled
	model.DB, model.LOG_DB = db, db
	common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = client, true, true
	common.SessionSecret = "isolated-native-realm-integration-secret"
	common.MemoryCacheEnabled = false
	common.RetryTimes = 0
	common.LogConsumeEnabled = true
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	model.InitDatabaseColumns()
	t.Cleanup(func() {
		if !assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond, "fixture work must finish before cleanup") {
			return
		}
		model.FlushBatchUpdates()
		model.DB, model.LOG_DB = oldDB, oldLog
		common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldEnabled, oldBatch
		common.SessionSecret, common.MemoryCacheEnabled, common.RetryTimes, common.LogConsumeEnabled = oldSecret, oldCache, oldRetry, oldLogConsume
		common.SetDatabaseTypes(oldMain, oldLogType)
		model.InitDatabaseColumns()
		_ = dbSQL.Close()
		_ = admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		_ = adminSQL.Close()
		_ = client.Close()
	})
	if err = db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.UniHubIdentityMapping{}, &model.UniHubSessionBinding{}, &model.ProductAuthLifecycle{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}); err != nil {
		t.Fatal("isolated schema migration failed")
	}
	if err = model.RegisterProductFundingModels(db); err != nil {
		t.Fatal("isolated billing schema migration failed")
	}
	require.Equal(t, "postgres", db.Dialector.Name())
	require.True(t, common.RedisEnabled)
	require.True(t, common.BatchUpdateEnabled)
	t.Run("native_wallet_session_refund", func(t *testing.T) { nativeRealmRealExercise(t, db, client) })
	t.Fatal("acceptance incomplete: admission limits, restart and cross-process recovery are not yet verified")
}

func nativeRealmLoopback(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

func validateNativeRealmPostgresURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || !nativeRealmLoopback(u.Hostname()) || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "native_realm_test_") || u.User == nil || u.User.Username() == "" || u.Port() == "" {
		return fmt.Errorf("unsafe PostgreSQL fixture")
	}
	// pgx permits query parameters and PG* environment values to override
	// URL fields. Accept only non-routing query settings and reject ambient
	// libpq settings so the checked host and database are authoritative.
	for key, values := range u.Query() {
		if (key != "sslmode" && key != "connect_timeout") || len(values) != 1 {
			return fmt.Errorf("unsafe PostgreSQL fixture")
		}
	}
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "PG") {
			return fmt.Errorf("ambient PostgreSQL settings are not allowed")
		}
	}
	config, err := pgx.ParseConfig(raw)
	if err != nil || !nativeRealmLoopback(config.Host) || config.Database != strings.TrimPrefix(u.Path, "/") || !strings.HasPrefix(config.Database, "native_realm_test_") {
		return fmt.Errorf("unsafe PostgreSQL fixture")
	}
	for _, fallback := range config.Fallbacks {
		if !nativeRealmLoopback(fallback.Host) {
			return fmt.Errorf("unsafe PostgreSQL fixture")
		}
	}
	return nil
}

func nativeRealmRealExercise(t *testing.T, db *gorm.DB, client *redis.Client) {
	t.Helper()
	enableUniHubBoundary(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	t.Setenv("LOG_SQL_DSN", "")
	common.CriticalRateLimitEnable = false
	common.GlobalApiRateLimitEnable = false
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	service.InitTokenEncoders()
	ratio_setting.InitRatioSettings()
	oldRatios := ratio_setting.ModelRatio2JSONString()
	fixtureModel := "fixture-" + strings.ReplaceAll(common.GetUUID(), "-", "")
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(fmt.Sprintf(`{%q:1}`, fixtureModel)))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(oldRatios) })
	password, err := common.Password2Hash("synthetic-native-password")
	require.NoError(t, err)
	user := model.User{Id: 901, Username: "integration-native", AffCode: "integration-native", Password: password, Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100000, Group: "default", Setting: `{"billing_preference":"wallet_only"}`}
	token := model.Token{Id: 902, UserId: 901, Key: "syntheticnativekey", Name: "original", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100000}
	ownedRedisKeys := []string{"user:901", "user:904", "auth:user:fence:901", "auth:user:version:901", "auth:user:fence:904", "auth:user:version:904", "ratelimit:user:901", "ratelimit:token:902", "new-api:subscription_plan:v1:905", "new-api:subscription_plan_info:v1:sub:906", "token:" + common.GenerateHMAC(token.Key), "token:fence:" + common.GenerateHMAC(token.Key)}
	t.Cleanup(func() {
		if !assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond) {
			return
		}
		if len(ownedRedisKeys) > 0 {
			assert.NoError(t, client.Del(context.Background(), ownedRedisKeys...).Err())
		}
		var fixtureCursor uint64
		for {
			keys, next, err := client.Scan(context.Background(), fixtureCursor, "perf:"+fixtureModel+":*", 100).Result()
			if !assert.NoError(t, err) {
				break
			}
			if len(keys) > 0 {
				assert.NoError(t, client.Del(context.Background(), keys...).Err())
			}
			fixtureCursor = next
			if fixtureCursor == 0 {
				break
			}
		}
		remaining, err := client.DBSize(context.Background()).Result()
		assert.NoError(t, err)
		if remaining != 0 {
			prefixes := map[string]int{}
			var cursor uint64
			for {
				keys, next, scanErr := client.Scan(context.Background(), cursor, "*", 100).Result()
				if scanErr != nil {
					t.Error("could not inspect remaining Redis key categories")
					break
				}
				for _, key := range keys {
					// Global rate limiting is shared namespace, never delete it.
					if key == "ratelimit:global" {
						continue
					}
					parts := strings.Split(key, ":")
					prefix := parts[0]
					if len(parts) > 1 && (parts[0] == "auth" || parts[0] == "ratelimit" || parts[0] == "perf") {
						prefix += ":" + parts[1]
					}
					prefixes[prefix]++
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
			if len(prefixes) > 0 {
				t.Errorf("unexpected keys are retained; categories: %v", prefixes)
			}
		}
	})
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&token).Error)
	pHash := sha256.Sum256([]byte(password))
	kHash := sha256.Sum256([]byte(token.Key))
	manifest := model.NativeRealmManifest{Version: 1, Users: []model.NativeRealmEnrollment{{UserID: user.Id, Role: user.Role, Status: user.Status, AuthVersion: user.AuthVersion, PasswordSHA256: hex.EncodeToString(pHash[:]), Keys: []model.NativeKeyEnrollment{{ID: token.Id, SecretSHA256: hex.EncodeToString(kHash[:])}}}}}
	raw, err := common.Marshal(manifest)
	require.NoError(t, err)
	mHash := sha256.Sum256(raw)
	require.NoError(t, model.EnrollNativeRealm(context.Background(), db, raw, hex.EncodeToString(mHash[:])))
	var calls atomic.Int32
	var fail atomic.Bool
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"id":"fixture","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}`, fixtureModel)))
	}))
	t.Cleanup(supplier.Close)
	require.NoError(t, db.Create(&model.Channel{Id: 903, Type: constant.ChannelTypeOpenAI, Name: "fixture", Key: "synthetic", BaseURL: &supplier.URL, Models: fixtureModel, Group: "default", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 903, Group: "default", Model: fixtureModel, Enabled: true}).Error)
	engine := gin.New()
	SetApiRouter(engine)
	SetRelayRouter(engine)
	request := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://dashboard.test")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	login := request("POST", "/api/user/login", "", `{"username":"integration-native","password":"synthetic-native-password"}`)
	require.Equal(t, http.StatusOK, login.Code, "native login must succeed")
	var loginData struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(login.Body.Bytes(), &loginData))
	require.True(t, loginData.Success)
	require.NotEmpty(t, loginData.Data.AccessToken)
	identity, err := service.ParseAccessToken(loginData.Data.AccessToken)
	require.NoError(t, err)
	ownedRedisKeys = append(ownedRedisKeys, "auth:session:"+common.GenerateHMACWithKey([]byte("user-session-cache-v1:"+common.SessionSecret), identity.SessionID))
	isNative, err := service.IsNativeLoginSession(context.Background(), identity)
	require.NoError(t, err)
	require.True(t, isNative)
	created := request("POST", "/api/token/", loginData.Data.AccessToken, `{"name":"native-created","remain_quota":100,"expired_time":-1,"unlimited_quota":false}`)
	require.Equal(t, http.StatusOK, created.Code)
	require.Contains(t, created.Body.String(), `"success":true`)
	var createdKey model.Token
	require.NoError(t, db.Where("user_id = ? AND id <> ?", user.Id, token.Id).First(&createdKey).Error)
	ownedRedisKeys = append(ownedRedisKeys, "token:"+common.GenerateHMAC(createdKey.Key), "token:fence:"+common.GenerateHMAC(createdKey.Key))
	require.Equal(t, model.NativeFundingLocalOnly, createdKey.AuthRealm)
	target := model.User{Id: 904, Username: "integration-target", AffCode: "integration-target", Password: password, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default"}
	require.NoError(t, db.Create(&target).Error)
	targetManifest := model.NativeRealmManifest{Version: 1, Users: []model.NativeRealmEnrollment{{UserID: target.Id, Role: target.Role, Status: target.Status, AuthVersion: target.AuthVersion, PasswordSHA256: hex.EncodeToString(pHash[:])}}}
	targetRaw, err := common.Marshal(targetManifest)
	require.NoError(t, err)
	targetHash := sha256.Sum256(targetRaw)
	require.NoError(t, model.EnrollNativeRealm(context.Background(), db, targetRaw, hex.EncodeToString(targetHash[:])))
	changed := request("POST", "/api/user/manage", loginData.Data.AccessToken, fmt.Sprintf(`{"id":%d,"action":"disable"}`, target.Id))
	require.Equal(t, http.StatusOK, changed.Code)
	require.Contains(t, changed.Body.String(), `"success":true`)
	require.NoError(t, db.First(&target, target.Id).Error)
	require.Equal(t, common.UserStatusDisabled, target.Status)
	_, err = model.GetUserCache(user.Id)
	require.NoError(t, err)
	_, err = model.GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
	cacheKey := "token:" + common.GenerateHMAC(token.Key)
	quota, err := client.HGet(context.Background(), cacheKey, "RemainQuota").Int()
	require.NoError(t, err)
	require.Equal(t, 100000, quota)
	chatBody := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":10}`, fixtureModel)
	success := request("POST", "/v1/chat/completions", "sk-"+token.Key, chatBody)
	require.Equal(t, http.StatusOK, success.Code, "native original key must reach the loopback model")
	require.EqualValues(t, 1, calls.Load())
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
	model.FlushBatchUpdates()
	var afterUser model.User
	var afterToken model.Token
	require.NoError(t, db.First(&afterUser, user.Id).Error)
	require.NoError(t, db.First(&afterToken, token.Id).Error)
	require.Equal(t, 99990, afterUser.Quota)
	require.Equal(t, 99990, afterToken.RemainQuota)
	require.Equal(t, 10, afterToken.UsedQuota)
	quota, err = client.HGet(context.Background(), cacheKey, "RemainQuota").Int()
	require.NoError(t, err)
	require.Equal(t, 99990, quota)
	cache, err := model.GetUserCache(user.Id)
	require.NoError(t, err)
	require.Equal(t, afterUser.Quota, cache.Quota)
	fail.Store(true)
	failed := request("POST", "/v1/chat/completions", "sk-"+token.Key, chatBody)
	require.GreaterOrEqual(t, failed.Code, 400)
	require.EqualValues(t, 2, calls.Load())
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
	model.FlushBatchUpdates()
	require.NoError(t, db.First(&afterUser, user.Id).Error)
	require.NoError(t, db.First(&afterToken, token.Id).Error)
	require.Equal(t, 99990, afterUser.Quota, "failed supplier call must refund exactly once")
	require.Equal(t, 99990, afterToken.RemainQuota)
	require.Equal(t, 10, afterToken.UsedQuota)
	quota, err = client.HGet(context.Background(), cacheKey, "RemainQuota").Int()
	require.NoError(t, err)
	require.Equal(t, 99990, quota)
	cache, err = model.GetUserCache(user.Id)
	require.NoError(t, err)
	require.Equal(t, afterUser.Quota, cache.Quota)
	// Change the persisted billing preference to subscription-only and
	// invalidate only this fixture's user cache before exercising it.
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Update("setting", `{"billing_preference":"subscription_only"}`).Error)
	require.NoError(t, client.Del(context.Background(), "user:901").Err())
	plan := model.SubscriptionPlan{Id: 905, Title: "fixture plan", Enabled: true, TotalAmount: 100000}
	require.NoError(t, db.Create(&plan).Error)
	sub := model.UserSubscription{Id: 906, UserId: user.Id, PlanId: plan.Id, Status: "active", StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(), AmountTotal: 100000}
	require.NoError(t, db.Create(&sub).Error)
	fail.Store(false)
	beforeCalls := calls.Load()
	subSuccess := request("POST", "/v1/chat/completions", "sk-"+token.Key, chatBody)
	require.Equal(t, http.StatusOK, subSuccess.Code, "subscription call must reach fixture")
	require.Equal(t, beforeCalls+1, calls.Load())
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
	model.FlushBatchUpdates()
	require.NoError(t, db.First(&sub, sub.Id).Error)
	require.EqualValues(t, 10, sub.AmountUsed)
	require.NoError(t, db.First(&afterUser, user.Id).Error)
	require.Equal(t, 99990, afterUser.Quota, "subscription charge must not debit wallet")
	fail.Store(true)
	subFail := request("POST", "/v1/chat/completions", "sk-"+token.Key, chatBody)
	require.GreaterOrEqual(t, subFail.Code, 400)
	require.Equal(t, beforeCalls+2, calls.Load())
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
	model.FlushBatchUpdates()
	require.NoError(t, db.First(&sub, sub.Id).Error)
	require.EqualValues(t, 10, sub.AmountUsed, "failed subscription call must refund exactly once")
	require.NoError(t, db.First(&afterToken, token.Id).Error)
	require.Equal(t, 99980, afterToken.RemainQuota)
	require.Equal(t, 20, afterToken.UsedQuota)
	// Redis can be stale after an administrator reduces a key's limit. The
	// current SQL row must prevent a forged unlimited cache from dispatching.
	limitChange := request("PUT", "/api/token/", loginData.Data.AccessToken, fmt.Sprintf(`{"id":%d,"name":"original","status":1,"expired_time":-1,"unlimited_quota":false,"remain_quota":5}`, token.Id))
	require.Equal(t, http.StatusOK, limitChange.Code)
	require.Contains(t, limitChange.Body.String(), `"success":true`)
	require.NoError(t, db.First(&afterToken, token.Id).Error)
	require.False(t, afterToken.UnlimitedQuota)
	require.Equal(t, 5, afterToken.RemainQuota)
	// Reconstitute a complete old cache entry to reproduce a delayed stale
	// writer after the real management mutation and fence were published.
	require.NoError(t, client.HSet(context.Background(), cacheKey, "Id", token.Id, "UserId", token.UserId, "Status", token.Status, "ExpiredTime", -1, "Name", "original", "Group", "default", "UsedQuota", 20).Err())
	require.NoError(t, client.HSet(context.Background(), cacheKey, "UnlimitedQuota", "true", "RemainQuota", "100000").Err())
	stale, err := model.GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	require.True(t, stale.UnlimitedQuota, "the Redis read must actually observe the stale unlimited flag")
	fail.Store(false)
	beforeLimitCalls := calls.Load()
	limited := request("POST", "/v1/chat/completions", "sk-"+token.Key, chatBody)
	require.GreaterOrEqual(t, limited.Code, 400, "stale unlimited cache must not bypass SQL key limit")
	require.Equal(t, beforeLimitCalls, calls.Load(), "over-limit request must not reach upstream")
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
	model.FlushBatchUpdates()
	require.NoError(t, db.First(&afterToken, token.Id).Error)
	require.Equal(t, 5, afterToken.RemainQuota)
	require.Equal(t, 20, afterToken.UsedQuota)
	var centralRequests atomic.Int32
	centralServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/product-runtime/identity/901" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		centralRequests.Add(1)
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: "901", UserID: "central-fixture", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "http://127.0.0.1:1", Subject: "central-fixture", User: userkit.User{ID: "central-fixture"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Permissions: []string{"product:use"}, ProductRole: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: 1, AuthVersion: 1}
		b, err := common.Marshal(identity)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(centralServer.Close)
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", centralServer.URL)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "http://127.0.0.1:1", User: platformaccount.User{ID: "central-fixture"}}, user.Id)
	require.NoError(t, err)
	central, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	ownedRedisKeys = append(ownedRedisKeys, "auth:session:"+common.GenerateHMACWithKey([]byte("user-session-cache-v1:"+common.SessionSecret), central.Session.SID))
	denied := request("POST", "/api/user/manage", central.AccessToken, fmt.Sprintf(`{"id":%d,"action":"enable"}`, target.Id))
	require.Greater(t, centralRequests.Load(), int32(0), "central authority must be online and consulted")
	require.Equal(t, http.StatusForbidden, denied.Code, "central ordinary credential must not inherit native admin authority")
	require.NoError(t, db.First(&target, target.Id).Error)
	require.Equal(t, common.UserStatusDisabled, target.Status)
	nativeEnable := request("POST", "/api/user/manage", loginData.Data.AccessToken, fmt.Sprintf(`{"id":%d,"action":"enable"}`, target.Id))
	require.Equal(t, http.StatusOK, nativeEnable.Code, "native admin must retain management authority with central online")
	require.Contains(t, nativeEnable.Body.String(), `"success":true`)
	require.NoError(t, db.First(&target, target.Id).Error)
	require.Equal(t, common.UserStatusEnabled, target.Status)
	self := request("GET", "/api/user/self", loginData.Data.AccessToken, "")
	require.Equal(t, http.StatusOK, self.Code)
	require.Contains(t, self.Body.String(), `"role":100`)
	require.NoError(t, client.Ping(context.Background()).Err())
}
