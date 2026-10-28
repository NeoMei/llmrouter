package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWireAuthoritySeparatesProtocolsAndLogicalCallIdentity(t *testing.T) {
	var reserves atomic.Int64
	rate := Rate{ID: "chat", Model: "test", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	grant := Claims{ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", Models: []string{"test"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
	// Decode the proposed external shape so RED exercises existing admission,
	// rather than failing compilation on an absent field.
	require.NoError(t, common.Unmarshal([]byte(`{"operations":{"test":["claude.messages.create"]}}`), &grant))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			out = grant
		case "/internal/v1/rates":
			out = []Rate{rate}
		case "/internal/v1/calls/reserve":
			reserves.Add(1)
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			out = RemoteCall{WireOperation: in["wire_operation"], CallID: in["call_id"], ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", RequestDigest: in["request_digest"], Rate: rate, Status: "RESERVED", Held: in["amount"], CredentialID: "credential", ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter"}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		b, e := common.Marshal(out)
		require.NoError(t, e)
		_, _ = w.Write(b)
	}))
	defer server.Close()
	db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/authority.db"), &gorm.Config{})
	require.NoError(t, e)
	a, e := New(db, Config{Enabled: true, Origin: server.URL, ServiceToken: "authority-service-token-at-least-32", Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, e)
	_, e = a.AdmitOperation(context.Background(), "uh_model_token", "task", "wrong-wire", "chat", "application/json", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	require.Error(t, e, "Messages grant must not authorize OpenAI Chat even with identical tariff")
	assert.Zero(t, reserves.Load())
	var count int64
	require.NoError(t, db.Model(&Call{}).Count(&count).Error)
	assert.Zero(t, count)
	grant.Operations = nil
	_, e = a.AdmitOperation(context.Background(), "uh_model_token", "task", "unscoped-call", "chat", "application/json", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	require.Error(t, e, "unscoped old-service response cannot establish frozen operation authority")
	assert.Zero(t, reserves.Load())
	grant.Operations = map[string][]string{"test": {"chat.completions.create", "claude.messages.create", "gemini.generate"}}
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`)
	session, e := a.AdmitOperation(context.Background(), "uh_model_token", "task", "shared-call", "chat", "application/json", body)
	require.NoError(t, e)
	assert.Equal(t, "chat.completions.create", session.call.WireOperation)
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "shared-call", WireOperation{ID: "claude.messages.create"}, "application/json", body)
	require.ErrorIs(t, e, ErrConflict)
	assert.EqualValues(t, 1, reserves.Load())
	// A pre-v3 call keeps its original digest interpretation and can only be
	// replay-compared as its original managed wire, never migrated to Claude.
	canonical, e := common.Marshal(map[string]any{"model": "test", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "max_tokens": 10})
	require.NoError(t, e)
	oldData, e := common.Marshal(struct {
		Version, Operation, Task, Actor, Account, Origin, Asset, Funding string
		Profile                                                          int
		Body                                                             string
	}{"platform-metered-v2", "chat", "task", "actor", "account", "superwagie", "CREDIT/6", "credits", 0, string(canonical)})
	require.NoError(t, e)
	sum := sha256.Sum256(oldData)
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "shared-call").Updates(map[string]any{"wire_operation": "", "digest_version": "", "digest": hex.EncodeToString(sum[:])}).Error)
	_, e = a.AdmitOperation(context.Background(), "uh_model_token", "task", "shared-call", "chat", "application/json", body)
	require.ErrorIs(t, e, ErrDuplicate)
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "shared-call", WireOperation{ID: "claude.messages.create"}, "application/json", body)
	require.ErrorIs(t, e, ErrConflict)
	gemini := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":10}}`)
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "gemini-call", WireOperation{ID: "gemini.generate", PathModel: "test", Mode: "generateContent"}, "application/json", gemini)
	require.NoError(t, e)
	_, e = a.AdmitWireOperation(context.Background(), "uh_model_token", "task", "gemini-call", WireOperation{ID: "gemini.generate", PathModel: "test", Mode: "streamGenerateContent"}, "application/json", gemini)
	require.ErrorIs(t, e, ErrConflict)
	assert.EqualValues(t, 2, reserves.Load())
}

func TestWireAuthorityDuplicateTariffCannotUseUnsharedPin(t *testing.T) {
	a, db, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	hub.mu.Lock()
	duplicate := map[string]any{}
	for k, v := range hub.rates[1] {
		duplicate[k] = v
	}
	duplicate["id"] = "duplicate-cash"
	hub.rates = append(hub.rates, duplicate)
	hub.mu.Unlock()
	a.config.AssetRateIDs = map[string]map[string]string{"CNY/6": {"test": "cash-rate"}}
	_, err := a.Admit(context.Background(), "uh_model_token", "task", "duplicate-pin", []byte(`{"model":"test","messages":[],"max_tokens":10}`))
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&Call{}).Count(&count).Error)
	assert.Zero(t, count)
	hub.mu.Lock()
	assert.Zero(t, hub.reserveCount)
	hub.mu.Unlock()
}
