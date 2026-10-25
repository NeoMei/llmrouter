package platformbilling

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Removing exclusive normalization or accepting estimated usage must fail this contract.
func TestConfirmedUsageNormalizesExclusiveCounters(t *testing.T) {
	u := &dto.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 20, CacheWriteTokens: 10}, CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 15}}
	got, err := NormalizeUsage(u)
	require.NoError(t, err)
	assert.Equal(t, Usage{Input: "70", Output: "35", CacheRead: "20", CacheWrite: "10", Reasoning: "15"}, got)
	u.TotalTokens = -1
	_, err = NormalizeUsage(u)
	require.Error(t, err)
	u.TotalTokens = 150
	u.PromptTokensDetails.CachedTokens = 200
	_, err = NormalizeUsage(u)
	require.Error(t, err)
}

func fixture(t *testing.T, failSettle *bool, reserveFailure ...bool) (*Adapter, *gorm.DB) {
	t.Helper()
	var reserved map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer service-service-service-service-token", r.Header.Get("Authorization"))
		var v any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			v = Claims{ID: "cred", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "product", Models: []string{"test"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
		case "/internal/v1/rates":
			v = []Rate{{ID: "rate", Model: "test", Status: "published", Semantics: "exclusive_v1", Input: Rational{"1", "1"}, Output: Rational{"2", "1"}, CacheRead: Rational{"1", "1"}, CacheWrite: Rational{"1", "1"}, Reasoning: Rational{"2", "1"}}}
		case "/internal/v1/calls/call/settle":
			if *failSettle {
				w.WriteHeader(503)
				return
			}
			v = RemoteCall{CallID: "call", Status: "SETTLED"}
		case "/internal/v1/calls/reserve":
			require.NoError(t, common.DecodeJson(r.Body, &reserved))
			if len(reserveFailure) > 0 && reserveFailure[0] {
				w.WriteHeader(503)
				return
			}
			v = RemoteCall{CallID: "call", Status: "RESERVED"}
		case "/internal/v1/calls/call":
			v = RemoteCall{CallID: "call", Status: "RESERVED"}
		case "/internal/v1/calls/call/release":
			v = RemoteCall{CallID: "call", Status: "RELEASED"}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if remote, ok := v.(RemoteCall); ok {
			remote.ActorID = "actor"
			remote.AccountID = "account"
			remote.OriginProductID = "product"
			remote.RequestDigest = reserved["request_digest"]
			remote.Rate = Rate{ID: "rate", Model: "test"}
			v = remote
		}
		b, e := common.Marshal(v)
		require.NoError(t, e)
		_, _ = w.Write(b)
	}))
	t.Cleanup(server.Close)
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/platform.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: server.URL, ServiceToken: "service-service-service-service-token", Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	return a, db
}

// Losing settlement responses must leave durable intent; restart must never call providers.
func TestDurableSettlementDuplicateAndRestart(t *testing.T) {
	fail := true
	a, db := fixture(t, &fail)
	ctx := context.Background()
	s, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[],"max_tokens":50}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(ctx, 1))
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	require.NoError(t, s.Finish(ctx))
	var row Call
	require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
	assert.Equal(t, "SETTLE_PENDING", row.Status)
	_, err = a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[],"max_tokens":50}`))
	assert.ErrorIs(t, err, ErrDuplicate)
	_, err = a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[],"max_tokens":49}`))
	assert.ErrorIs(t, err, ErrConflict)
	fail = false
	require.NoError(t, a.Recover(ctx))
	require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
	assert.Equal(t, "SETTLED", row.Status)
	var count int64
	require.NoError(t, db.Model(&Attempt{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestUnknownAndMissingUsageNeverRelease(t *testing.T) {
	for _, dispatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_usage", true: "crash_after_dispatch"}[dispatch], func(t *testing.T) {
			fail := false
			a, db := fixture(t, &fail)
			ctx := context.Background()
			s, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
			require.NoError(t, err)
			require.NoError(t, s.Dispatch(ctx, 1))
			if !dispatch {
				require.NoError(t, s.Finish(ctx))
			}
			require.NoError(t, a.Recover(ctx))
			var row Call
			require.NoError(t, db.First(&row).Error)
			assert.Equal(t, "UNKNOWN", row.Status)
		})
	}
}

func TestAdmissionRejectsUntrustedScopeAndBounds(t *testing.T) {
	fail := false
	a, _ := fixture(t, &fail)
	for _, tt := range []struct{ task, id, body string }{{"other", "call", `{"model":"test","max_tokens":50}`}, {"task", "", `{"model":"test","max_tokens":50}`}, {"task", "call", `{"model":"test","max_tokens":101}`}, {"task", "call", `{"model":"test","max_tokens":50,"group":"other"}`}, {"task", "call", `{"model":"unknown","max_tokens":50}`}, {"task", "call", `{"model":"test","max_tokens":50,"web_search_options":{}}`}} {
		_, err := a.Admit(context.Background(), "uh_model_token", tt.task, tt.id, []byte(tt.body))
		require.Error(t, err)
	}
}

func TestConfigDisabledByDefaultAndRejectsUnsafeServiceOrigin(t *testing.T) {
	c, err := ConfigFromEnv(func(string) string { return "" })
	require.NoError(t, err)
	assert.False(t, c.Enabled)
	a, err := New(nil, c)
	require.NoError(t, err)
	assert.False(t, a.Enabled())
	for _, origin := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=x"} {
		_, err := newClient(Config{Origin: origin, ServiceToken: "service-service-service-service-token"})
		require.Error(t, err)
	}
}
func TestUniHubRedirectNeverReceivesServiceToken(t *testing.T) {
	leaked := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer dest.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
	defer source.Close()
	c, err := newClient(Config{Origin: source.URL, ServiceToken: "service-service-service-service-token"})
	require.NoError(t, err)
	require.Error(t, c.do(context.Background(), "GET", "/internal/v1/rates", "", nil, nil))
	assert.False(t, leaked)
}
func TestDefinitiveProviderRetryPersistsSeparateAttempts(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail)
	ctx := context.Background()
	s, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(ctx, 1))
	assert.False(t, s.CanRetry())
	s.Rejected()
	assert.True(t, s.CanRetry())
	require.NoError(t, s.Dispatch(ctx, 2))
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: 5})
	require.NoError(t, s.Finish(ctx))
	var attempts []Attempt
	require.NoError(t, db.Order("id").Find(&attempts).Error)
	require.Len(t, attempts, 2)
	assert.Equal(t, "REJECTED", attempts[0].Status)
	assert.Equal(t, 2, attempts[1].ChannelID)
}

func TestReceiptRequiresExactVerifiedScopeWithoutRedispatch(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail)
	ctx := context.Background()
	_, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
	require.NoError(t, err)
	receipt, err := a.Receipt(ctx, "uh_model_reissued", "task", "call")
	require.NoError(t, err)
	assert.Equal(t, "RESERVED", receipt.Status)
	assert.Equal(t, "call", receipt.CallID)
	_, err = a.Receipt(ctx, "uh_model_reissued", "other-task", "call")
	require.Error(t, err)
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "call").Update("account_id", "other-account").Error)
	_, err = a.Receipt(ctx, "uh_model_reissued", "task", "call")
	require.Error(t, err)
	var n int64
	require.NoError(t, db.Model(&Attempt{}).Count(&n).Error)
	assert.Zero(t, n)
}
func TestTextContentArraysAndAttemptTerminalAudit(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail)
	s, err := a.Admit(context.Background(), "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":50}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(context.Background(), 1))
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: 5})
	require.NoError(t, s.Finish(context.Background()))
	var attempt Attempt
	require.NoError(t, db.First(&attempt).Error)
	assert.Equal(t, "USAGE_CONFIRMED", attempt.Status)
}

// A global remote call-ID collision must never release another account's hold.
func TestRecoveryRefusesRemotePayerCollision(t *testing.T) {
	released := false
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/calls/collision/release" {
			released = true
		}
		b, _ := common.Marshal(RemoteCall{CallID: "collision", Status: "RESERVED", ActorID: "different", AccountID: "victim", OriginProductID: "product", RequestDigest: "other"})
		_, _ = w.Write(b)
	}))
	defer hub.Close()
	db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
	require.NoError(t, e)
	a, e := New(db, Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, e)
	require.NoError(t, db.Create(&Call{CallID: "collision", ActorID: "actor", AccountID: "account", OriginProductID: "product", Digest: "digest", Status: "RESERVE_UNKNOWN"}).Error)
	require.NoError(t, a.Recover(context.Background()))
	assert.False(t, released)
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "RESERVE_UNKNOWN", row.Status)
}

func TestLostReservationResponseReconcilesWithoutProviderDispatch(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail, true)
	ctx := context.Background()
	_, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
	require.Error(t, err)
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "RESERVE_UNKNOWN", row.Status)
	require.NoError(t, a.Recover(ctx))
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "RELEASED", row.Status)
	var n int64
	require.NoError(t, db.Model(&Attempt{}).Count(&n).Error)
	assert.Zero(t, n)
}

func TestInvalidFinalUsageDoesNotChargeEarlierPartialCounters(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail)
	s, e := a.Admit(context.Background(), "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
	require.NoError(t, e)
	require.NoError(t, s.Dispatch(context.Background(), 1))
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: 5})
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: -1})
	require.NoError(t, s.Finish(context.Background()))
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "UNKNOWN", row.Status)
}

func TestExpiredUndispatchedLeaseCannotDispatchAfterRecovery(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail)
	ctx := context.Background()
	s, e := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
	require.NoError(t, e)
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "call").UpdateColumn("updated_at", time.Now().Add(-2*time.Minute)).Error)
	require.NoError(t, a.Recover(ctx))
	require.Error(t, s.Dispatch(ctx, 1))
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "RELEASED", row.Status)
	var n int64
	require.NoError(t, db.Model(&Attempt{}).Count(&n).Error)
	assert.Zero(t, n)
}
