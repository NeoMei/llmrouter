//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type integrationProbe struct {
	ID    uint `gorm:"primaryKey"`
	Value string
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required for the integration matrix", name)
	}
	return value
}

func TestDatabaseMatrix(t *testing.T) {
	cases := map[string]gorm.Dialector{
		"sqlite":     sqlite.Open(t.TempDir() + "/matrix.db"),
		"mysql":      mysql.Open(requiredEnv(t, "TEST_MYSQL_DSN")),
		"postgresql": postgres.Open(requiredEnv(t, "TEST_POSTGRES_DSN")),
	}
	for name, dialector := range cases {
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&integrationProbe{}))
			value := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
			probe := integrationProbe{Value: value}
			require.NoError(t, db.Create(&probe).Error)
			var loaded integrationProbe
			require.NoError(t, db.First(&loaded, probe.ID).Error)
			require.Equal(t, value, loaded.Value)
		})
	}
}

func TestRedisAtomicClaimsAcrossClients(t *testing.T) {
	options, err := redis.ParseURL(requiredEnv(t, "TEST_REDIS_URL"))
	require.NoError(t, err)
	first := redis.NewClient(options)
	second := redis.NewClient(options)
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	ctx := context.Background()
	key := fmt.Sprintf("integration:claim:%d", time.Now().UnixNano())
	require.NoError(t, first.Del(ctx, key).Err())
	claimed, err := first.SetNX(ctx, key, "worker-a", time.Minute).Result()
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = second.SetNX(ctx, key, "worker-b", time.Minute).Result()
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestBothGatewayInstancesAreReady(t *testing.T) {
	for _, endpoint := range []string{requiredEnv(t, "TEST_GATEWAY_A_URL"), requiredEnv(t, "TEST_GATEWAY_B_URL")} {
		response, err := http.Get(endpoint + "/readyz")
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
	}
}
