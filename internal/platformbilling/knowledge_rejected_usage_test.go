package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The normalized record deliberately has no metering.Usage envelope or output.
type rejectedUsageRecord struct {
	CallID            string
	AttemptID         uint
	BindingDigest     string
	ObservationDigest string
	Measurements      string
	Source            string
	Reason            string
	Disposition       string
	CreatedAt         time.Time
}

func rejectedKnowledgeFixture(t *testing.T, op string) (*Session, string, string, string) {
	t.Helper()
	return rejectedKnowledgeFixtureOnDB(t, op, knowledgeResultTestDB(t, "sqlite", "file:"+t.TempDir()+"/rejected.db"))
}

func rejectedKnowledgeFixtureOnDB(t *testing.T, op string, db *gorm.DB) (*Session, string, string, string) {
	t.Helper()
	// Callback removal is expected fixture cleanup. Keep actual SQL errors
	// visible while suppressing that warning and normal missing-row probes.
	db.Logger = logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{LogLevel: logger.Error, IgnoreRecordNotFoundError: true})
	s, key, upstream := responseFixtureOnDB(t, op, db)
	s.uncertain = true
	s.adapter.config.AllowAliEmbeddingV4, s.adapter.config.AllowAliRerank = true, true
	if op == "embedding" {
		var f frozenKnowledge
		require.NoError(t, common.UnmarshalJsonStr(s.call.MediaSnapshot, &f))
		f.Policy.Version, f.Policy.Origin = "knowledge_ali_text_v1", "https://dashscope.aliyuncs.com"
		f.Policy.ChannelType, f.Policy.UpstreamModel = constant.ChannelTypeAli, "text-embedding-v4"
		f.Policy.Space = &dto.EmbeddingSpace{Provider: "aliyun", Model: "text-embedding-v4", Version: "v4", Dimensions: 64, InputSemantics: "text"}
		upstream = f.Policy.UpstreamModel
		b, err := common.Marshal(f)
		require.NoError(t, err)
		s.call.MediaSnapshot = string(b)
	}
	v, err := newKnowledgeResultVault(s.adapter.db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	require.NoError(t, err)
	s.adapter.knowledgeResults = v
	require.NoError(t, s.adapter.sealKnowledgeSnapshot(&s.call))
	s.call.Amount = "100"
	require.NoError(t, s.adapter.db.Save(&s.call).Error)
	// A transport trap covers both provider and central financial operations.
	s.adapter.client = &client{origin: "http://127.0.0.1:1", token: "fixture", http: &http.Client{Transport: rejectedUsageTransport{t: t}}}
	body := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":101}}`
	if op == "embedding" {
		vector := "[" + strings.Repeat("0,", 63) + "0]"
		body = `{"model":"text-embedding-v4","data":[{"index":0,"embedding":` + vector + `},{"index":1,"embedding":` + vector + `}],"usage":{"prompt_tokens":101,"total_tokens":101}}`
	}
	return s, key, upstream, body
}

type rejectedUsageTransport struct {
	t        *testing.T
	requests *atomic.Int64
}

func (r rejectedUsageTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if r.requests != nil {
		r.requests.Add(1)
	}
	r.t.Error("rejected usage must not invoke a provider or central financial operation")
	return nil, ErrInvalid
}

func assertRejectedKnowledgeUncharged(t *testing.T, s *Session) {
	t.Helper()
	var row Call
	require.NoError(t, s.adapter.db.First(&row, "call_id = ?", s.call.CallID).Error)
	assert.Equal(t, "UNKNOWN", row.Status)
	assert.Equal(t, "100", row.Amount)
	assert.Empty(t, row.Evidence)
	assert.Empty(t, row.NativeObservation)
	assert.Empty(t, row.Intent)
	assert.Empty(t, row.Charged)
	assert.False(t, row.EvidenceConflict)
	var attempts []Attempt
	require.NoError(t, s.adapter.db.Where("call_id = ?", row.CallID).Find(&attempts).Error)
	require.Len(t, attempts, 1)
	assert.Equal(t, "DISPATCHED", attempts[0].Status)
	var n int64
	require.NoError(t, s.adapter.db.Model(&knowledgeResultRow{}).Count(&n).Error)
	assert.Zero(t, n)
	assert.False(t, s.CanRetry())
}

func TestKnowledgeRejectedUsageDurableNonchargeable(t *testing.T) {
	for _, op := range []string{"embedding", "rerank"} {
		t.Run(op, func(t *testing.T) {
			s, key, upstream, body := rejectedKnowledgeFixture(t, op)
			// Actual dispatched session state: uncertainty prevents another send.
			s.uncertain = true
			control := strings.ReplaceAll(body, "101", "3")
			_, err := s.adapter.knowledgeResults.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(control))})
			require.NoError(t, err, "same binding and complete provider shape are valid in-bound")
			raw, err := s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(body))
			require.ErrorIs(t, err, ErrInvalid)
			stage := "knowledge_result_usage_reservation_bound"
			if op == "rerank" {
				stage = "knowledge_result_response_shape"
			}
			assert.Equal(t, stage, err.Error())
			assert.Empty(t, raw)
			assertRejectedKnowledgeUncharged(t, s)
			require.True(t, s.adapter.db.Migrator().HasTable("platform_knowledge_rejected_usage"), "valid rejected quantities need a separate durable nonchargeable record")
			var stored rejectedUsageRecord
			require.NoError(t, s.adapter.db.Table("platform_knowledge_rejected_usage").First(&stored, "call_id = ?", s.call.CallID).Error)
			assert.Equal(t, s.attempt.ID, stored.AttemptID)
			assert.Equal(t, "NONCHARGEABLE", stored.Disposition)
			assert.Equal(t, "reservation_quantity_exceeded", stored.Reason)
			assert.Contains(t, stored.Measurements, `"value":"101"`)
			assert.Len(t, stored.BindingDigest, 64)
			assert.Len(t, stored.ObservationDigest, 64)
			assert.NotContains(t, stored.Measurements, "second")
			assert.NotContains(t, stored.Measurements, key)
			// Identical observation is idempotent; changed observations never replace it.
			for _, incoming := range []string{body, strings.ReplaceAll(body, "101", "102"), control, body} {
				raw, err = s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(incoming))
				require.Error(t, err)
				if incoming == body {
					assert.ErrorIs(t, err, ErrInvalid)
				} else {
					assert.ErrorIs(t, err, ErrConflict)
				}
				assert.Empty(t, raw)
				var current rejectedUsageRecord
				require.NoError(t, s.adapter.db.Table("platform_knowledge_rejected_usage").First(&current, "call_id = ?", s.call.CallID).Error)
				assert.Equal(t, stored, current)
				assertRejectedKnowledgeUncharged(t, s)
			}
			pool, err := s.adapter.db.DB()
			require.NoError(t, err)
			dsn := s.adapter.db.Dialector.(*sqlite.Dialector).DSN
			require.NoError(t, pool.Close())
			reopened, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
			require.NoError(t, err)
			reopenedPool, err := reopened.DB()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reopenedPool.Close()) })
			s.adapter.db = reopened
			s.adapter.knowledgeResults, err = newKnowledgeResultVault(reopened, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
			require.NoError(t, err)
			var retained rejectedUsageRecord
			require.NoError(t, reopened.Table("platform_knowledge_rejected_usage").First(&retained, "call_id = ?", s.call.CallID).Error)
			assert.Equal(t, stored, retained)
			require.NoError(t, s.adapter.Recover(context.Background()))
			require.NoError(t, s.Finish(context.Background()))
			assertRejectedKnowledgeUncharged(t, s)
		})
	}
}

func TestKnowledgeRejectedUsageBlocksUnresolvedWaiver(t *testing.T) {
	db, row, attempt, approval := waiverFixture(t)
	_, err := newKnowledgeResultVault(db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	require.NoError(t, err)
	require.NoError(t, db.Create(&knowledgeRejectedUsage{CallID: row.CallID, AttemptID: attempt.ID, Disposition: "NONCHARGEABLE", Reason: "reservation_quantity_exceeded", Measurements: `[{"key":"input","unit":"token","quantity":{"value":"101"}}]`}).Error)
	raw, digest := waiverApprovalBytes(t, approval)
	for _, apply := range []bool{false, true} {
		_, err = PreparePlatformFaultWaiver(context.Background(), db, raw, digest, apply)
		assert.ErrorIs(t, err, ErrConflict, "recorded provider usage cannot be waived as unresolved absent evidence")
	}
	var saved Call
	require.NoError(t, db.First(&saved, "call_id = ?", row.CallID).Error)
	assert.Equal(t, row, saved)
	var n int64
	require.NoError(t, db.Model(&PlatformFaultWaiverDecision{}).Count(&n).Error)
	assert.Zero(t, n)
}

func TestKnowledgeRejectedUsageRequiresCompleteResponse(t *testing.T) {
	for _, op := range []string{"embedding", "rerank"} {
		t.Run(op, func(t *testing.T) {
			s, key, upstream, body := rejectedKnowledgeFixture(t, op)
			cases := map[string]string{
				"duplicate usage":    strings.Replace(body, `"usage":`, `"usage":{},"usage":`, 1),
				"missing usage":      strings.Replace(body, `"usage":`, `"other":`, 1),
				"fractional counter": strings.ReplaceAll(body, "101", "101.1"),
				"unbounded counter":  strings.ReplaceAll(body, "101", strings.Repeat("9", 100)),
				"missing output":     `{"usage":{"prompt_tokens":101,"total_tokens":101}}`,
			}
			if op == "embedding" {
				cases["wrong model"] = strings.Replace(body, "text-embedding-v4", "other", 1)
				cases["inconsistent counters"] = strings.Replace(body, `"total_tokens":101`, `"total_tokens":102`, 1)
				cases["bad vector"] = strings.Replace(body, "[0,0,", "[null,0,", 1)
				cases["output tokens"] = strings.Replace(body, `"usage":{`, `"usage":{"completion_tokens":1,`, 1)
			} else {
				cases["wrong document"] = strings.Replace(body, "second", "foreign", 1)
				cases["bad score"] = strings.Replace(body, "0.9", "null", 1)
				cases["wrong index"] = strings.Replace(body, `"index":1`, `"index":9`, 1)
			}
			for name, incoming := range cases {
				t.Run(name, func(t *testing.T) {
					raw, err := s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(incoming))
					require.Error(t, err)
					assert.Empty(t, raw)
				})
			}
			for name, binding := range map[string]struct {
				channel       int
				upstream, key string
			}{"channel": {2, upstream, key}, "model": {1, "other", key}, "key": {1, upstream, "wrong"}} {
				t.Run(name, func(t *testing.T) {
					raw, err := s.CaptureKnowledgeResult(context.Background(), binding.channel, binding.upstream, binding.key, []byte(body))
					require.Error(t, err)
					assert.Empty(t, raw)
				})
			}
			var n int64
			require.NoError(t, s.adapter.db.Model(&knowledgeRejectedUsage{}).Count(&n).Error)
			assert.Zero(t, n)
		})
	}
}

func TestKnowledgeRejectedUsageRechecksImmutableBindingAndState(t *testing.T) {
	// A staged response is paused at the validation/persistence boundary while
	// another writer finalizes or changes its bound call/attempt. No sleeps.
	changes := map[string]struct {
		attempt bool
		column  string
		value   any
	}{
		"call id": {false, "call_id", "other"}, "actor": {false, "actor_id", "other"}, "payer": {false, "account_id", "other"},
		"origin": {false, "origin_product_id", "other"}, "instance": {false, "instance_id", "other"}, "task": {false, "task_id", "other"},
		"credential": {false, "credential_id", "other"}, "credential kind": {false, "credential_kind", "api_key"},
		"model": {false, "model", "other"}, "request digest": {false, "digest", "other"}, "digest version": {false, "digest_version", "other"},
		"wire operation": {false, "wire_operation", "other"}, "wire path": {false, "wire_path_model", "other"}, "wire mode": {false, "wire_mode", "other"},
		"operation": {false, "operation", "other"}, "policy": {false, "media_snapshot", "{}"}, "rate snapshot": {false, "rate_snapshot", "{}"},
		"rate id": {false, "rate_id", "other"}, "asset": {false, "asset", "CNY/6"}, "funding mode": {false, "funding_mode", "prepaid_cash"},
		"funding product": {false, "funding_product_id", "other"}, "funding snapshot": {false, "funding_snapshot", "other"},
		"billing profile": {false, "billing_profile_version", 2}, "held amount": {false, "amount", "999"},
		"reservation": {false, "reservation_quantity", 999}, "output reservation": {false, "reservation_output", 1},
		"settled": {false, "status", "SETTLED"}, "waiver pending": {false, "status", "WAIVER_PENDING"},
		"intent": {false, "intent", "{}"}, "evidence": {false, "evidence", "{}"}, "native observation": {false, "native_observation", "{}"},
		"charged": {false, "charged", "1"}, "conflict": {false, "evidence_conflict", true},
		"attempt call": {true, "call_id", "other"}, "attempt channel": {true, "channel_id", 2}, "attempt upstream": {true, "upstream_identity", "other"},
		"attempt creation": {true, "created_at", time.Unix(1, 0)}, "attempt rejected": {true, "status", "REJECTED"},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, key, upstream, body := rejectedKnowledgeFixture(t, "rerank")
			v := s.adapter.knowledgeResults
			staged, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() {
				if change.attempt {
					done <- s.adapter.db.Model(&Attempt{}).Where("id = ?", s.attempt.ID).Update(change.column, change.value).Error
				} else {
					done <- s.adapter.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update(change.column, change.value).Error
				}
			}()
			require.NoError(t, <-done)
			require.Error(t, v.captureValidated(context.Background(), s, staged))
			var n int64
			require.NoError(t, s.adapter.db.Model(&knowledgeRejectedUsage{}).Count(&n).Error)
			assert.Zero(t, n)
		})
	}
}

func TestKnowledgeRejectedUsageStorageFailureIsNotDurableSuccess(t *testing.T) {
	for _, op := range []string{"embedding", "rerank"} {
		t.Run(op, func(t *testing.T) {
			s, key, upstream, body := rejectedKnowledgeFixture(t, op)
			require.NoError(t, s.adapter.db.Callback().Create().After("gorm:create").Register("fixture:reject_observation_commit", func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "platform_knowledge_rejected_usage" {
					tx.AddError(errors.New("sensitive SQL/provider failure"))
				}
			}))
			raw, err := s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(body))
			require.ErrorIs(t, err, ErrPending)
			assert.Equal(t, "knowledge_result_response_persistence", err.Error())
			assert.Empty(t, raw)
			require.NoError(t, s.adapter.db.Callback().Create().Remove("fixture:reject_observation_commit"))
			var n int64
			require.NoError(t, s.adapter.db.Model(&knowledgeRejectedUsage{}).Count(&n).Error)
			assert.Zero(t, n, "failed transaction rolls back the already executed insert")
			assertRejectedKnowledgeUncharged(t, s)
			raw, err = s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(body))
			require.ErrorIs(t, err, ErrInvalid)
			assert.Empty(t, raw)
			require.NoError(t, s.adapter.db.Model(&knowledgeRejectedUsage{}).Count(&n).Error)
			assert.EqualValues(t, 1, n)
			assertRejectedKnowledgeUncharged(t, s)
		})
	}
}

func TestKnowledgeRejectedUsageConcurrentObservations(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "distinct"}[distinct], func(t *testing.T) {
			s, key, upstream, body := rejectedKnowledgeFixture(t, "rerank")
			v := s.adapter.knowledgeResults
			first, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
			require.NoError(t, err)
			second := first
			if distinct {
				second, err = v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.ReplaceAll(body, "101", "102")))})
				require.NoError(t, err)
			}
			outcomes := make(chan error, 2)
			start := make(chan struct{})
			for _, staged := range []sealedKnowledgeResponse{first, second} {
				go func(result sealedKnowledgeResponse) {
					<-start
					outcomes <- v.captureValidated(context.Background(), s, result)
				}(staged)
			}
			close(start)
			for range 2 {
				require.Error(t, <-outcomes)
			}
			var rows []knowledgeRejectedUsage
			require.NoError(t, s.adapter.db.Find(&rows).Error)
			require.Len(t, rows, 1)
			var measurements []metering.Measurement
			require.NoError(t, common.UnmarshalUnique([]byte(rows[0].Measurements), &measurements))
			require.Len(t, measurements, 1)
			assert.Contains(t, []string{"101", "102"}, measurements[0].Quantity.Value)
			assertRejectedKnowledgeUncharged(t, s)
		})
	}
}

func TestKnowledgeRejectedUsageDatabaseContract(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "file:" + t.TempDir() + "/rejected.db"
			if dialect != "sqlite" {
				dsn = os.Getenv(map[string]string{"mysql": "KNOWLEDGE_RESULT_TEST_MYSQL_DSN", "postgres": "KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"}[dialect])
				if dsn == "" {
					t.Skip("explicit disposable " + dialect + " database not configured")
				}
			}
			for _, op := range []string{"embedding", "rerank"} {
				t.Run(op, func(t *testing.T) {
					if dialect == "sqlite" {
						dsn = "file:" + t.TempDir() + "/rejected.db"
					}
					db := knowledgeResultTestDB(t, dialect, dsn)
					s, key, upstream, body := rejectedKnowledgeFixtureOnDB(t, op, db)
					var original Call
					require.NoError(t, db.First(&original, "call_id = ?", s.call.CallID).Error)
					// Fail after INSERT on the actual engine, then retry the same observation.
					require.NoError(t, db.Callback().Create().After("gorm:create").Register("fixture:rejected_database_rollback", func(tx *gorm.DB) {
						if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "platform_knowledge_rejected_usage" {
							tx.AddError(ErrPending)
						}
					}))
					_, err := s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(body))
					require.ErrorIs(t, err, ErrPending)
					require.NoError(t, db.Callback().Create().Remove("fixture:rejected_database_rollback"))
					var n int64
					require.NoError(t, db.Model(&knowledgeRejectedUsage{}).Count(&n).Error)
					assert.Zero(t, n)
					_, err = s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(body))
					require.ErrorIs(t, err, ErrInvalid)
					var first knowledgeRejectedUsage
					require.NoError(t, db.First(&first, "call_id = ?", s.call.CallID).Error)
					assert.Equal(t, "NONCHARGEABLE", first.Disposition)
					assert.Contains(t, first.Measurements, `"value":"101"`)
					for range 2 {
						s.adapter.knowledgeResults, err = newKnowledgeResultVault(db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
						require.NoError(t, err, "repeated migration preserves existing calls and rejection rows")
					}
					for _, incoming := range []string{body, strings.ReplaceAll(body, "101", "102"), strings.ReplaceAll(body, "101", "3")} {
						_, err = s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(incoming))
						if incoming == body {
							require.ErrorIs(t, err, ErrInvalid)
						} else {
							require.ErrorIs(t, err, ErrConflict)
						}
					}
					var retained knowledgeRejectedUsage
					require.NoError(t, db.First(&retained, "call_id = ?", s.call.CallID).Error)
					assert.Equal(t, first, retained)
					var current Call
					require.NoError(t, db.First(&current, "call_id = ?", s.call.CallID).Error)
					assert.Equal(t, original, current, "migration/rejection cannot change the hold, identity, state or chargeable evidence")
					assertRejectedKnowledgeUncharged(t, s)
				})
			}
		})
	}
}

func TestKnowledgeRejectedUsagePreservesSuccessfulCapture(t *testing.T) {
	for _, op := range []string{"embedding", "rerank"} {
		t.Run(op, func(t *testing.T) {
			s, key, upstream, body := rejectedKnowledgeFixture(t, op)
			body = strings.ReplaceAll(body, "101", "3")
			funding := RemoteCall{Allocations: []Allocation{{BatchID: "fixture-batch", Amount: "100"}}}
			s.call.FundingSnapshot = funding.fundingSnapshot()
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update("funding_snapshot", s.call.FundingSnapshot).Error)
			var post, get atomic.Int64
			s.adapter.client.http = &http.Client{Transport: meteredReserveTransport(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/internal/v1/calls/call/settle":
					post.Add(1)
				case r.Method == http.MethodGet && r.URL.Path == "/internal/v1/calls/call":
					get.Add(1)
				default:
					t.Errorf("unexpected operation: %s %s", r.Method, r.URL.Path)
					return nil, ErrInvalid
				}
				var row Call
				require.NoError(t, s.adapter.db.First(&row, "call_id = ?", s.call.CallID).Error)
				var rate Rate
				require.NoError(t, common.UnmarshalUnique([]byte(row.RateSnapshot), &rate))
				var intent struct {
					Usage        Usage  `json:"usage"`
					ProviderCost string `json:"provider_cost"`
				}
				require.NoError(t, common.UnmarshalUnique([]byte(row.Intent), &intent))
				digestInput, err := common.Marshal(struct {
					CallID       string `json:"call_id"`
					Usage        *Usage `json:"usage"`
					ProviderCost string `json:"provider_cost"`
				}{row.CallID, &intent.Usage, intent.ProviderCost})
				require.NoError(t, err)
				digest := sha256.Sum256(digestInput)
				receipt := knowledgeSettledReceipt{RemoteCall: RemoteCall{CallID: row.CallID, InstanceID: row.InstanceID, ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, CredentialID: row.CredentialID, RequestDigest: row.Digest, WireOperation: row.WireOperation, Rate: rate, Held: row.Amount, Allocations: funding.Allocations, ChargeAsset: row.Asset, FundingMode: row.FundingMode, FundingProductID: row.FundingProductID, Status: "SETTLED", Charged: "3"}, TaskID: row.TaskID, ServiceID: "llmrouter", Usage: &intent.Usage, FinalDigest: hex.EncodeToString(digest[:])}
				raw, err := common.Marshal(receipt)
				require.NoError(t, err)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			raw, err := s.CaptureKnowledgeResult(context.Background(), 1, upstream, key, []byte(body))
			require.NoError(t, err)
			assert.NotEmpty(t, raw)
			assert.EqualValues(t, 1, post.Load())
			assert.EqualValues(t, 1, get.Load())
			var row Call
			require.NoError(t, s.adapter.db.First(&row, "call_id = ?", s.call.CallID).Error)
			assert.Equal(t, "SETTLED", row.Status)
			assert.Equal(t, "3", row.Charged)
			assert.Contains(t, row.Evidence, `"value":"3"`)
			var n int64
			require.NoError(t, s.adapter.db.Model(&knowledgeRejectedUsage{}).Count(&n).Error)
			assert.Zero(t, n)
		})
	}
}

// The first ordinary SELECT establishes a stale snapshot on MySQL REPEATABLE
// READ. Capture commits while waiver is paused before its first row lock.
// The exclusion must read current locked observation rows, not that snapshot.
func TestKnowledgeRejectedUsageWaiverCurrentRead(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv(map[string]string{"mysql": "KNOWLEDGE_RESULT_TEST_MYSQL_DSN", "postgres": "KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"}[dialect])
			if dsn == "" {
				t.Skip("explicit disposable " + dialect + " database not configured")
			}
			for _, op := range []string{"embedding", "rerank"} {
				t.Run(op, func(t *testing.T) {
					db := knowledgeResultTestDB(t, dialect, dsn)
					if dialect == "mysql" {
						var isolation string
						require.NoError(t, db.Raw("SELECT @@transaction_isolation").Scan(&isolation).Error)
						require.Equal(t, "REPEATABLE-READ", isolation, "exercise the supported default isolation, without overriding it")
					}
					s, key, upstream, body := rejectedKnowledgeFixtureOnDB(t, op, db)
					// Bind this real capture fixture to the existing cash-waiver contract.
					frozen, err := s.adapter.knowledgeSnapshot(s.call)
					require.NoError(t, err)
					frozen.BodyCiphertext, frozen.BodyKeyID = nil, ""
					frozen.Policy.RateIDs = map[string]string{"CNY/6": "rate"}
					snapshot, err := common.Marshal(frozen)
					require.NoError(t, err)
					s.call.MediaSnapshot = string(snapshot)
					s.call.OriginProductID, s.call.Asset, s.call.FundingMode = "llmrouter", "CNY/6", "prepaid_cash"
					var rate Rate
					require.NoError(t, common.UnmarshalUnique([]byte(s.call.RateSnapshot), &rate))
					rate.Asset = "CNY/6"
					encodedRate, err := common.Marshal(rate)
					require.NoError(t, err)
					s.call.RateSnapshot = string(encodedRate)
					require.NoError(t, s.adapter.sealKnowledgeSnapshot(&s.call))
					require.NoError(t, db.Save(&s.call).Error)
					require.NoError(t, InstallPlatformFaultWaiverSchema(context.Background(), db))
					var original Call
					var attempt Attempt
					require.NoError(t, db.First(&original, "call_id = ?", s.call.CallID).Error)
					require.NoError(t, db.First(&attempt, "id = ?", s.attempt.ID).Error)
					approval, digest := waiverApprovalBytes(t, waiverApprovalForCall(t, original, attempt))
					require.NoError(t, ValidatePlatformFaultWaiverApproval(approval, digest))

					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					snapshotRead := make(chan struct{})
					resume := make(chan struct{})
					var paused atomic.Bool
					var resumeOnce sync.Once
					release := func() { resumeOnce.Do(func() { close(resume) }) }
					defer release()
					require.NoError(t, db.Callback().Query().After("gorm:query").Register("fixture:waiver_snapshot_barrier", func(tx *gorm.DB) {
						if tx.Statement.Table == "platform_model_fault_waiver_decisions" && paused.CompareAndSwap(false, true) {
							close(snapshotRead)
							select {
							case <-resume:
							case <-ctx.Done():
								tx.AddError(ctx.Err())
							}
						}
					}))
					defer func() { require.NoError(t, db.Callback().Query().Remove("fixture:waiver_snapshot_barrier")) }()
					finished := make(chan error, 1)
					go func() {
						_, waiverErr := PreparePlatformFaultWaiver(ctx, db, approval, digest, true)
						finished <- waiverErr
					}()
					select {
					case <-snapshotRead:
					case waiverErr := <-finished:
						t.Fatalf("waiver finished before snapshot barrier: %v", waiverErr)
					case <-ctx.Done():
						t.Fatal("waiver did not reach snapshot barrier")
					}
					raw, captureErr := s.CaptureKnowledgeResult(ctx, 1, upstream, key, []byte(body))
					// Capture returned only after its rejected observation transaction committed.
					release()
					var waiverErr error
					select {
					case waiverErr = <-finished:
					case <-ctx.Done():
						t.Fatal("waiver did not finish after capture commit")
					}
					require.ErrorIs(t, captureErr, ErrInvalid)
					assert.Empty(t, raw)
					assert.ErrorIs(t, waiverErr, ErrConflict, "committed supplier overage must block a waiver that began from an older snapshot")
					var current Call
					require.NoError(t, db.First(&current, "call_id = ?", original.CallID).Error)
					assert.True(t, waiverCallEqual(original, current), "call, hold, state, financial evidence and intent remain unchanged")
					var retained knowledgeRejectedUsage
					require.NoError(t, db.First(&retained, "call_id = ?", original.CallID).Error)
					assert.Equal(t, "NONCHARGEABLE", retained.Disposition)
					assert.Contains(t, retained.Measurements, `"value":"101"`)
					var decisions int64
					require.NoError(t, db.Model(&PlatformFaultWaiverDecision{}).Count(&decisions).Error)
					assert.Zero(t, decisions)
					assertRejectedKnowledgeUncharged(t, s)
				})
			}
		})
	}
}

// checkWriter's ordinary active-generation SELECT can establish a snapshot
// before capture waits for the original Attempt/Call locks. These checks must
// observe binding/finalization changes committed after that snapshot.
func TestKnowledgeRejectedUsageCaptureCurrentRead(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv(map[string]string{"mysql": "KNOWLEDGE_RESULT_TEST_MYSQL_DSN", "postgres": "KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"}[dialect])
			if dsn == "" {
				t.Skip("explicit disposable " + dialect + " database not configured")
			}
			for _, op := range []string{"embedding", "rerank"} {
				for _, change := range []struct {
					name    string
					attempt bool
					values  map[string]any
				}{
					{"call binding", false, map[string]any{"credential_id": "other-credential"}},
					{"attempt binding", true, map[string]any{"upstream_identity": "other-upstream"}},
					{"call finalization", false, map[string]any{"status": "WAIVER_PENDING", "intent": `{"reason":"reviewed-platform-fault"}`}},
				} {
					t.Run(op+"/"+change.name, func(t *testing.T) {
						db := knowledgeResultTestDB(t, dialect, dsn)
						if dialect == "mysql" {
							var isolation string
							require.NoError(t, db.Raw("SELECT @@transaction_isolation").Scan(&isolation).Error)
							require.Equal(t, "REPEATABLE-READ", isolation)
						}
						s, key, upstream, body := rejectedKnowledgeFixtureOnDB(t, op, db)
						ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
						defer cancel()
						snapshotRead := make(chan struct{})
						resume := make(chan struct{})
						var paused atomic.Bool
						var resumeOnce sync.Once
						release := func() { resumeOnce.Do(func() { close(resume) }) }
						defer release()
						require.NoError(t, db.Callback().Query().After("gorm:query").Register("fixture:capture_snapshot_barrier", func(tx *gorm.DB) {
							if tx.Statement.Table == "platform_knowledge_result_active" && paused.CompareAndSwap(false, true) {
								close(snapshotRead)
								select {
								case <-resume:
								case <-ctx.Done():
									tx.AddError(ctx.Err())
								}
							}
						}))
						defer func() { require.NoError(t, db.Callback().Query().Remove("fixture:capture_snapshot_barrier")) }()
						finished := make(chan error, 1)
						go func() {
							_, captureErr := s.CaptureKnowledgeResult(ctx, 1, upstream, key, []byte(body))
							finished <- captureErr
						}()
						select {
						case <-snapshotRead:
						case captureErr := <-finished:
							t.Fatalf("capture finished before snapshot barrier: %v", captureErr)
						case <-ctx.Done():
							t.Fatal("capture did not reach snapshot barrier")
						}
						// This writer commits before capture acquires the Attempt/Call locks.
						var mutationErr error
						if change.attempt {
							mutationErr = db.Model(&Attempt{}).Where("id = ?", s.attempt.ID).UpdateColumns(change.values).Error
						} else {
							mutationErr = db.Model(&Call{}).Where("call_id = ?", s.call.CallID).UpdateColumns(change.values).Error
						}
						release()
						var captureErr error
						select {
						case captureErr = <-finished:
						case <-ctx.Done():
							t.Fatal("capture did not finish after binding change")
						}
						require.NoError(t, mutationErr)
						assert.ErrorIs(t, captureErr, ErrPending, "current call and original attempt must be rechecked after the locks are acquired")
						var current Call
						var attempt Attempt
						require.NoError(t, db.First(&current, "call_id = ?", s.call.CallID).Error)
						require.NoError(t, db.First(&attempt, "id = ?", s.attempt.ID).Error)
						if change.attempt {
							assert.Equal(t, "other-upstream", attempt.UpstreamIdentity)
						} else if change.name == "call binding" {
							assert.Equal(t, "other-credential", current.CredentialID)
						} else {
							assert.Equal(t, "WAIVER_PENDING", current.Status)
							assert.Equal(t, change.values["intent"], current.Intent)
						}
						assert.Equal(t, "100", current.Amount)
						assert.Empty(t, current.Evidence)
						assert.Empty(t, current.NativeObservation)
						assert.Empty(t, current.Charged)
						assert.False(t, current.EvidenceConflict)
						var rejected, results, attempts int64
						require.NoError(t, db.Model(&knowledgeRejectedUsage{}).Count(&rejected).Error)
						require.NoError(t, db.Model(&knowledgeResultRow{}).Count(&results).Error)
						require.NoError(t, db.Model(&Attempt{}).Count(&attempts).Error)
						assert.Zero(t, rejected, "stale binding must not be certified in nonchargeable evidence")
						assert.Zero(t, results)
						assert.EqualValues(t, 1, attempts)
					})
				}
			}
		})
	}
}
