package platformbilling

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestKnowledgeResultAndSettlementIntentAreAtomic(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/atomic.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			db := knowledgeResultTestDB(t, dialect, dsn)
			require.NoError(t, db.AutoMigrate(&Call{}, &Attempt{}))
			v, err := newKnowledgeResultVault(db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
			require.NoError(t, err)
			for _, op := range []string{"embedding", "rerank"} {
				wire := map[string]string{"embedding": "embeddings.create", "rerank": "rerank.create"}[op]
				price := metering.Price{Version: "metered_v2", Operation: op, Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
				usage := metering.Usage{Version: "metered_v2", Operation: op, State: "confirmed", Source: "test_provider", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "2"}}}}
				rate, err := common.Marshal(Rate{Metered: &price})
				require.NoError(t, err)
				evidence, err := common.Marshal(usage)
				require.NoError(t, err)
				frozen, err := common.Marshal(frozenKnowledge{Policy: KnowledgePolicy{ChannelID: 1}})
				require.NoError(t, err)
				row := Call{CallID: "atomic-" + op, ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", TaskID: "task", Model: "model", Digest: "digest", Status: "UNKNOWN", Operation: op, WireOperation: wire, Evidence: string(evidence), RateSnapshot: string(rate), MediaSnapshot: string(frozen), ReservationQuantity: 10}
				require.NoError(t, db.Create(&row).Error)
				require.NoError(t, db.Create(&Attempt{CallID: row.CallID, Status: "DISPATCHED", ChannelID: 1}).Error)
				var wg sync.WaitGroup
				errors := make(chan error, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						errors <- v.captureAndPrepare(context.Background(), row.CallID, []byte(`{"data":"validated fixture"}`))
					}()
				}
				wg.Wait()
				close(errors)
				success := 0
				for err := range errors {
					if err == nil {
						success++
					}
				}
				assert.Equal(t, 1, success)
				var actual Call
				require.NoError(t, db.First(&actual, "call_id = ?", row.CallID).Error)
				assert.Equal(t, "SETTLE_PENDING", actual.Status)
				assert.Contains(t, actual.Intent, `"state":"confirmed"`)
				var result knowledgeResultRow
				require.NoError(t, db.First(&result, "call_id = ?", row.CallID).Error)
				assert.NotEmpty(t, result.Ciphertext)
				assert.Error(t, v.captureAndPrepare(context.Background(), row.CallID, []byte(`{"data":"different"}`)))
			}
			bad := Call{CallID: "atomic-reject", Status: "UNKNOWN", Operation: "embedding", WireOperation: "embeddings.create", Evidence: `{"state":"missing"}`}
			require.NoError(t, db.Create(&bad).Error)
			assert.Error(t, v.captureAndPrepare(context.Background(), bad.CallID, []byte("response")))
			var n int64
			require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", bad.CallID).Count(&n).Error)
			assert.Zero(t, n)
			require.NoError(t, db.First(&bad, "call_id = ?", bad.CallID).Error)
			assert.Equal(t, "UNKNOWN", bad.Status)
			// A prior ciphertext tombstone must not leave a settlement intent.
			var original Call
			require.NoError(t, db.First(&original, "call_id = ?", "atomic-rerank").Error)
			original.CallID, original.Status, original.Intent = "atomic-existing", "UNKNOWN", ""
			require.NoError(t, db.Create(&original).Error)
			require.NoError(t, db.Create(&Attempt{CallID: original.CallID, Status: "DISPATCHED", ChannelID: 1}).Error)
			require.NoError(t, db.Create(&knowledgeResultRow{CallID: original.CallID}).Error)
			assert.Error(t, v.captureAndPrepare(context.Background(), original.CallID, []byte("response")))
			require.NoError(t, db.First(&original, "call_id = ?", original.CallID).Error)
			assert.Equal(t, "UNKNOWN", original.Status)
			assert.Empty(t, original.Intent)
			// Inject a failure after the ciphertext INSERT but before the state UPDATE.
			original.CallID, original.Status, original.Intent = "atomic-rollback", "UNKNOWN", ""
			require.NoError(t, db.Create(&original).Error)
			require.NoError(t, db.Create(&Attempt{CallID: original.CallID, Status: "DISPATCHED", ChannelID: 1}).Error)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("fixture:fail_settlement_intent", func(tx *gorm.DB) {
				if values, ok := tx.Statement.Dest.(map[string]any); ok && values["status"] == "SETTLE_PENDING" {
					tx.AddError(errors.New("injected intent write failure"))
				}
			}))
			assert.Error(t, v.captureAndPrepare(context.Background(), original.CallID, []byte("response")))
			require.NoError(t, db.Callback().Update().Remove("fixture:fail_settlement_intent"))
			require.NoError(t, db.First(&original, "call_id = ?", original.CallID).Error)
			assert.Equal(t, "UNKNOWN", original.Status)
			require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", original.CallID).Count(&n).Error)
			assert.Zero(t, n)
		})
	}
}

func TestKnowledgeResultPendingSettlementRecoversWithoutRedispatch(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/recovery.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			db := knowledgeResultTestDB(t, dialect, dsn)
			require.NoError(t, db.AutoMigrate(&Call{}, &Attempt{}))
			key := []byte("0123456789abcdef0123456789abcdef")
			v, err := newKnowledgeResultVault(db, key, time.Hour)
			require.NoError(t, err)
			price := metering.Price{Version: "metered_v2", Operation: "rerank", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
			rate := Rate{ID: "rate", Model: "model", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
			usage := metering.Usage{Version: "metered_v2", Operation: "rerank", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "2"}}}}
			encodedRate, err := common.Marshal(rate)
			require.NoError(t, err)
			evidence, err := common.Marshal(usage)
			require.NoError(t, err)
			frozen, err := common.Marshal(frozenKnowledge{Policy: KnowledgePolicy{ChannelID: 1}})
			require.NoError(t, err)
			row := Call{CallID: "recover-result", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", CredentialID: "credential", TaskID: "task", Model: "model", Digest: "digest", RateID: "rate", Amount: "10", Asset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Status: "UNKNOWN", Operation: "rerank", WireOperation: "rerank.create", Evidence: string(evidence), RateSnapshot: string(encodedRate), MediaSnapshot: string(frozen), ReservationQuantity: 10}
			require.NoError(t, db.Create(&row).Error)
			require.NoError(t, db.Create(&Attempt{CallID: row.CallID, Status: "DISPATCHED", ChannelID: 1}).Error)
			result := []byte(`{"results":[{"index":0,"score":0.8}]}`)
			require.NoError(t, v.captureAndPrepare(context.Background(), row.CallID, result))
			var attempts atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/calls/recover-result/settle" {
					http.Error(w, "unexpected request", 404)
					return
				}
				if attempts.Add(1) == 1 {
					http.Error(w, "unavailable", 503)
					return
				}
				remote := RemoteCall{CallID: row.CallID, ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, CredentialID: row.CredentialID, RequestDigest: row.Digest, WireOperation: row.WireOperation, Rate: rate, Held: row.Amount, ChargeAsset: row.Asset, FundingMode: row.FundingMode, FundingProductID: row.FundingProductID, Status: "SETTLED", Charged: "2"}
				raw, err := common.Marshal(remote)
				if err != nil {
					http.Error(w, "bad fixture", 500)
					return
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			a := &Adapter{db: db, config: Config{Enabled: true}, client: &client{origin: server.URL, token: "fixture", http: server.Client()}}
			require.NoError(t, a.Recover(context.Background()))
			_, err = v.readSettled(context.Background(), row.CallID)
			assert.Error(t, err)
			reopened, err := newKnowledgeResultVault(db, key, time.Hour)
			require.NoError(t, err)
			require.NoError(t, a.Recover(context.Background()))
			got, err := reopened.readSettled(context.Background(), row.CallID)
			require.NoError(t, err)
			assert.Equal(t, result, got)
			var count int64
			require.NoError(t, db.Model(&Attempt{}).Where("call_id = ?", row.CallID).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			assert.EqualValues(t, 2, attempts.Load())
		})
	}
}
