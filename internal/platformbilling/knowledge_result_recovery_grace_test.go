package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const recoveryGraceEnv = "UNIHUB_KNOWLEDGE_SETTLED_RESULT_RECOVERY_GRACE_SECONDS"

type recoveryGraceFixture struct {
	a        *Adapter
	row      Call
	stored   knowledgeResultRow
	body     string
	clock    time.Time
	grant    knowledgeReadGrant
	remote   knowledgeSettledReceipt
	requests []string
	revoked  bool
}

// Capture a real validated v2 result with the production constructor and env
// parser. The only HTTP double is the separate central authority; no provider
// endpoint exists, so a reserve, settle or dispatch is an unexpected request.
func newRecoveryGraceFixture(t *testing.T, grace string) *recoveryGraceFixture {
	t.Helper()
	s, key, upstream := responseFixture(t, "rerank")
	var frozen frozenKnowledge
	require.NoError(t, common.Unmarshal([]byte(s.call.MediaSnapshot), &frozen))
	frozen.Policy.PublicModel = "qwen3-rerank"
	s.call.Model = "qwen3-rerank"
	s.call.CredentialID = "original-credential"
	s.call.CredentialKind = "task"
	s.call.Amount = "10"
	funding := RemoteCall{Allocations: []Allocation{{BatchID: "batch", Amount: "10"}}}
	s.call.FundingSnapshot = funding.fundingSnapshot()
	var rate Rate
	require.NoError(t, common.Unmarshal([]byte(s.call.RateSnapshot), &rate))
	rate.Model = s.call.Model
	encodedRate, err := common.Marshal(rate)
	require.NoError(t, err)
	s.call.RateSnapshot = string(encodedRate)
	f := &recoveryGraceFixture{clock: time.Now().UTC().Truncate(time.Millisecond)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		var reply any
		switch r.Method + " " + r.URL.Path {
		case "POST /internal/v1/model-credentials/verify":
			if f.revoked {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			var request map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &request))
			if r.Header.Get("X-UniHub-Actor-Authorization") != "Bearer actor-token" || request["purpose"] != "result_read" || request["task_id"] != "" || request["model"] != "qwen3-rerank" || request["wire_operation"] != "rerank.create" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			reply = f.grant
		case "GET /internal/v1/calls/call":
			reply = f.remote
		default:
			t.Errorf("unexpected network operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, e := common.Marshal(reply)
		require.NoError(t, e)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	env := knowledgeRuntimeEnvironment(t, server.URL)
	env[recoveryGraceEnv] = grace
	env["UNIHUB_ALI_EMBEDDING_ENABLED"] = "false"
	env["UNIHUB_MAX_INPUT_TOKENS"] = "100"
	env["UNIHUB_MAX_BODY_BYTES"] = "65536"
	env["UNIHUB_KNOWLEDGE_RESULT_KEYS"] = `{"v1":"` + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")) + `"}`
	env["UNIHUB_KNOWLEDGE_RESULT_ACTIVE_KEY"] = "v1"
	policies, err := common.Marshal(map[string]map[string]KnowledgePolicy{"rerank.create": {"qwen3-rerank": frozen.Policy}})
	require.NoError(t, err)
	env["UNIHUB_KNOWLEDGE_POLICIES"] = string(policies)
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	require.NoError(t, err)
	f.a, err = New(s.adapter.db, cfg)
	require.NoError(t, err)
	binding := ManagedBinding{RouteID: 1, TargetID: 2, ChannelID: s.attempt.ChannelID, InstanceID: "default", OriginProductID: "superwagie", Model: s.call.Model, WireOperation: s.call.WireOperation, RouteGeneration: strings.Repeat("b", 64)}
	f.a.SetKnowledgeBindingResolver(func(_ *gorm.DB, product, model, wire string) (ManagedBinding, string, error) {
		if product != binding.OriginProductID || model != binding.Model || wire != binding.WireOperation {
			return ManagedBinding{}, "", ErrInvalid
		}
		return binding, strings.Repeat("f", 64), nil
	}, func(*gorm.DB, string) error { return nil })
	pins, err := f.a.ActivateManagedKnowledge(context.Background(), binding.Model, binding.WireOperation, []string{"superwagie"})
	require.NoError(t, err)
	frozen.Binding = &pins[0]
	snapshot, err := common.Marshal(frozen)
	require.NoError(t, err)
	s.call.MediaSnapshot = string(snapshot)
	require.NoError(t, f.a.sealKnowledgeSnapshot(&s.call))
	require.NoError(t, s.adapter.db.Save(&s.call).Error)
	s.adapter = f.a
	v := f.a.knowledgeResults
	v.now = func() time.Time { return f.clock.Add(-2 * time.Hour) }
	raw := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
	staged, err := v.validateForCapture(context.Background(), s, s.attempt.ChannelID, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))})
	require.NoError(t, err)
	require.NoError(t, v.captureValidated(context.Background(), s, staged))
	require.NoError(t, f.a.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Updates(map[string]any{"status": "SETTLED", "charged": "3"}).Error)
	require.NoError(t, f.a.db.First(&f.row, "call_id = ?", s.call.CallID).Error)
	require.NoError(t, f.a.db.First(&f.stored, "call_id = ?", s.call.CallID).Error)
	f.body = staged.body
	var intent struct {
		Usage        Usage  `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}
	require.NoError(t, common.Unmarshal([]byte(f.row.Intent), &intent))
	digestInput, err := common.Marshal(struct {
		CallID       string `json:"call_id"`
		Usage        *Usage `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}{f.row.CallID, &intent.Usage, intent.ProviderCost})
	require.NoError(t, err)
	digest := sha256.Sum256(digestInput)
	f.remote = knowledgeSettledReceipt{RemoteCall: RemoteCall{CallID: f.row.CallID, ActorID: f.row.ActorID, AccountID: f.row.AccountID, OriginProductID: f.row.OriginProductID, CredentialID: f.row.CredentialID, RequestDigest: f.row.Digest, WireOperation: f.row.WireOperation, Rate: rate, Held: f.row.Amount, Allocations: funding.Allocations, ChargeAsset: f.row.Asset, FundingMode: f.row.FundingMode, FundingProductID: "llmrouter", Status: "SETTLED", Charged: "3"}, TaskID: f.row.TaskID, ServiceID: "llmrouter", Usage: &intent.Usage, FinalDigest: hex.EncodeToString(digest[:])}
	f.grant = knowledgeReadGrant{Claims: Claims{ID: "fresh-reader", Audience: "llmrouter", ActorID: f.row.ActorID, AccountID: f.row.AccountID, OriginProductID: f.row.OriginProductID, TaskID: f.row.TaskID, Models: []string{f.row.Model}, Operations: map[string][]string{f.row.Model: {f.row.WireOperation}}, ExpiresAt: f.clock.Add(25 * time.Hour)}, ActorExpiresAt: f.clock.Add(25 * time.Hour).Unix()}
	v.now = func() time.Time { return f.clock }
	return f
}

func TestKnowledgeSettledRecoveryGraceRequiresExplicitBoundedConfiguration(t *testing.T) {
	for _, raw := range []string{"-1", "82801", "1.5", "NaN", " 1", "+1", "1e3", "9223372036854775807"} {
		t.Run(raw, func(t *testing.T) {
			env := knowledgeRuntimeEnvironment(t, "https://unihub.example")
			env[recoveryGraceEnv] = raw
			_, err := ConfigFromEnv(func(k string) string { return env[k] })
			assert.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestKnowledgeSettledRecoveryGraceReadKeepsOriginalRowsAndFreshProof(t *testing.T) {
	f := newRecoveryGraceFixture(t, "82800")
	result, err := f.a.KnowledgeResultWithEvidence(context.Background(), "actor-token", "uh_model_fresh", f.row.TaskID, f.row.CallID)
	require.NoError(t, err)
	assert.Equal(t, f.body, string(result.RawBytes))
	assert.Equal(t, f.clock.Unix(), result.Receipt.IssuedAt)
	assert.Equal(t, f.clock.Unix()+30, result.Receipt.ExpiresAt)
	assert.Equal(t, "original-credential", result.Receipt.ProducerCredentialID)
	assert.Equal(t, "fresh-reader", result.Receipt.AuthorizedCredentialID)
	var after knowledgeResultRow
	require.NoError(t, f.a.db.First(&after, "call_id = ?", f.row.CallID).Error)
	assert.Equal(t, f.stored, after)
	var callAfter Call
	require.NoError(t, f.a.db.First(&callAfter, "call_id = ?", f.row.CallID).Error)
	assert.Equal(t, f.row, callAfter)
	assert.Equal(t, []string{"POST /internal/v1/model-credentials/verify", "GET /internal/v1/calls/call", "POST /internal/v1/model-credentials/verify"}, f.requests)
	_, err = f.a.knowledgeResults.readSettled(context.Background(), f.row.CallID)
	assert.Error(t, err, "generic capture/legacy read retains original expiry")
	f.clock = time.UnixMilli(f.stored.ExpiresAt).Add(23*time.Hour - 5*time.Second)
	result, err = f.a.KnowledgeResultWithEvidence(context.Background(), "actor-token", "uh_model_fresh", f.row.TaskID, f.row.CallID)
	require.NoError(t, err)
	assert.Equal(t, f.clock.Unix()+5, result.Receipt.ExpiresAt, "fresh proof is clipped to effective read deadline")
	f.clock = f.clock.Add(5 * time.Second)
	_, err = f.a.KnowledgeResultWithEvidence(context.Background(), "actor-token", "uh_model_fresh", f.row.TaskID, f.row.CallID)
	assert.Error(t, err, "deadline itself is expired")
}

func TestKnowledgeSettledRecoveryGraceNeverBypassesAuthorityOrBindings(t *testing.T) {
	for _, scenario := range []string{"default-zero", "actor-expired", "grant-expired", "wrong-actor", "wrong-payer", "remote-pending", "bad-charge", "bad-digest", "v1", "cleared", "changed-policy", "changed-route", "unknown", "missing-usage", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			grace := "82800"
			if scenario == "default-zero" {
				grace = ""
			}
			f := newRecoveryGraceFixture(t, grace)
			switch scenario {
			case "revoked":
				f.revoked = true
			case "actor-expired":
				f.grant.ActorExpiresAt = f.clock.Unix()
			case "grant-expired":
				f.grant.ExpiresAt = time.Now().Add(-time.Second)
			case "wrong-actor":
				f.grant.ActorID = "other"
			case "wrong-payer":
				f.grant.AccountID = "other"
			case "remote-pending":
				f.remote.Status = "RESERVED"
			case "bad-charge":
				f.remote.Charged = "0"
			case "bad-digest":
				f.remote.FinalDigest = strings.Repeat("0", 64)
			case "v1":
				require.NoError(t, f.a.db.Model(&knowledgeResultRow{}).Where("call_id = ?", f.row.CallID).Update("binding_version", 1).Error)
			case "cleared":
				require.NoError(t, f.a.db.Model(&knowledgeResultRow{}).Where("call_id = ?", f.row.CallID).Update("ciphertext", nil).Error)
			case "changed-policy":
				require.NoError(t, f.a.db.Table("knowledge_active_bindings").Where("origin_product_id = ?", "superwagie").Update("active_pin", strings.Repeat("c", 64)).Error)
			case "changed-route":
				f.a.bindingResolver = func(*gorm.DB, string, string, string) (ManagedBinding, string, error) {
					return ManagedBinding{}, "", ErrConflict
				}
			case "unknown":
				require.NoError(t, f.a.db.Model(&Call{}).Where("call_id = ?", f.row.CallID).Update("status", "UNKNOWN").Error)
			case "missing-usage":
				f.remote.Usage = nil
			}
			_, err := f.a.KnowledgeResultWithEvidence(context.Background(), "actor-token", "uh_model_fresh", f.row.TaskID, f.row.CallID)
			assert.Error(t, err)
		})
	}
}

func TestKnowledgeSettledRecoveryGracePurgeRetainsOnlyValidPaidV2UntilDeadline(t *testing.T) {
	for _, scenario := range []string{"within-grace", "at-deadline", "default-zero", "unknown", "conflict", "bad-charge", "v1", "bad-attempt", "unbound"} {
		t.Run(scenario, func(t *testing.T) {
			grace := "82800"
			if scenario == "default-zero" {
				grace = "0"
			}
			f := newRecoveryGraceFixture(t, grace)
			switch scenario {
			case "at-deadline":
				f.clock = time.UnixMilli(f.stored.ExpiresAt).Add(23 * time.Hour)
			case "unknown":
				require.NoError(t, f.a.db.Model(&Call{}).Where("call_id = ?", f.row.CallID).Update("status", "UNKNOWN").Error)
			case "conflict":
				require.NoError(t, f.a.db.Model(&Call{}).Where("call_id = ?", f.row.CallID).Update("evidence_conflict", true).Error)
			case "bad-charge":
				require.NoError(t, f.a.db.Model(&Call{}).Where("call_id = ?", f.row.CallID).Update("charged", "0").Error)
			case "bad-attempt":
				require.NoError(t, f.a.db.Model(&Attempt{}).Where("id = ?", f.stored.AttemptID).Update("status", "PREPARED").Error)
			case "unbound":
				var snapshot frozenKnowledge
				require.NoError(t, common.Unmarshal([]byte(f.row.MediaSnapshot), &snapshot))
				snapshot.Binding = nil
				raw, e := common.Marshal(snapshot)
				require.NoError(t, e)
				f.row.MediaSnapshot = string(raw)
				require.NoError(t, f.a.db.Model(&Call{}).Where("call_id = ?", f.row.CallID).Update("media_snapshot", f.row.MediaSnapshot).Error)
				aad, e := knowledgeResultStoredAAD(f.row, f.stored)
				require.NoError(t, e)
				nonce := make([]byte, f.a.knowledgeResults.aead.NonceSize())
				encoded := f.a.knowledgeResults.aead.Seal(nonce, nonce, []byte(f.body), aad)
				require.NoError(t, f.a.db.Model(&knowledgeResultRow{}).Where("call_id = ?", f.row.CallID).Update("ciphertext", encoded).Error)
			case "v1":
				legacy := f.stored
				legacy.BindingVersion = 1
				aad, err := knowledgeResultStoredAAD(f.row, legacy)
				require.NoError(t, err)
				nonce := make([]byte, f.a.knowledgeResults.aead.NonceSize())
				legacy.Ciphertext = f.a.knowledgeResults.aead.Seal(nonce, nonce, []byte(f.body), aad)
				require.NoError(t, f.a.db.Model(&knowledgeResultRow{}).Where("call_id = ?", f.row.CallID).Updates(map[string]any{"binding_version": 1, "ciphertext": legacy.Ciphertext}).Error)
			}
			count, cursor, err := f.a.knowledgeResults.purgeExpired(context.Background(), 1, "")
			require.NoError(t, err)
			assert.Equal(t, f.row.CallID, cursor)
			var after knowledgeResultRow
			require.NoError(t, f.a.db.First(&after, "call_id = ?", f.row.CallID).Error)
			assert.Equal(t, f.stored.ExpiresAt, after.ExpiresAt)
			if scenario == "within-grace" {
				assert.Zero(t, count)
				assert.Equal(t, f.stored, after)
			} else {
				assert.EqualValues(t, 1, count)
				assert.Empty(t, after.Ciphertext)
			}
			assert.Empty(t, f.requests, "purge cannot invoke authority or provider")
		})
	}
}

func TestKnowledgeSettledRecoveryGraceClipsProofToCurrentAuthority(t *testing.T) {
	for _, bound := range []string{"actor", "credential"} {
		t.Run(bound, func(t *testing.T) {
			f := newRecoveryGraceFixture(t, "1")
			// Capture's original one-hour expiry is past; this selected one-second
			// grace is also expired and cannot be revived by a fresh credential.
			_, err := f.a.KnowledgeResultWithEvidence(context.Background(), "actor-token", "uh_model_fresh", f.row.TaskID, f.row.CallID)
			assert.Error(t, err)
			f = newRecoveryGraceFixture(t, "82800")
			if bound == "actor" {
				f.grant.ActorExpiresAt = f.clock.Unix() + 7
			} else {
				f.grant.ExpiresAt = time.Unix(f.clock.Unix()+7, 0)
			}
			got, err := f.a.KnowledgeResultWithEvidence(context.Background(), "actor-token", "uh_model_fresh", f.row.TaskID, f.row.CallID)
			require.NoError(t, err)
			assert.Equal(t, f.clock.Unix()+7, got.Receipt.ExpiresAt)
		})
	}
}

func TestKnowledgeSettledRecoveryGraceRejectsInvalidInProcessDuration(t *testing.T) {
	for _, value := range []time.Duration{-time.Second, 23*time.Hour + time.Second, time.Nanosecond} {
		_, err := New(nil, Config{KnowledgeSettledResultRecoveryGrace: value})
		assert.ErrorIs(t, err, ErrInvalid)
	}
}

// A read that entered before original expiry must not silently cross into the
// grace window without its extra active-binding checks.
func TestKnowledgeSettledRecoveryGraceDoesNotBypassChecksAtExpiryRace(t *testing.T) {
	f := newRecoveryGraceFixture(t, "82800")
	f.a.bindingResolver = func(*gorm.DB, string, string, string) (ManagedBinding, string, error) {
		return ManagedBinding{}, "", ErrConflict
	}
	reads := 0
	f.a.knowledgeResults.now = func() time.Time {
		reads++
		if reads <= 5 {
			return time.UnixMilli(f.stored.ExpiresAt - 1)
		}
		return time.UnixMilli(f.stored.ExpiresAt + 1)
	}
	_, err := f.a.KnowledgeResult(context.Background(), "actor-token", "uh_model_fresh", f.row.TaskID, f.row.CallID)
	assert.Error(t, err)
}
