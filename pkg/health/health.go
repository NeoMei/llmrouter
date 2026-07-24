package health

import (
	"context"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

// Checker verifies liveness/readiness of backing stores (DB + Redis).
// rdb may be nil when Redis is disabled (see common.InitRedisClient); the
// Redis check is then skipped.
type Checker struct {
	rdb *redis.Client
	db  func(context.Context) error
}

// NewChecker builds a Checker. dbPing should ping the primary database.
func NewChecker(rdb *redis.Client, dbPing func(context.Context) error) *Checker {
	return &Checker{rdb: rdb, db: dbPing}
}

// HealthzHandler reports liveness: 200 if DB (and Redis, when enabled) are
// reachable, 503 otherwise.
func (c *Checker) HealthzHandler(g *gin.Context) {
	ctx, cancel := context.WithTimeout(g.Request.Context(), 2*time.Second)
	defer cancel()

	if err := c.db(ctx); err != nil {
		common.SysError("healthz db ping failed: " + err.Error())
		g.JSON(http.StatusServiceUnavailable, gin.H{"status": "db_error"})
		return
	}
	if c.rdb != nil {
		if err := c.rdb.Ping(ctx).Err(); err != nil {
			common.SysError("healthz redis ping failed: " + err.Error())
			g.JSON(http.StatusServiceUnavailable, gin.H{"status": "redis_error"})
			return
		}
	}
	g.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ReadyzHandler reports readiness. Currently identical to liveness.
// ponytail: 首版 readyz = healthz，后续可加 profile 预热检查
func (c *Checker) ReadyzHandler(g *gin.Context) {
	c.HealthzHandler(g)
}
