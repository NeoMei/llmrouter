package platformbilling

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeValidatedBindingAtomic(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/binding.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			for _, op := range []string{"embedding", "rerank"} {
				t.Run(op, func(t *testing.T) {
					// Each case gets independent tables via a child schema (or SQLite file).
					caseDB := knowledgeResultTestDB(t, dialect, map[string]string{"sqlite": "file:" + t.TempDir() + "/case.db", "postgres": dsn}[dialect])
					s, key, upstream := responseFixtureOnDB(t, op, caseDB)
					v, err := newKnowledgeResultVault(caseDB, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
					require.NoError(t, err)
					body := `{"model":"upstream-embed","data":[{"index":0,"embedding":[0,0,0]},{"index":1,"embedding":[1,1,1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`
					if op == "rerank" {
						body = `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
					}
					stage := func() sealedKnowledgeResponse {
						value, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
						require.NoError(t, err)
						return value
					}
					result := stage()
					for _, tamper := range []func(*sealedKnowledgeResponse){func(r *sealedKnowledgeResponse) { r.body = "foreign" }, func(r *sealedKnowledgeResponse) { r.usage = "{}" }, func(r *sealedKnowledgeResponse) { r.attemptID++ }, func(r *sealedKnowledgeResponse) { r.callID = "other" }} {
						fake := stage()
						tamper(&fake)
						assert.Error(t, v.captureValidated(context.Background(), s, fake))
					}
					other := *s
					assert.Error(t, v.captureValidated(context.Background(), &other, result))
					forged := result
					forged.owner = &other
					assert.Error(t, v.captureValidated(context.Background(), &other, forged))
					require.NoError(t, caseDB.Model(&Attempt{}).Where("id = ?", s.attempt.ID).Update("channel_id", 2).Error)
					assert.Error(t, v.captureValidated(context.Background(), s, result))
					require.NoError(t, caseDB.Model(&Attempt{}).Where("id = ?", s.attempt.ID).Update("channel_id", 1).Error)
					var wg sync.WaitGroup
					outcomes := make(chan error, 2)
					for i := 0; i < 2; i++ {
						wg.Add(1)
						go func() { defer wg.Done(); outcomes <- v.captureValidated(context.Background(), s, result) }()
					}
					wg.Wait()
					close(outcomes)
					successes := 0
					for err := range outcomes {
						if err == nil {
							successes++
						}
					}
					assert.Equal(t, 1, successes)
					var row Call
					require.NoError(t, caseDB.First(&row, "call_id = ?", s.call.CallID).Error)
					assert.Equal(t, "SETTLE_PENDING", row.Status)
					assert.Contains(t, row.Evidence, `"state":"confirmed"`)
					var stored knowledgeResultRow
					require.NoError(t, caseDB.First(&stored, "call_id = ?", s.call.CallID).Error)
					assert.NotEmpty(t, stored.Ciphertext)
					assert.Equal(t, 1, stored.BindingVersion)
					assert.Equal(t, s.attempt.ID, stored.AttemptID)
					assert.Len(t, stored.ObservationTag, 64)
					require.NoError(t, caseDB.Model(&Call{}).Where("call_id = ?", row.CallID).Update("status", "SETTLED").Error)
					got, err := v.readSettled(context.Background(), row.CallID)
					require.NoError(t, err)
					assert.Equal(t, result.body, string(got))
					require.NoError(t, caseDB.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("observation_tag", strings.Repeat("a", 64)).Error)
					_, err = v.readSettled(context.Background(), row.CallID)
					assert.Error(t, err)
					require.NoError(t, caseDB.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("observation_tag", stored.ObservationTag).Error)
					v.now = func() time.Time { return time.UnixMilli(stored.ExpiresAt).Add(time.Second) }
					purged, _, err := v.purgeExpired(context.Background(), 1, "")
					require.NoError(t, err)
					assert.EqualValues(t, 1, purged)
					require.NoError(t, caseDB.First(&stored, "call_id = ?", row.CallID).Error)
					assert.Empty(t, stored.Ciphertext)
					assert.Len(t, stored.ObservationTag, 64)
				})
			}
		})
	}
}

func TestKnowledgeValidatedConflictPreservesUsage(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/conflict.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			s, key, upstream := responseFixtureOnDB(t, "rerank", knowledgeResultTestDB(t, dialect, dsn))
			v, err := newKnowledgeResultVault(s.adapter.db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
			require.NoError(t, err)
			body := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
			result, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
			require.NoError(t, err)
			var other metering.Usage
			require.NoError(t, common.Unmarshal([]byte(result.usage), &other))
			other.Measurements[0].Quantity.Value = "4"
			require.NoError(t, s.ObserveMetering(context.Background(), other))
			assert.ErrorIs(t, v.captureValidated(context.Background(), s, result), ErrConflict)
			var row Call
			require.NoError(t, s.adapter.db.First(&row, "call_id = ?", s.call.CallID).Error)
			assert.True(t, row.EvidenceConflict)
			assert.Equal(t, "UNKNOWN", row.Status)
			assert.Contains(t, row.Evidence, `"value":"4"`)
			var count int64
			require.NoError(t, s.adapter.db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestKnowledgeValidatedConflictAfterFinish(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "settled"}[terminal], func(t *testing.T) {
			s, key, upstream := responseFixture(t, "rerank")
			v, err := newKnowledgeResultVault(s.adapter.db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
			require.NoError(t, err)
			body := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
			result, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
			require.NoError(t, err)
			var other metering.Usage
			require.NoError(t, common.Unmarshal([]byte(result.usage), &other))
			other.Measurements[0].Quantity.Value = "4"
			require.NoError(t, s.ObserveMetering(context.Background(), other))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
			defer server.Close()
			s.adapter.client = &client{origin: server.URL, token: "fixture", http: server.Client()}
			require.NoError(t, s.Finish(context.Background()))
			if terminal {
				require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update("status", "SETTLED").Error)
			}
			assert.ErrorIs(t, v.captureValidated(context.Background(), s, result), ErrConflict)
			var row Call
			require.NoError(t, s.adapter.db.First(&row, "call_id = ?", s.call.CallID).Error)
			assert.True(t, row.EvidenceConflict)
			assert.Equal(t, map[bool]string{false: "SETTLE_PENDING", true: "SETTLED"}[terminal], row.Status)
			var count int64
			require.NoError(t, s.adapter.db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}
