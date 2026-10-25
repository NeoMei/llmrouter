package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/crypto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/profile"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func gatewayPolicy(t *testing.T, choice any, names ...string) toolpolicy.Policy {
	t.Helper()
	tools := make([]dto.ToolCallRequest, 0, len(names))
	for _, name := range names {
		tools = append(tools, dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{Name: name}})
	}
	policy, err := toolpolicy.BuildFromChat(tools, choice)
	require.NoError(t, err)
	return policy
}

func TestRepairFinalChatRequestUsesContextPolicyAndPreservesRawFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	toolpolicy.Set(c, gatewayPolicy(t, nil, "lookup"))
	body := []byte(`{"model":"m","unknown":{"huge":9007199254740993123456789},"messages":[` +
		`{"role":"assistant","content":null,"provider_extension":{"huge":9007199254740993123456789},` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}` +
		`]}`)

	out, err := RepairFinalChatRequest(c, body)
	require.NoError(t, err)

	var object map[string]json.RawMessage
	require.NoError(t, common.UnmarshalUnique(out, &object))
	assert.JSONEq(t, `{"huge":9007199254740993123456789}`, string(object["unknown"]))
	var messages []json.RawMessage
	require.NoError(t, common.UnmarshalUnique(object["messages"], &messages))
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0]), `"provider_extension"`)
	assert.Contains(t, string(messages[0]), `9007199254740993123456789`)
	var result dto.Message
	require.NoError(t, common.UnmarshalUnique(messages[1], &result))
	assert.Equal(t, "tool", result.Role)
	assert.Equal(t, "call_1", result.ToolCallId)
}

func TestRepairFinalChatRequestFailsClosedWithoutPolicyOnlyWhenCallsExist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	withCalls := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]}`)

	out, err := RepairFinalChatRequest(c, withCalls)
	assert.Nil(t, out)
	require.ErrorIs(t, err, toolpolicy.ErrMissingToolPolicy)

	text := []byte(`{"unknown":9007199254740993123456789,"messages":[{"role":"user","content":"hi"}]}`)
	out, err = RepairFinalChatRequest(c, text)
	require.NoError(t, err)
	assert.Equal(t, text, out, "text-only requests must remain byte-for-byte compatible")

	emptyCalls := []byte(`{"messages":[{"role":"assistant","content":"done","tool_calls":[]}]}`)
	out, err = RepairFinalChatRequest(c, emptyCalls)
	require.NoError(t, err)
	assert.Equal(t, emptyCalls, out, "an empty tool_calls array contains no call and needs no policy")
}

func TestRepairFinalChatRequestRejectsInvalidCallWithoutChangingBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	toolpolicy.Set(c, gatewayPolicy(t, nil, "lookup"))
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"delete_all","arguments":"{}"}}]}]}`)
	original := append([]byte(nil), body...)

	out, err := RepairFinalChatRequest(c, body)

	assert.Nil(t, out)
	require.ErrorIs(t, err, toolpolicy.ErrUndeclaredTool)
	assert.Equal(t, original, body)
}

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

// ─── keypool with in-memory SQLite ───

func setupTestDB(t *testing.T) (*sql.DB, *gorm.DB, *crypto.AESGCM) {
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
	require.NoError(t, gormDB.AutoMigrate(&model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	return db, gormDB, c
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

	db, gormDB, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(gormDB, c)

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

	db, gormDB, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(gormDB, c)

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

	db, gormDB, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(gormDB, c)

	insertPlatformKey(t, db, c, 3, "disabled", nil)

	_, _, err := ks.SelectPlatformKey(ctx, 3)
	require.Error(t, err, "no active key should produce error")
}

func TestGetBYOKKey(t *testing.T) {
	orig := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(orig) })

	db, gormDB, c := setupTestDB(t)
	ctx := context.Background()
	ks := NewKeySelector(gormDB, c)

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
		assert.Equal(t, "https://api.deepseek.com/chat/completions", url)
	})

	t.Run("bracket strip", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "qwen-max[xhigh]"}}
		url, err := a.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://dashscope.aliyuncs.com/chat/completions", url)
		assert.Equal(t, "qwen-max", info.UpstreamModelName, "bracket suffix should be stripped from info")
	})
}

func TestPlatformCredentialRefreshKeepsOwnerAndIsolatesOldOutcome(t *testing.T) {
	db, gormDB, crypto := setupTestDB(t)
	id := insertPlatformKey(t, db, crypto, 33, "active", nil)
	selector := NewKeySelector(gormDB, crypto)
	ctx := context.Background()
	refs, _, owners, err := selector.PlatformAccounts(ctx, 33, 59)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	owner := owners[id]
	require.NoError(t, model.SetUpstreamAccountQuotaGroup(ctx, gormDB, owner, "quota:33", time.Now()))
	selected, _, err := selector.SelectPlatformAccount(ctx, 33, 59, model.UpstreamAccountScope{Operation: "chat.completions.create", Model: "m"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "quota:33", selected.Ref.QuotaGroup)
	encrypted, err := crypto.Encrypt([]byte("refreshed-key"))
	require.NoError(t, err)
	require.NoError(t, gormDB.Model(&model.ProviderKeyPool{}).Where("id = ?", id).Update("encrypted_key", encrypted).Error)
	require.NoError(t, model.RecordUpstreamAccountFailure(ctx, gormDB, selected, model.UpstreamAccountScope{}, model.UpstreamAccountFailure{Kind: model.UpstreamAccountInvalid, Reason: "invalid_credential"}, time.Now()))
	refreshed, key, err := selector.SelectPlatformAccount(ctx, 33, 59, model.UpstreamAccountScope{}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, selected.Ref.MemberID, refreshed.Ref.MemberID)
	assert.Equal(t, "quota:33", refreshed.Ref.QuotaGroup)
	assert.Equal(t, "refreshed-key", key)
	owned, err := selector.ResolveAccountKey(ctx, owner)
	require.NoError(t, err)
	assert.Equal(t, "refreshed-key", owned)
	owner.KeyIndex++
	_, err = selector.ResolveAccountKey(ctx, owner)
	require.Error(t, err)
}

func TestBYOKDisabledOrUnreadableFailsClosed(t *testing.T) {
	db, gormDB, crypto := setupTestDB(t)
	selector := NewKeySelector(gormDB, crypto)
	_, err := db.Exec(`INSERT INTO user_provider_keys(user_id,profile_id,encrypted_key,status) VALUES(1,2,?,'disabled')`, []byte("bad"))
	require.NoError(t, err)
	_, err = selector.GetBYOKKey(context.Background(), 1, 2)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE user_provider_keys SET status='active' WHERE user_id=1`)
	require.NoError(t, err)
	_, err = selector.GetBYOKKey(context.Background(), 1, 2)
	require.Error(t, err)
}

func TestPlatformLegacyCooldownProvidesRetryTimeAndOwnerRemainsManageable(t *testing.T) {
	db, gormDB, cipher := setupTestDB(t)
	id := insertPlatformKey(t, db, cipher, 44, "active", nil)
	retryAt := time.Now().Add(2 * time.Minute)
	require.NoError(t, gormDB.Model(&model.ProviderKeyPool{}).Where("id = ?", id).Update("cooldown_until", retryAt).Error)
	selector := NewKeySelector(gormDB, cipher)
	_, _, err := selector.SelectPlatformAccount(context.Background(), 44, 59, model.UpstreamAccountScope{}, nil)
	var unavailable *model.UpstreamAccountUnavailable
	require.ErrorAs(t, err, &unavailable)
	assert.WithinDuration(t, retryAt, unavailable.RetryAt, time.Millisecond)
	owner, err := selector.ResolvePlatformOwner(context.Background(), 44, 59, id)
	require.NoError(t, err)
	assert.NotEmpty(t, owner.MemberID)
}

func TestPlatformSelectorInstancesShareHealth(t *testing.T) {
	db, gormDB, cipher := setupTestDB(t)
	firstID := insertPlatformKey(t, db, cipher, 55, "active", nil)
	secondID := insertPlatformKey(t, db, cipher, 55, "active", nil)
	first := NewKeySelector(gormDB, cipher)
	second := NewKeySelector(gormDB, cipher)
	scope := model.UpstreamAccountScope{Operation: "chat.completions.create", Model: "m"}
	sent, _, err := first.SelectPlatformAccount(context.Background(), 55, 59, scope, nil)
	require.NoError(t, err)
	assert.Equal(t, firstID, sent.KeyIndex)
	require.NoError(t, model.RecordUpstreamAccountFailure(context.Background(), gormDB, sent, scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: time.Now().Add(time.Minute)}, time.Now()))
	next, _, err := second.SelectPlatformAccount(context.Background(), 55, 59, scope, nil)
	require.NoError(t, err)
	assert.Equal(t, secondID, next.KeyIndex)
	owner, err := second.ResolvePlatformOwner(context.Background(), 55, 59, secondID)
	require.NoError(t, err)
	require.NoError(t, model.SetUpstreamAccountPaused(context.Background(), gormDB, owner, true, time.Now()))
	_, _, err = first.SelectPlatformAccount(context.Background(), 55, 59, scope, nil)
	require.Error(t, err)
}
