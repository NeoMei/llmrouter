package model

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The DSN must point at a dedicated disposable test database; never use production.
func TestUniHubPurgeCrossDBCaseSensitiveReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{{"mysql", "NATIVE_PROOF_MYSQL_DSN", mysql.Open}, {"postgres", "NATIVE_PROOF_POSTGRES_DSN", postgres.Open}} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skip("isolated database not configured")
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.AutoMigrate(&UserSession{}, &UniHubSessionBinding{}))
			oldDB, oldSecret := DB, common.SessionSecret
			DB, common.SessionSecret = db, "crossdb-purge-secret-32-characters"
			t.Cleanup(func() { DB, common.SessionSecret = oldDB, oldSecret })
			now := time.Now().Unix()
			s := UserSession{SID: "cross-sid", UserID: 42, Version: 1, UserAuthVersion: 1, Status: UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "unihub", ExpiresAt: now + 3600}
			require.NoError(t, db.Create(&s).Error)
			b := UniHubSessionBinding{SID: s.SID, UserID: 42, Issuer: "issuer", Subject: "subject", ProductID: "product", InstanceID: "default", ExpiresAt: now - 1}
			aead, err := bindingAEAD()
			require.NoError(t, err)
			nonce := make([]byte, aead.NonceSize())
			b.Ciphertext = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte("fixture"), b.aad()))
			require.NoError(t, db.Create(&b).Error)
			replacement := strings.ToUpper(b.Ciphertext)
			require.NotEqual(t, b.Ciphertext, replacement)
			callback := "test:crossdb_case_only_replace"
			changed := false
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if changed || tx.Statement.Table != "uni_hub_session_bindings" {
					return
				}
				changed = true
				if e := db.Model(&UniHubSessionBinding{}).Where("sid = ?", b.SID).Update("ciphertext", replacement).Error; e != nil {
					tx.AddError(e)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			require.NoError(t, PurgeUniHubSessionBindings(now))
			require.True(t, changed)
			var got UniHubSessionBinding
			require.NoError(t, db.First(&got, "sid = ?", b.SID).Error)
			require.Equal(t, replacement, got.Ciphertext)
			require.NoError(t, db.Callback().Query().Remove(callback))
			require.NoError(t, db.AutoMigrate(&ProductBrokerSessionProof{}))
			broker := ProductBrokerSessionProof{SID: s.SID, UserID: 42, Issuer: "issuer", Subject: "subject", ProductID: "product", InstanceID: "default", ClientID: "client", ExpiresAt: now - 1}
			brokerAEAD, err := brokerProofAEAD()
			require.NoError(t, err)
			nonce = make([]byte, brokerAEAD.NonceSize())
			broker.Ciphertext = base64.RawURLEncoding.EncodeToString(brokerAEAD.Seal(nonce, nonce, []byte("ubsp_"+strings.Repeat("a", 64)), broker.aad()))
			require.NoError(t, db.Create(&broker).Error)
			brokerReplacement := strings.ToUpper(broker.Ciphertext)
			require.NotEqual(t, broker.Ciphertext, brokerReplacement)
			brokerCallback := "test:crossdb_broker_case_only_replace"
			brokerChanged := false
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(brokerCallback, func(tx *gorm.DB) {
				if brokerChanged || tx.Statement.Table != "product_broker_session_proofs" {
					return
				}
				brokerChanged = true
				if e := db.Model(&ProductBrokerSessionProof{}).Where("sid = ?", broker.SID).Update("ciphertext", brokerReplacement).Error; e != nil {
					tx.AddError(e)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(brokerCallback) })
			require.NoError(t, PurgeBrokerSourceProofs(now))
			require.True(t, brokerChanged)
			var saved ProductBrokerSessionProof
			require.NoError(t, db.First(&saved, "sid = ?", broker.SID).Error)
			require.Equal(t, brokerReplacement, saved.Ciphertext)
		})
	}
}
