package platformbilling

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeResultAuthenticatesFinancialSnapshot(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/financial.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			db := knowledgeResultTestDB(t, dialect, dsn)
			s, key, upstream := responseFixtureOnDB(t, "rerank", db)
			v, err := newKnowledgeResultVault(db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
			require.NoError(t, err)
			raw := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
			staged, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))})
			require.NoError(t, err)
			require.NoError(t, v.captureValidated(context.Background(), s, staged))
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Updates(map[string]any{"status": "SETTLED", "charged": "3"}).Error)
			var original Call
			require.NoError(t, db.First(&original, "call_id = ?", s.call.CallID).Error)
			got, err := v.readSettled(context.Background(), s.call.CallID)
			require.NoError(t, err)
			assert.Equal(t, staged.body, string(got))
			fields := []struct {
				name              string
				changed, original any
			}{
				{"rate_snapshot", "{}", original.RateSnapshot},
				{"rate_id", "changed", original.RateID},
				{"amount", "999", original.Amount},
				{"asset", "CNY/6", original.Asset},
				{"funding_mode", "changed", original.FundingMode},
				{"funding_product_id", "changed", original.FundingProductID},
				{"funding_snapshot", "changed", original.FundingSnapshot},
				{"billing_profile_version", original.BillingProfileVersion + 1, original.BillingProfileVersion},
				{"reservation_quantity", original.ReservationQuantity + 1, original.ReservationQuantity},
				{"reservation_output", original.ReservationOutput + 1, original.ReservationOutput},
				{"media_snapshot", "changed", original.MediaSnapshot},
				{"intent", "{}", original.Intent},
				{"digest_version", "changed", original.DigestVersion},
				{"wire_path_model", "changed", original.WirePathModel},
				{"operation", "changed", original.Operation},
			}
			for _, field := range fields {
				t.Run(field.name, func(t *testing.T) {
					require.NoError(t, db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update(field.name, field.changed).Error)
					_, err := v.readSettled(context.Background(), s.call.CallID)
					assert.Error(t, err)
					require.NoError(t, db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Update(field.name, field.original).Error)
				})
			}
			got, err = v.readSettled(context.Background(), s.call.CallID)
			require.NoError(t, err)
			assert.Equal(t, staged.body, string(got))
		})
	}
}

// The locally sealed intent must survive actual pending settlement recovery;
// the authoritative receipt, rather than AAD, determines the later charge.
func TestKnowledgeFinancialBindingSurvivesSettlementRecovery(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/settlement.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			db := knowledgeResultTestDB(t, dialect, dsn)
			s, key, upstream := responseFixtureOnDB(t, "rerank", db)
			v, err := newKnowledgeResultVault(db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
			require.NoError(t, err)
			raw := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
			staged, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(raw))})
			require.NoError(t, err)
			require.NoError(t, v.captureValidated(context.Background(), s, staged))
			var before Call
			require.NoError(t, db.First(&before, "call_id = ?", s.call.CallID).Error)
			var rate Rate
			require.NoError(t, common.Unmarshal([]byte(before.RateSnapshot), &rate))
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/calls/call/settle" {
					http.Error(w, "unexpected", http.StatusNotFound)
					return
				}
				calls++
				if calls == 1 {
					http.Error(w, "retry later", http.StatusServiceUnavailable)
					return
				}
				receipt := RemoteCall{CallID: before.CallID, ActorID: before.ActorID, AccountID: before.AccountID, OriginProductID: before.OriginProductID, CredentialID: before.CredentialID, RequestDigest: before.Digest, WireOperation: before.WireOperation, Rate: rate, Held: before.Amount, ChargeAsset: before.Asset, FundingMode: before.FundingMode, FundingProductID: before.FundingProductID, Status: "SETTLED", Charged: "3"}
				data, marshalErr := common.Marshal(receipt)
				if marshalErr != nil {
					http.Error(w, "invalid", http.StatusInternalServerError)
					return
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			s.adapter.client = &client{origin: server.URL, token: "fixture", http: server.Client()}
			require.NoError(t, s.adapter.Recover(context.Background()))
			_, err = v.readSettled(context.Background(), s.call.CallID)
			assert.Error(t, err)
			require.NoError(t, s.adapter.Recover(context.Background()))
			got, err := v.readSettled(context.Background(), s.call.CallID)
			require.NoError(t, err)
			assert.Equal(t, staged.body, string(got))
			var after Call
			require.NoError(t, db.First(&after, "call_id = ?", s.call.CallID).Error)
			assert.Equal(t, "SETTLED", after.Status)
			assert.Equal(t, "3", after.Charged)
			assert.Equal(t, before.Intent, after.Intent)
			assert.Equal(t, 2, calls)
		})
	}
}
