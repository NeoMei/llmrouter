package responsesstate

import (
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

// Default returns the process-wide Responses state store. It prefers a Redis
// store when one can be built and Redis is available; otherwise it falls back
// to the bounded in-memory store for single-instance/development use. The
// result is cached so callers on the hot relay path do not rebuild the store.
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
		if store, err := NewRedis(); err == nil {
			defaultSrv = store
			return store, nil
		}
		// Redis is configured but encryption is not; fall through to memory so
		// single-instance development still works. Readiness can surface this.
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
		return nil
	}
	return errStatefulWithoutRedis
}

var errStatefulWithoutRedis = fmt.Errorf("responses state continuity is enabled but Redis is unavailable; disable RESPONSES_STATE_ENABLED or configure REDIS_CONN_STRING for multi-instance production")
