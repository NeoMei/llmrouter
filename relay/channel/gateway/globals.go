package gateway

import (
	"database/sql"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/crypto"
	"github.com/QuantumNous/new-api/relay/profile"

	"github.com/go-redis/redis/v8"
)

// Package-level globals wired during startup (main.InitResources). They default
// to nil so the process compiles and starts even when the provider subsystem is
// not configured; the Adaptor is nil-safe and falls back to its inner adaptor.
var (
	GlobalLoader *profile.Loader
	GlobalKeySel *KeySelector
)

// InitGlobals wires the profile loader and platform-key selector. Passing nil
// for either disables that feature at runtime (adaptor methods are nil-safe).
func InitGlobals(loader *profile.Loader, keySel *KeySelector) {
	GlobalLoader = loader
	GlobalKeySel = keySel
}

// AESGCM returns the process-wide AES-GCM instance used for provider key
// encryption, or nil if the gateway subsystem is not configured.
func AESGCM() *crypto.AESGCM {
	if GlobalKeySel == nil {
		return nil
	}
	return GlobalKeySel.crypto
}

// InitFromEnv wires GlobalLoader and GlobalKeySel from runtime infrastructure.
//
// Each component is optional and degrades gracefully:
//   - A missing/invalid GATEWAY_AES_KEY leaves GlobalKeySel nil; the adaptor
//     then keeps whatever Authorization header the inner adaptor set.
//   - The production profile DBQuerier (provider_models JOIN provider_profiles)
//     is added in the model-layer task. Until then GlobalLoader stays nil and
//     the adaptor delegates to its inner OpenAI adaptor. Wire it here once the
//     querier exists: GlobalLoader = profile.NewLoader(rdb, querier).
func InitFromEnv(rdb *redis.Client, db *sql.DB) {
	var keySel *KeySelector
	if aesKey := os.Getenv("GATEWAY_AES_KEY"); aesKey != "" {
		c, err := crypto.NewAESGCM([]byte(aesKey))
		if err != nil {
			common.SysLog("GATEWAY_AES_KEY invalid (" + err.Error() + "), platform key pool disabled")
		} else if db != nil {
			keySel = NewKeySelector(db, c)
		}
	}
	// ponytail: GlobalLoader intentionally nil until the profile DBQuerier
	// (provider_models JOIN provider_profiles) lands in the model-layer task.
	// Adaptor nil-guards make this safe; nil Loader => delegate to Inner.
	InitGlobals(nil, keySel)
}
