package gateway

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/crypto"
	"github.com/QuantumNous/new-api/relay/profile"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ─── pure-function helpers ───

func TestStripBracketSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gpt-5[xhigh]", "gpt-5"},
		{"deepseek-r1[reasoning:high]", "deepseek-r1"},
		{"claude-sonnet-4", "claude-sonnet-4"},
		{"model[x]  ", "model"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, stripBracketSuffix(c.in), "input=%q", c.in)
	}
}

func TestRebind(t *testing.T) {
	orig := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	query := `SELECT * FROM t WHERE a = ? AND b = ?`

	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	assert.Equal(t, query, rebind(query))

	common.SetMainDatabaseType(common.DatabaseTypeMySQL)
	assert.Equal(t, query, rebind(query))

	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	assert.Equal(t, `SELECT * FROM t WHERE a = $1 AND b = $2`, rebind(query))
}

func TestLockClause(t *testing.T) {
	orig := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	assert.Equal(t, "", lockClause())

	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	assert.Equal(t, " FOR UPDATE SKIP LOCKED", lockClause())

	common.SetMainDatabaseType(common.DatabaseTypeMySQL)
	assert.Equal(t, " FOR UPDATE SKIP LOCKED", lockClause())
}

// ─── keypool with in-memory SQLite ───

func setupTestDB(t *testing.T) (*sql.DB, *crypto.AESGCM) {
	t.Helper()
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TABLE provider_key_pool (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			profile_id INTEGER NOT NULL,
			encrypted_key BLOB NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			cooldown_until DATETIME,
			last_used_at DATETIME,
			last_error TEXT
		);
		CREATE TABLE user_provider_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			profile_id INTEGER NOT NULL,
			encrypted_key BLOB NOT NULL,
			status TEXT NOT NULL DEFAULT 'active'
		);
	`)
	require.NoError(t, err)

	c, err := crypto.NewAESGCM(make([]byte, 32)) // AES-256 with zero key
	require.NoError(t, err)

	t.Cleanup(func() { db.Close() })
	return db, c
}

func insertPlatformKey(t *testing.T, db *sql.DB, c *crypto.AESGCM, profileID int, status string, lastUsed *time.Time) int {
	t.Helper()
	enc, err := c.Encrypt([]byte("sk-pool-key"))
	require.NoError(t, err)
	res, err := db.Exec(
		`INSERT INTO provider_key_pool (profile_id, encrypted_key, status, last_used_at) VALUES (?, ?, ?, ?)`,
		profileID, enc, status, lastUsed,
	)
	require.NoError(t, err)
	id, _ := res.LastInsertId()
	return int(id)
}

func TestSelectPlatformKeyPicksLeastRecentlyUsed(t *testing.T) {
	orig := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	db, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(db, c)

	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-5 * time.Minute)

	insertPlatformKey(t, db, c, 1, "active", &recent)
	idOld := insertPlatformKey(t, db, c, 1, "active", &old)

	keyID, key, err := ks.SelectPlatformKey(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, idOld, keyID, "should pick the least-recently-used key")
	assert.Equal(t, "sk-pool-key", key)
}

func TestSelectPlatformKeySkipsCooldown(t *testing.T) {
	orig := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	db, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(db, c)

	// active key with future cooldown → skipped
	_, err := db.Exec(`UPDATE provider_key_pool SET cooldown_until = ? WHERE id = ?`,
		time.Now().Add(10*time.Minute), insertPlatformKey(t, db, c, 2, "active", nil))
	require.NoError(t, err)

	idGood := insertPlatformKey(t, db, c, 2, "active", nil)

	keyID, _, err := ks.SelectPlatformKey(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, idGood, keyID)
}

func TestSelectPlatformKeySkipsNonActive(t *testing.T) {
	orig := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	db, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(db, c)

	insertPlatformKey(t, db, c, 3, "disabled", nil)

	_, _, err := ks.SelectPlatformKey(ctx, 3)
	require.Error(t, err, "no active key should produce error")
}

func TestMarkCooldown(t *testing.T) {
	orig := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	db, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(db, c)

	id := insertPlatformKey(t, db, c, 4, "active", nil)
	ks.MarkCooldown(ctx, id, 5*time.Minute)

	var status string
	var cooldown sql.NullTime
	require.NoError(t, db.QueryRow(`SELECT status, cooldown_until FROM provider_key_pool WHERE id = ?`, id).Scan(&status, &cooldown))
	assert.Equal(t, "cooldown", status)
	assert.True(t, cooldown.Valid && cooldown.Time.After(time.Now()))
}

func TestMarkDisabled(t *testing.T) {
	orig := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	db, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(db, c)

	id := insertPlatformKey(t, db, c, 5, "active", nil)
	ks.MarkDisabled(ctx, id, "auth failed: 401")

	var status, lastErr string
	require.NoError(t, db.QueryRow(`SELECT status, last_error FROM provider_key_pool WHERE id = ?`, id).Scan(&status, &lastErr))
	assert.Equal(t, "disabled", status)
	assert.Equal(t, "auth failed: 401", lastErr)
}

func TestGetBYOKKey(t *testing.T) {
	orig := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	db, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(db, c)

	enc, err := c.Encrypt([]byte("sk-byok-user-key"))
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO user_provider_keys (user_id, profile_id, encrypted_key, status) VALUES (?, ?, ?, 'active')`,
		42, 10, enc,
	)
	require.NoError(t, err)

	// existing BYOK key
	key, err := ks.GetBYOKKey(ctx, 42, 10)
	require.NoError(t, err)
	assert.Equal(t, "sk-byok-user-key", key)

	// no BYOK key → empty string, no error
	key, err = ks.GetBYOKKey(ctx, 99, 10)
	require.NoError(t, err)
	assert.Equal(t, "", key)
}

// ─── URL building via mock DBQuerier ───

type mockQuerier struct {
	profiles map[string]*profile.ModelProfile
}

func (m *mockQuerier) QueryModelProfile(_ context.Context, modelID string) (*profile.ModelProfile, error) {
	if p, ok := m.profiles[modelID]; ok {
		return p, nil
	}
	return nil, sql.ErrNoRows
}

func TestGetRequestURL(t *testing.T) {
	q := &mockQuerier{
		profiles: map[string]*profile.ModelProfile{
			"deepseek-r1": {
				ModelID:   "deepseek-r1",
				BaseURL:   "https://api.deepseek.com",
				ProfileID: 1,
			},
			"qwen-max[xhigh]": {
				ModelID:    "qwen-max",
				BaseURL:    "https://dashscope.aliyuncs.com",
				ProfileID:  2,
				QuirkRules: profile.QuirkRules{BracketStrip: true},
			},
		},
	}
	loader := profile.NewLoader(nil, q) // nil rdb → always hits DBQuerier

	gin.SetMode(gin.TestMode)
	a := &Adaptor{Loader: loader}

	t.Run("basic URL", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-r1"}}
		url, err := a.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://api.deepseek.com/v1/chat/completions", url)
	})

	t.Run("bracket strip", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "qwen-max[xhigh]"}}
		url, err := a.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://dashscope.aliyuncs.com/v1/chat/completions", url)
		assert.Equal(t, "qwen-max", info.UpstreamModelName, "bracket suffix should be stripped from info")
	})
}
