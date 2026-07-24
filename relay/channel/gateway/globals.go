package gateway

import (
	"database/sql"
	"encoding/hex"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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
func InitFromEnv(rdb *redis.Client, db *sql.DB) {
	var keySel *KeySelector
	if aesKey := os.Getenv("GATEWAY_AES_KEY"); aesKey != "" {
		keyBytes := decodeAESKey(aesKey)
		c, err := crypto.NewAESGCM(keyBytes)
		if err != nil {
			common.SysLog("GATEWAY_AES_KEY invalid (" + err.Error() + "), platform key pool disabled")
		} else if db != nil {
			keySel = NewKeySelector(db, c)
		}
	}

	// Wire the profile loader with the GORM-backed querier.
	// This requires model.DB to be initialized before InitFromEnv is called.
	var loader *profile.Loader
	if querier := model.NewGormProfileQuerier(); querier != nil {
		loader = profile.NewLoader(rdb, querier)
	}

	InitGlobals(loader, keySel)
}

// decodeAESKey tries hex-decoding first (standard for key exchange), falls back
// to raw bytes for ASCII keys. Returns nil if the result isn't a valid AES key size.
func decodeAESKey(s string) []byte {
	// Try hex decode first
	if decoded, err := hex.DecodeString(s); err == nil {
		switch len(decoded) {
		case 16, 24, 32:
			return decoded
		}
	}
	// Fall back to raw bytes
	switch len(s) {
	case 16, 24, 32:
		return []byte(s)
	}
	// Not a valid key size — return as-is and let AES-GCM reject it
	return []byte(s)
}
