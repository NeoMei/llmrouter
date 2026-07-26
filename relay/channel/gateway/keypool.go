package gateway

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/crypto"
)

// ponytail: 平台 key 选择逻辑参见 spec Section 6.1
// 跨数据库兼容：MySQL/SQLite 用 ? 占位符，PostgreSQL 用 $N；SKIP LOCKED 仅在
// MySQL/PostgreSQL 上使用，SQLite 单写者模型不需要行锁。

type KeySelector struct {
	db     *sql.DB
	crypto *crypto.AESGCM
}

func NewKeySelector(db *sql.DB, c *crypto.AESGCM) *KeySelector {
	return &KeySelector{db: db, crypto: c}
}

// SelectPlatformKey selects the least-recently-used active platform key using
// SKIP LOCKED to avoid contention. The key's last_used_at is bumped inside the
// transaction so concurrent selectors pick different keys.
func (ks *KeySelector) SelectPlatformKey(ctx context.Context, profileID int) (keyID int, decryptedKey string, err error) {
	tx, err := ks.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()

	query := `
		SELECT id, encrypted_key FROM provider_key_pool
		WHERE profile_id = ? AND status = 'active'
		  AND (cooldown_until IS NULL OR cooldown_until < ?)
		ORDER BY (last_used_at IS NULL) DESC, last_used_at ASC
		LIMIT 1` + lockClause()

	var encrypted []byte
	now := time.Now()
	err = tx.QueryRowContext(ctx, rebind(query), profileID, now).Scan(&keyID, &encrypted)
	if err != nil {
		return 0, "", fmt.Errorf("no available platform key: %w", err)
	}

	_, err = tx.ExecContext(ctx, rebind(`UPDATE provider_key_pool SET last_used_at = ? WHERE id = ?`), now, keyID)
	if err != nil {
		return 0, "", err
	}

	if err = tx.Commit(); err != nil {
		return 0, "", err
	}

	decryptedKey, err = decryptKey(ks.crypto, encrypted)
	return keyID, decryptedKey, err
}

// GetBYOKKey looks up a user's active BYOK key. Returns ("", nil) when the user
// has no BYOK key for the profile (not a BYOK user).
func (ks *KeySelector) GetBYOKKey(ctx context.Context, userID, profileID int) (string, error) {
	var encrypted []byte
	err := ks.db.QueryRowContext(ctx, rebind(`
		SELECT encrypted_key FROM user_provider_keys
		WHERE user_id = ? AND profile_id = ? AND status = 'active'
	`), userID, profileID).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return "", nil // 不是 BYOK 用户
	}
	if err != nil {
		return "", err
	}
	return decryptKey(ks.crypto, encrypted)
}

// MarkCooldown puts a platform key into cooldown for the given duration.
// Only sets cooldown_until — does NOT change status, so the key automatically
// becomes selectable again once cooldown_until < NOW().
func (ks *KeySelector) MarkCooldown(ctx context.Context, keyID int, duration time.Duration) {
	ks.db.ExecContext(ctx, rebind(`
		UPDATE provider_key_pool
		SET cooldown_until = ?
		WHERE id = ?
	`), time.Now().Add(duration), keyID)
}

// MarkDisabled permanently disables a platform key with the triggering error.
func (ks *KeySelector) MarkDisabled(ctx context.Context, keyID int, errMsg string) {
	ks.db.ExecContext(ctx, rebind(`
		UPDATE provider_key_pool
		SET status = 'disabled', last_error = ?
		WHERE id = ?
	`), errMsg, keyID)
}

func decryptKey(c *crypto.AESGCM, encrypted []byte) (string, error) {
	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt key: %w", err)
	}
	return string(decrypted), nil
}

// lockClause returns " FOR UPDATE SKIP LOCKED" on MySQL/PostgreSQL and "" on
// SQLite (no SKIP LOCKED syntax; single-writer model suffices).
func lockClause() string {
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return ""
	}
	return " FOR UPDATE SKIP LOCKED"
}

// rebind converts ? placeholders to $1, $2, ... for PostgreSQL. MySQL and SQLite
// use ? natively.
func rebind(query string) string {
	if !common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
