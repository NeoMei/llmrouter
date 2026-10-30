package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func nativeQuotaControllerFixture(t *testing.T) (*gorm.DB, *gin.Engine, model.Token) {
	t.Helper()
	oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	model.InitDatabaseColumns()
	t.Cleanup(func() { common.SetDatabaseTypes(oldMain, oldLogType); model.InitDatabaseColumns() })
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/status.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, model.RegisterProductFundingModels(db))
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	oldDB, oldRedis, oldEnabled, oldBatch := model.DB, common.RDB, common.RedisEnabled, common.BatchUpdateEnabled
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	model.DB, common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = db, client, true, true
	t.Cleanup(func() {
		model.DB, common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = oldDB, oldRedis, oldEnabled, oldBatch
		require.NoError(t, client.Close())
		require.NoError(t, conn.Close())
	})
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "native-status", AffCode: "native-status", Status: common.UserStatusEnabled}).Error)
	token := model.Token{Id: 2, UserId: 1, Key: "synthetic-native-status-key", AuthRealm: model.NativeFundingLocalOnly, Name: "native", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100, ModelLimitsEnabled: true, ModelLimits: "m"}
	require.NoError(t, db.Create(&token).Error)
	keyHash := sha256.Sum256([]byte(token.Key))
	raw, err := common.Marshal(model.NativeTokenQuotaManifest{Version: 1, CutoverEvidence: strings.Repeat("a", 64), Tokens: []model.NativeTokenQuotaImport{{TokenID: token.Id, UserID: token.UserId, KeySHA256: hex.EncodeToString(keyHash[:]), RemainQuota: 100, Status: token.Status, ExpiredTime: -1}}})
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	require.NoError(t, model.ImportNativeTokenQuota(context.Background(), db, raw, hex.EncodeToString(digest[:])))
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("id", 1); c.Set("authenticated_native_realm", true); c.Next() })
	engine.PUT("/api/token/", UpdateToken)
	engine.GET("/api/token/:id", GetToken)
	engine.GET("/api/token/", GetAllTokens)
	engine.GET("/api/token/search", SearchTokens)
	return db, engine, token
}

func TestNativeQuotaStatusOnlyControllerPreservesUnflushedHold(t *testing.T) {
	db, engine, token := nativeQuotaControllerFixture(t)
	accepted, err := model.TryReserveTokenQuota(token.Id, token.Key, 80, false, "before-status")
	require.NoError(t, err)
	require.True(t, accepted)
	var projection model.Token
	require.NoError(t, db.First(&projection, token.Id).Error)
	require.Equal(t, 100, projection.RemainQuota)
	for _, status := range []int{common.TokenStatusDisabled, common.TokenStatusEnabled, common.TokenStatusEnabled} {
		request := httptest.NewRequest(http.MethodPut, "/api/token/?status_only=true", strings.NewReader(fmt.Sprintf(`{"id":2,"status":%d}`, status)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), `"success":true`)
	}
	require.NoError(t, db.First(&projection, token.Id).Error)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), token.Id, projection.QuotaPolicyGeneration)
	require.NoError(t, err)
	assert.Equal(t, 20, remain)
	assert.Equal(t, 80, used)
	accepted, err = model.TryReserveTokenQuota(token.Id, token.Key, 80, false, "after-status")
	require.NoError(t, err)
	assert.False(t, accepted)
	assert.True(t, common.RedisEnabled)
	assert.True(t, common.BatchUpdateEnabled)
}

func TestNativeQuotaManagementReadsShowDurableUnflushedBalance(t *testing.T) {
	_, engine, token := nativeQuotaControllerFixture(t)
	accepted, err := model.TryReserveTokenQuota(token.Id, token.Key, 80, false, "before-management-read")
	require.NoError(t, err)
	require.True(t, accepted)
	for _, path := range []string{"/api/token/2", "/api/token/", "/api/token/search?keyword=native"} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), `"remain_quota":20`)
		assert.Contains(t, response.Body.String(), `"used_quota":80`)
	}
	require.NoError(t, model.DisableModelLimits(token.Id))
	var current model.Token
	require.NoError(t, model.DB.First(&current, token.Id).Error)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), token.Id, current.QuotaPolicyGeneration)
	require.NoError(t, err)
	assert.Equal(t, 20, remain)
	assert.Equal(t, 80, used)
}

func TestNativeQuotaStatusOnlyControllerPreservesAdmissionAfterItsRead(t *testing.T) {
	db, engine, token := nativeQuotaControllerFixture(t)
	accepted, err := model.TryReserveTokenQuota(token.Id, token.Key, 40, false, "before-status-read")
	require.NoError(t, err)
	require.True(t, accepted)
	var interleaved atomic.Bool
	var admissionErr error
	var admitted bool
	const hook = "native-status-admission-after-state-read"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "native_token_quota_states" && interleaved.CompareAndSwap(false, true) {
			admitted, admissionErr = model.TryReserveTokenQuota(token.Id, token.Key, 20, false, "between-status-read-and-write")
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(hook) })
	request := httptest.NewRequest(http.MethodPut, "/api/token/?status_only=true", strings.NewReader(`{"id":2,"status":1}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.NoError(t, admissionErr)
	require.True(t, admitted)
	require.True(t, interleaved.Load())
	assert.Contains(t, response.Body.String(), `"success":true`)
	var current model.Token
	require.NoError(t, db.First(&current, token.Id).Error)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), token.Id, current.QuotaPolicyGeneration)
	require.NoError(t, err)
	assert.Equal(t, 40, remain)
	assert.Equal(t, 60, used)
	accepted, err = model.TryReserveTokenQuota(token.Id, token.Key, 41, false, "after-interleaved-status")
	require.NoError(t, err)
	assert.False(t, accepted)
}

func TestNativeQuotaFullControllerUpdateKeepsExplicitAbsoluteQuotaEdit(t *testing.T) {
	db, engine, token := nativeQuotaControllerFixture(t)
	accepted, err := model.TryReserveTokenQuota(token.Id, token.Key, 80, false, "before-explicit-edit")
	require.NoError(t, err)
	require.True(t, accepted)
	request := httptest.NewRequest(http.MethodPut, "/api/token/", strings.NewReader(`{"id":2,"name":"native","status":1,"expired_time":-1,"remain_quota":50,"unlimited_quota":false}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	assert.Contains(t, response.Body.String(), `"success":true`)
	var current model.Token
	require.NoError(t, db.First(&current, token.Id).Error)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), token.Id, current.QuotaPolicyGeneration)
	require.NoError(t, err)
	assert.Equal(t, 50, remain)
	assert.Equal(t, 80, used)
	accepted, err = model.TryReserveTokenQuota(token.Id, token.Key, 51, false, "after-explicit-edit")
	require.NoError(t, err)
	assert.False(t, accepted)
}

// A drawer opened at 100 must not restore that snapshot after a new hold or refund.
func TestNativeQuotaMetadataControllerPreservesConcurrentQuotaAndStatus(t *testing.T) {
	for _, refund := range []bool{false, true} {
		t.Run(fmt.Sprintf("refund_%t", refund), func(t *testing.T) {
			db, engine, token := nativeQuotaControllerFixture(t)
			openedRemain := 100
			if refund {
				accepted, err := model.ReserveNativeTokenQuota(token.Id, token.Key, "drawer-hold", 80)
				require.NoError(t, err)
				require.True(t, accepted)
				openedRemain = 20
			}
			var interleaved atomic.Bool
			var mutationErr error
			const hook = "native-metadata-change-after-controller-read"
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
				if tx.Statement.Table == "native_token_quota_states" && interleaved.CompareAndSwap(false, true) {
					if refund {
						mutationErr = model.RefundNativeTokenQuota(token.Id, token.Key, "drawer-hold")
					} else {
						var accepted bool
						accepted, mutationErr = model.ReserveNativeTokenQuota(token.Id, token.Key, "drawer-hold", 80)
						assert.True(t, accepted)
					}
					if mutationErr == nil {
						mutationErr = db.Model(&model.Token{}).Where("id = ?", token.Id).Update("status", common.TokenStatusDisabled).Error
					}
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(hook) })
			body := fmt.Sprintf(`{"id":2,"name":"renamed only","expired_time":-1,"remain_quota":%d,"unlimited_quota":false,"model_limits_enabled":true,"model_limits":"m"}`, openedRemain)
			request := httptest.NewRequest(http.MethodPut, "/api/token/?preserve_quota=true", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			require.NoError(t, mutationErr)
			require.True(t, interleaved.Load())
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), `"success":true`)
			var current model.Token
			require.NoError(t, db.First(&current, token.Id).Error)
			assert.Equal(t, "renamed only", current.Name)
			assert.Equal(t, common.TokenStatusDisabled, current.Status, "metadata save must not replace a concurrent status change")
			assert.False(t, current.UnlimitedQuota)
			remain, used, err := model.NativeTokenQuotaBalance(context.Background(), token.Id, current.QuotaPolicyGeneration)
			require.NoError(t, err)
			expectedRemain, expectedUsed := 20, 80
			if refund {
				expectedRemain, expectedUsed = 100, 0
			}
			assert.Equal(t, expectedRemain, remain)
			assert.Equal(t, expectedUsed, used)
			current.Status = common.TokenStatusEnabled
			require.NoError(t, current.UpdateStatus(model.NativeFundingLocalOnly))
			accepted, err := model.ReserveNativeTokenQuota(token.Id, token.Key, "after-drawer", expectedRemain+1)
			require.NoError(t, err)
			assert.False(t, accepted)
			assert.True(t, common.RedisEnabled)
			assert.True(t, common.BatchUpdateEnabled)
		})
	}
}

func TestNativeQuotaMetadataControllerRejectsInvalidScope(t *testing.T) {
	for _, query := range []string{"preserve_quota=invalid", "preserve_quota=false", "preserve_quota=true&preserve_quota=true", "preserve_quota=true&status_only=true"} {
		t.Run(query, func(t *testing.T) {
			db, engine, token := nativeQuotaControllerFixture(t)
			request := httptest.NewRequest(http.MethodPut, "/api/token/?"+query, strings.NewReader(`{"id":2,"name":"must not save","expired_time":-1,"remain_quota":5}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.NotContains(t, response.Body.String(), `"success":true`)
			var current model.Token
			require.NoError(t, db.First(&current, token.Id).Error)
			assert.Equal(t, "native", current.Name)
			remain, _, err := model.NativeTokenQuotaBalance(context.Background(), token.Id, current.QuotaPolicyGeneration)
			require.NoError(t, err)
			assert.Equal(t, 100, remain)
		})
	}
}
