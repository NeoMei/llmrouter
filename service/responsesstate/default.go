package responsesstate

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// stateEnabled is the environment flag that turns on Responses continuity.
// Keeping it opt-in preserves existing behaviour for deployments that do not
// want stateful replay, while allowing multi-instance production to require
// Redis via readiness.
const stateEnabledEnv = "RESPONSES_STATE_ENABLED"

var (
	mu         sync.RWMutex
	defaultSrv Store
	defaultErr error
)

// Enabled reports whether Responses continuity is turned on for this process.
func Enabled() bool {
	return os.Getenv(stateEnabledEnv) != ""
}

// Default returns the process-wide Responses state store. Configured Redis is
// authoritative and construction errors fail closed; memory is selected only
// when Redis is absent. The result is cached for the relay hot path.
func Default() (Store, error) {
	mu.RLock()
	if defaultSrv != nil || defaultErr != nil {
		store, err := defaultSrv, defaultErr
		mu.RUnlock()
		return store, err
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if defaultSrv != nil || defaultErr != nil {
		return defaultSrv, defaultErr
	}

	if common.RDB != nil {
		store, err := NewRedis()
		if err != nil {
			defaultErr = fmt.Errorf("construct Redis responses state store: %w", err)
			return nil, defaultErr
		}
		defaultSrv = store
		return store, nil
	}
	store := NewMemory(DefaultRecordTTL, 32*1024*1024)
	defaultSrv = store
	return store, nil
}

// ResetForTest clears the cached store. It exists for tests that need to
// reconfigure the process-wide store between cases.
func ResetForTest() {
	mu.Lock()
	defaultSrv = nil
	defaultErr = nil
	mu.Unlock()
}

// ReadinessCheck returns nil when Responses continuity is safe for the current
// deployment, or an error describing why it is not. It enforces the plan
// invariant that stateful compatibility must use Redis in multi-instance
// production, because the in-memory fallback is only correct for a single
// process.
func ReadinessCheck() error {
	if !Enabled() {
		return nil
	}
	if common.RDB != nil {
		store, err := Default()
		if err != nil {
			return err
		}
		if _, ok := store.(*RedisStore); !ok {
			return fmt.Errorf("responses state selected a non-Redis store while Redis is configured")
		}
		if err := common.RDB.Ping(context.Background()).Err(); err != nil {
			return fmt.Errorf("responses state Redis connectivity check failed: %w", err)
		}
		return nil
	}
	return errStatefulWithoutRedis
}

var errStatefulWithoutRedis = fmt.Errorf("responses state continuity is enabled but Redis is unavailable; disable RESPONSES_STATE_ENABLED or configure REDIS_CONN_STRING for multi-instance production")
