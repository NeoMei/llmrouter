package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type managedKnowledgeFixture struct {
	credential        string
	reserveInput      map[string]string
	mutateClaims      func(int, *platformbilling.Claims)
	acceptGeneratedID bool
}

func (m *managedKnowledgeFixture) verify(f *pairedFixture, w http.ResponseWriter, r *http.Request, in map[string]string) any {
	assert.Empty(f.t, r.Header.Get("X-UniHub-Actor-Authorization"))
	assert.Equal(f.t, map[string]string{"token": m.credential, "model": f.name, "wire_operation": f.wire, "instance_id": "default"}, in)
	f.claimChecks++
	if f.claimChecks == 2 && f.beforeClaim != nil {
		f.beforeClaim()
	}
	if f.revoked {
		w.WriteHeader(http.StatusForbidden)
		return nil
	}
	claims := platformbilling.Claims{CredentialKind: "api_key", InstanceID: "default", ID: "parent-key", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: f.product, Models: []string{f.name}, Operations: map[string][]string{f.name: {f.wire}}, ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second), Asset: "CREDIT/6", FundingMode: "credits"}
	if m.mutateClaims != nil {
		m.mutateClaims(f.claimChecks, &claims)
	}
	return claims
}

func newManagedKnowledgeFixture(t *testing.T, embedding bool) *pairedFixture {
	t.Helper()
	f := newPairedFixture(t, embedding)
	if embedding {
		f.body = `{"model":"unihub-text-embedding-v4","input":["test"]}`
	} else {
		f.body = `{"model":"qwen3-rerank","query":"test","documents":["test"],"top_n":1}`
	}
	f.managed = &managedKnowledgeFixture{credential: "uh_key_managed_fixture"}
	sum := sha256.Sum256([]byte(f.managed.credential + "\x00managed-knowledge"))
	f.callID = "uk_" + hex.EncodeToString(sum[:])
	f.taskID = "task_" + f.callID
	f.engine = gin.New()
	SetManagedKeyDispatch(f.engine, f.adapter)
	return f
}

func managedKnowledgeRequest(f *pairedFixture, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodPost, strings.TrimPrefix(f.path, "/platform"), strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.managed.credential)
	r.Header.Set("Idempotency-Key", "managed-knowledge")
	if edit != nil {
		edit(r)
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}

func TestManagedKnowledgePublicRoutesUseParentKey(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		name := "rerank"
		if embedding {
			name = "embedding"
		}
		t.Run(name, func(t *testing.T) {
			f := newManagedKnowledgeFixture(t, embedding)
			w := managedKnowledgeRequest(f, f.body, nil)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, f.callID, w.Header().Get("X-UniHub-Call-ID"))
			assert.Equal(t, 2, f.claimChecks, "verify the parent key at admission and immediately before provider dispatch")
			assert.Equal(t, 1, f.reserves)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
			require.NotNil(t, f.managed.reserveInput)
			assert.Equal(t, f.managed.credential, f.managed.reserveInput["credential"])
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", f.callID).Error)
			assert.Equal(t, "parent-key", row.CredentialID)
			assert.Equal(t, "api_key", row.CredentialKind)
			assert.Equal(t, "SETTLED", row.Status)
			assert.Equal(t, "actor", row.ActorID)
			assert.Equal(t, "account", row.AccountID)
			assert.Equal(t, f.taskID, row.TaskID)
			assert.NotContains(t, row.MediaSnapshot, f.managed.credential)
			var attempts int64
			require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Where("call_id = ?", f.callID).Count(&attempts).Error)
			assert.EqualValues(t, 1, attempts)
		})
	}
}

func TestManagedKnowledgePublicRoutesWithoutIdempotencyKey(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		name := "rerank"
		if embedding {
			name = "embedding"
		}
		t.Run(name, func(t *testing.T) {
			f := newManagedKnowledgeFixture(t, embedding)
			f.managed.acceptGeneratedID = true
			w := managedKnowledgeRequest(f, f.body, func(r *http.Request) {
				r.Header.Del("Idempotency-Key")
				require.Len(t, r.Header, 2)
			})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, f.callID, w.Header().Get("X-UniHub-Call-ID"))
			assert.Equal(t, 1, f.reserves)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
			var calls []platformbilling.Call
			require.NoError(t, f.db.Find(&calls).Error)
			require.Len(t, calls, 1)
			assert.Equal(t, f.callID, calls[0].CallID)
			assert.Equal(t, f.taskID, calls[0].TaskID)
			assert.Equal(t, "parent-key", calls[0].CredentialID)
			assert.Equal(t, "SETTLED", calls[0].Status)
			var attempts int64
			require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
			assert.EqualValues(t, 1, attempts)
		})
	}
}

func TestManagedKnowledgeDuplicateCannotSendAgain(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "same-request"
		if changed {
			name = "changed-request"
		}
		t.Run(name, func(t *testing.T) {
			f := newManagedKnowledgeFixture(t, true)
			first := managedKnowledgeRequest(f, f.body, nil)
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			body := f.body
			if changed {
				body = `{"model":"unihub-text-embedding-v4","input":["different"]}`
			}
			duplicate := managedKnowledgeRequest(f, body, nil)
			assert.Equal(t, http.StatusConflict, duplicate.Code, duplicate.Body.String())
			assert.Equal(t, 1, f.reserves)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
			var attempts int64
			require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Where("call_id = ?", f.callID).Count(&attempts).Error)
			assert.EqualValues(t, 1, attempts)
		})
	}
}

func TestManagedKnowledgePinRotatedAfterReserveNeverReachesProvider(t *testing.T) {
	f := newManagedKnowledgeFixture(t, true)
	f.beforeReserve = func() {
		_, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"superwagie"})
		require.NoError(t, err)
	}
	w := managedKnowledgeRequest(f, f.body, nil)
	assert.GreaterOrEqual(t, w.Code, 400, w.Body.String())
	assert.Equal(t, 1, f.reserves)
	assert.Zero(t, f.sends)
	assert.Zero(t, f.settles)
	var attempts int64
	require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
	assert.Zero(t, attempts)
}

func TestManagedKnowledgeRevokedBeforeSendNeverReachesProvider(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		name := "rerank"
		if embedding {
			name = "embedding"
		}
		t.Run(name, func(t *testing.T) {
			f := newManagedKnowledgeFixture(t, embedding)
			f.beforeReserve = func() { f.revoked = true }
			w := managedKnowledgeRequest(f, f.body, nil)
			assert.GreaterOrEqual(t, w.Code, 400, w.Body.String())
			assert.Equal(t, 2, f.claimChecks)
			assert.Equal(t, 1, f.reserves)
			assert.Zero(t, f.sends)
			assert.Zero(t, f.settles)
			var attempts int64
			require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
			assert.Zero(t, attempts)
		})
	}
}

func TestManagedKnowledgeRejectsClientAuthorityHeaders(t *testing.T) {
	for _, headers := range []struct {
		name  string
		actor bool
		pin   bool
	}{
		{name: "actor", actor: true},
		{name: "policy", pin: true},
		{name: "actor-and-policy", actor: true, pin: true},
	} {
		t.Run(headers.name, func(t *testing.T) {
			f := newManagedKnowledgeFixture(t, true)
			w := managedKnowledgeRequest(f, f.body, func(r *http.Request) {
				if headers.actor {
					r.Header.Set("X-UniHub-Actor-Authorization", "Bearer original-actor")
				}
				if headers.pin {
					r.Header.Set("X-UniHub-Knowledge-Policy", f.binding.PolicyDigest)
				}
			})
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Zero(t, f.verifies)
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
			var calls int64
			require.NoError(t, f.db.Model(&platformbilling.Call{}).Count(&calls).Error)
			assert.Zero(t, calls)
		})
	}
}

func TestTaskKnowledgeMissingActorCannotUseManagedKeyAuthority(t *testing.T) {
	for _, removePin := range []bool{false, true} {
		name := "missing-actor"
		if removePin {
			name = "missing-actor-and-policy"
		}
		t.Run(name, func(t *testing.T) {
			f := newPairedFixture(t, true)
			w := f.request(f.path, f.body, false, func(r *http.Request) {
				r.Header.Del("X-UniHub-Actor-Authorization")
				if removePin {
					r.Header.Del("X-UniHub-Knowledge-Policy")
				}
			})
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Zero(t, f.verifies)
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
		})
	}
}

func TestManagedKnowledgeRejectsInvalidParentClaims(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*platformbilling.Claims)
	}{
		{"expired", func(c *platformbilling.Claims) { c.ExpiresAt = time.Now().Add(-time.Minute) }},
		{"task-credential", func(c *platformbilling.Claims) { c.CredentialKind, c.TaskID = "task", "task" }},
		{"other-instance", func(c *platformbilling.Claims) { c.InstanceID = "other" }},
		{"other-model", func(c *platformbilling.Claims) { c.Models = []string{"other"} }},
		{"other-wire", func(c *platformbilling.Claims) {
			c.Operations = map[string][]string{"unihub-text-embedding-v4": {"chat.completions.create"}}
		}},
		{"missing-actor", func(c *platformbilling.Claims) { c.ActorID = "" }},
		{"missing-account", func(c *platformbilling.Claims) { c.AccountID = "" }},
		{"other-audience", func(c *platformbilling.Claims) { c.Audience = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedKnowledgeFixture(t, true)
			f.managed.mutateClaims = func(_ int, c *platformbilling.Claims) { tc.mutate(c) }
			w := managedKnowledgeRequest(f, f.body, nil)
			assert.GreaterOrEqual(t, w.Code, 400, w.Body.String())
			assert.Less(t, w.Code, 500)
			assert.Equal(t, 1, f.claimChecks)
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
			var calls int64
			require.NoError(t, f.db.Model(&platformbilling.Call{}).Count(&calls).Error)
			assert.Zero(t, calls)
		})
	}
}

func TestManagedKnowledgeRejectsParentBindingChangedBeforeSend(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*platformbilling.Claims)
	}{
		{"credential", func(c *platformbilling.Claims) { c.ID = "replacement-key" }},
		{"actor", func(c *platformbilling.Claims) { c.ActorID = "replacement-actor" }},
		{"account", func(c *platformbilling.Claims) { c.AccountID = "replacement-payer" }},
		{"product", func(c *platformbilling.Claims) { c.OriginProductID = "llmrouter" }},
		{"funding", func(c *platformbilling.Claims) { c.Asset, c.FundingMode = "CNY/6", "prepaid_cash" }},
		{"billing-version", func(c *platformbilling.Claims) { c.BillingProfileVersion++ }},
		{"expired", func(c *platformbilling.Claims) { c.ExpiresAt = time.Now().Add(-time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedKnowledgeFixture(t, true)
			f.managed.mutateClaims = func(check int, c *platformbilling.Claims) {
				if check == 2 {
					tc.mutate(c)
				}
			}
			w := managedKnowledgeRequest(f, f.body, nil)
			assert.GreaterOrEqual(t, w.Code, 400, w.Body.String())
			assert.Equal(t, 2, f.claimChecks)
			assert.Equal(t, 1, f.reserves)
			assert.Zero(t, f.sends)
			assert.Zero(t, f.settles)
			var attempts int64
			require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
			assert.Zero(t, attempts)
		})
	}
}
