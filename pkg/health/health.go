package health

import (
	"context"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/pkg/readiness"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

type Checker struct {
	readiness *readiness.Checker
}

type NamedProbe = readiness.Probe

func NewChecker(rdb *redis.Client, dbPing func(context.Context) error) *Checker {
	return NewCheckerWithProbes(rdb, dbPing)
}

func NewCheckerWithProbes(rdb *redis.Client, dbPing func(context.Context) error, extra ...NamedProbe) *Checker {
	probes := []readiness.Probe{{Name: "database", Check: dbPing}}
	if rdb != nil {
		probes = append(probes, readiness.Probe{
			Name:  "redis",
			Check: func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		})
	}
	probes = append(probes, extra...)
	return &Checker{readiness: readiness.New(2*time.Second, probes...)}
}

func (c *Checker) HealthzHandler(g *gin.Context) {
	g.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (c *Checker) ReadyzHandler(g *gin.Context) {
	result := c.readiness.Status(g.Request.Context())
	status := http.StatusOK
	state := "ok"
	if !result.Ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	g.JSON(status, gin.H{"status": state, "components": result.Components})
}
