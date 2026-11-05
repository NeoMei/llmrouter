package router

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type admissionFailingBody struct{}

func (admissionFailingBody) Read([]byte) (int, error) {
	return 0, errors.New("request stream interrupted")
}

func TestPlatformBodyLimitRejectsBeforeAdmission(t *testing.T) {
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/body-limit.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var authorityCalls atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorityCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(hub.Close)
	adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("f", 32), InstanceID: "default", PrincipalID: 1, Group: "platform", MaxBodyBytes: 1024, MaxInputTokens: 65536, MaxOutputTokens: 4096})
	require.NoError(t, err)
	engine := gin.New()
	SetPlatformRouter(engine, adapter)
	body, err := common.Marshal(map[string]any{"model": "test", "messages": []map[string]string{{"role": "user", "content": strings.Repeat("x", 1024)}}, "stream": true, "max_tokens": 16})
	require.NoError(t, err)
	for _, path := range []string{"/platform/v1/chat/completions", "/platform/v1/embeddings"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer uh_model_fixture")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-UniHub-Task-ID", "task-body-limit")
			r.Header.Set("Idempotency-Key", "call-body-limit")
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, r)
			require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
			var response struct {
				Error struct {
					Code         string `json:"code"`
					Stage        string `json:"stage"`
					MaxBodyBytes int64  `json:"max_body_bytes"`
				} `json:"error"`
			}
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
			assert.Equal(t, "platform_request_body_too_large", response.Error.Code)
			assert.Equal(t, "pre_admission", response.Error.Stage)
			assert.EqualValues(t, 1024, response.Error.MaxBodyBytes)
			assert.Empty(t, w.Header().Get("X-UniHub-Call-ID"))
		})
	}
	assert.Zero(t, authorityCalls.Load())
	var calls, attempts int64
	require.NoError(t, db.Model(&platformbilling.Call{}).Count(&calls).Error)
	require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
	assert.Zero(t, calls)
	assert.Zero(t, attempts)
}

func TestPlatformBodyReadFailureIsNotSizeRejection(t *testing.T) {
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/body-read.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var authorityCalls atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorityCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(hub.Close)
	adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("f", 32), InstanceID: "default", PrincipalID: 1, Group: "platform", MaxBodyBytes: 1024, MaxInputTokens: 65536, MaxOutputTokens: 4096})
	require.NoError(t, err)
	engine := gin.New()
	SetPlatformRouter(engine, adapter)
	for _, path := range []string{"/platform/v1/chat/completions", "/platform/v1/embeddings"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, path, io.MultiReader(strings.NewReader(`{"model":"test"`), admissionFailingBody{}))
			r.Header.Set("Authorization", "Bearer uh_model_fixture")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-UniHub-Task-ID", "task-body-read")
			r.Header.Set("Idempotency-Key", "call-body-read")
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, r)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			var response map[string]map[string]any
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
			assert.Equal(t, "platform_request_body_read_failed", response["error"]["code"])
			assert.Equal(t, "pre_admission", response["error"]["stage"])
			assert.NotContains(t, response["error"], "max_body_bytes")
			assert.Empty(t, w.Header().Get("X-UniHub-Call-ID"))
		})
	}
	assert.Zero(t, authorityCalls.Load())
	var calls, attempts int64
	require.NoError(t, db.Model(&platformbilling.Call{}).Count(&calls).Error)
	require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
	assert.Zero(t, calls)
	assert.Zero(t, attempts)
}
