package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// call invokes a checker handler on an isolated gin engine and returns the
// recorded response.
func call(t *testing.T, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)
	return w
}

func TestHealthz_HealthyWithoutRedis(t *testing.T) {
	c := NewChecker(nil, func(context.Context) error { return nil })
	w := call(t, c.HealthzHandler)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestHealthz_DatabaseDown(t *testing.T) {
	c := NewChecker(nil, func(context.Context) error { return errors.New("conn refused") })
	w := call(t, c.HealthzHandler)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"db_error"`)
}

func TestHealthz_RedisDown(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Shut the redis server so Ping fails while the client is still alive.
	mr.Close()

	c := NewChecker(rdb, func(context.Context) error { return nil })
	w := call(t, c.HealthzHandler)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"redis_error"`)
}

func TestHealthz_RedisHealthy(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	require.NoError(t, rdb.Ping(context.Background()).Err())
	defer rdb.Close()

	c := NewChecker(rdb, func(context.Context) error { return nil })
	w := call(t, c.HealthzHandler)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestReadyz_MirrorsHealthz(t *testing.T) {
	c := NewChecker(nil, func(context.Context) error { return nil })
	w := call(t, c.ReadyzHandler)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}
