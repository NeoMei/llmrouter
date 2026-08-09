package router

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/health"
	"github.com/QuantumNous/new-api/pkg/observability"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/service/objectstore"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func SetRouter(router *gin.Engine, assets WebAssets) {
	observability.Init()

	// Health + metrics endpoints (registered before web fallback / auth so they
	// remain reachable during incidents). Healthz/readyz ping DB + Redis.
	dbPing := func(ctx context.Context) error {
		sqlDB, err := model.DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
	store, storeErr := objectstore.Default()
	checker := health.NewCheckerWithProbes(common.RDB, dbPing,
		health.NamedProbe{
			Name:  "capability_registry",
			Check: func(context.Context) error { return capability.DefaultRegistry().Ready() },
		},
		health.NamedProbe{
			Name: "object_store",
			Check: func(ctx context.Context) error {
				if storeErr != nil {
					return storeErr
				}
				return store.Health(ctx)
			},
		},
	)
	router.GET("/healthz", checker.HealthzHandler)
	router.GET("/readyz", checker.ReadyzHandler)
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	SetApiRouter(router)
	SetDashboardRouter(router)
	SetRelayRouter(router)
	SetVideoRouter(router)
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if common.IsMasterNode && frontendBaseUrl != "" {
		frontendBaseUrl = ""
		common.SysLog("FRONTEND_BASE_URL is ignored on master node")
	}
	if frontendBaseUrl == "" {
		SetWebRouter(router, assets)
	} else {
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(func(c *gin.Context) {
			c.Set(middleware.RouteTagKey, "web")
			c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
		})
	}
}
