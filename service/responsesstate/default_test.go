package responsesstate

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func TestDefaultFallsBackToMemory(t *testing.T) {
	ResetForTest()
	t.Setenv(stateEnabledEnv, "1")
	store, err := Default()
	require.NoError(t, err)
	require.NotNil(t, store)

	// Second call returns the cached instance.
	again, err := Default()
	require.NoError(t, err)
	require.Same(t, store, again)

	ResetForTest()
}

func TestEnabledReflectsEnv(t *testing.T) {
	t.Setenv(stateEnabledEnv, "")
	require.False(t, Enabled())
	t.Setenv(stateEnabledEnv, "1")
	require.True(t, Enabled())
}

func TestReadinessCheckPassesWhenDisabled(t *testing.T) {
	t.Setenv(stateEnabledEnv, "")
	require.NoError(t, ReadinessCheck())
}

func TestRedisConfiguredWithoutEncryptionKeyFailsClosed(t *testing.T) {
	previousRDB := common.RDB
	common.RDB = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousRDB
		ResetForTest()
	})
	t.Setenv(stateEnabledEnv, "1")
	t.Setenv("GATEWAY_AES_KEY", "")
	ResetForTest()

	store, err := Default()
	require.Nil(t, store)
	require.ErrorContains(t, err, "GATEWAY_AES_KEY")
	require.ErrorContains(t, ReadinessCheck(), "GATEWAY_AES_KEY")
}

func TestRedisConfiguredWithBadEncryptionKeyFailsClosed(t *testing.T) {
	previousRDB := common.RDB
	common.RDB = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousRDB
		ResetForTest()
	})
	t.Setenv(stateEnabledEnv, "1")
	t.Setenv("GATEWAY_AES_KEY", "too-short")
	ResetForTest()

	store, err := Default()
	require.Nil(t, store)
	require.ErrorContains(t, err, "16, 24, or 32")
	require.ErrorContains(t, ReadinessCheck(), "16, 24, or 32")
}

func TestReadinessChecksSelectedRedisStoreConnectivity(t *testing.T) {
	server := miniredis.RunT(t)
	previousRDB := common.RDB
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousRDB
		ResetForTest()
	})
	t.Setenv(stateEnabledEnv, "1")
	t.Setenv("GATEWAY_AES_KEY", "0123456789abcdef0123456789abcdef")
	ResetForTest()
	require.NoError(t, common.RDB.Ping(context.Background()).Err())
	require.NoError(t, ReadinessCheck())

	server.Close()
	require.Error(t, ReadinessCheck())
}
