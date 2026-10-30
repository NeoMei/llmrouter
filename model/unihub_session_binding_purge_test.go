package model

import (
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUniHubPurgeDoesNotDeleteConcurrentReplacement(t *testing.T) {
	oldDB, oldSecret := DB, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "binding.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&UserSession{}, &UniHubSessionBinding{}))
	DB, common.SessionSecret = db, "binding-purge-test-secret-32-bytes"
	t.Cleanup(func() {
		DB, common.SessionSecret = oldDB, oldSecret
		conn, e := db.DB()
		if e == nil {
			_ = conn.Close()
		}
	})
	now := time.Now().Unix()
	session := UserSession{SID: "sid", UserID: 42, Version: 1, UserAuthVersion: 1, Status: UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "unihub", ExpiresAt: now + 3600}
	require.NoError(t, db.Create(&session).Error)
	b := UniHubSessionBinding{SID: "sid", UserID: 42, Issuer: "issuer", Subject: "subject", ProductID: "product", InstanceID: "default", ExpiresAt: now - 1}
	aead, err := bindingAEAD()
	require.NoError(t, err)
	nonce := make([]byte, aead.NonceSize())
	b.Ciphertext = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte("fixture"), b.aad()))
	require.NoError(t, db.Create(&b).Error)
	callback := "test:replace_binding_before_delete"
	changed := false
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if changed || tx.Statement.Table != "uni_hub_session_bindings" {
			return
		}
		changed = true
		if e := db.Model(&UniHubSessionBinding{}).Where("sid = ?", b.SID).Update("ciphertext", "replacement").Error; e != nil {
			tx.AddError(e)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	require.NoError(t, PurgeUniHubSessionBindings(now))
	require.True(t, changed)
	var saved UniHubSessionBinding
	require.NoError(t, db.First(&saved, "sid = ?", b.SID).Error)
	require.Equal(t, "replacement", saved.Ciphertext)
}
