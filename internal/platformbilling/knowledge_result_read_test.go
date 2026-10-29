package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestKnowledgeResultReadRequiresBoundGrantAndRemoteSettlement(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/authority.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			s, key, upstream := responseFixtureOnDB(t, "rerank", knowledgeResultTestDB(t, dialect, dsn))
			funding := RemoteCall{Allocations: []Allocation{{BatchID: "batch", Amount: "10"}}}
			s.call.FundingSnapshot = funding.fundingSnapshot()
			s.call.Amount = "10"
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Updates(map[string]any{"funding_snapshot": s.call.FundingSnapshot, "amount": s.call.Amount}).Error)
			v, err := newKnowledgeResultVault(s.adapter.db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
			require.NoError(t, err)
			raw := `{"output":{"results":[{"index":1,"relevance_score":0.9,"document":{"text":"second"}}]},"usage":{"total_tokens":3}}`
			staged, err := v.validateForCapture(context.Background(), s, 1, upstream, key, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))})
			require.NoError(t, err)
			require.NoError(t, v.captureValidated(context.Background(), s, staged))
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", s.call.CallID).Updates(map[string]any{"status": "SETTLED", "charged": "3"}).Error)
			var row Call
			require.NoError(t, s.adapter.db.First(&row, "call_id = ?", s.call.CallID).Error)
			var rate Rate
			require.NoError(t, common.Unmarshal([]byte(row.RateSnapshot), &rate))
			var intent struct {
				Usage        Usage  `json:"usage"`
				ProviderCost string `json:"provider_cost"`
			}
			require.NoError(t, common.Unmarshal([]byte(row.Intent), &intent))
			payload, err := common.Marshal(struct {
				CallID       string `json:"call_id"`
				Usage        *Usage `json:"usage"`
				ProviderCost string `json:"provider_cost"`
			}{row.CallID, &intent.Usage, intent.ProviderCost})
			require.NoError(t, err)
			digest := sha256.Sum256(payload)
			remote := knowledgeSettledReceipt{RemoteCall: RemoteCall{CallID: row.CallID, ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, CredentialID: row.CredentialID, RequestDigest: row.Digest, WireOperation: row.WireOperation, Rate: rate, Held: row.Amount, Allocations: funding.Allocations, ChargeAsset: row.Asset, FundingMode: row.FundingMode, FundingProductID: "llmrouter", Status: "SETTLED", Charged: "3"}, TaskID: row.TaskID, ServiceID: "llmrouter", Usage: &intent.Usage, FinalDigest: hex.EncodeToString(digest[:])}
			grant := Claims{ID: "credential", Audience: "llmrouter", ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, TaskID: row.TaskID, Models: []string{row.Model}, Operations: map[string][]string{row.Model: {row.WireOperation}}, ExpiresAt: time.Now().Add(time.Hour)}
			var duplicate bool
			var duringGet func()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var data []byte
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					if duplicate {
						_, _ = w.Write([]byte(`{"actor_id":"one","actor_id":"two"}`))
						return
					}
					data, _ = common.Marshal(grant)
				case "/internal/v1/calls/call":
					if duringGet != nil {
						duringGet()
					}
					data, _ = common.Marshal(remote)
				default:
					w.WriteHeader(404)
					return
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			s.adapter.client = &client{origin: server.URL, token: "service", http: server.Client()}
			got, err := v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			require.NoError(t, err)
			assert.Equal(t, staged.body, string(got))
			var stored knowledgeResultRow
			require.NoError(t, s.adapter.db.First(&stored, "call_id = ?", row.CallID).Error)
			clockReads := 0
			v.now = func() time.Time {
				clockReads++
				if clockReads == 1 {
					return time.UnixMilli(stored.ExpiresAt - 1)
				}
				return time.UnixMilli(stored.ExpiresAt)
			}
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			v.now = time.Now
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("funding_snapshot", "").Error)
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("funding_snapshot", row.FundingSnapshot).Error)
			duplicate = true
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			duplicate = false
			var mutationErr error
			duringGet = func() {
				mutationErr = s.adapter.db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("credential_id", "modified").Error
			}
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			require.NoError(t, mutationErr)
			duringGet = nil
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("credential_id", row.CredentialID).Error)
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", "other", row.CallID)
			assert.Error(t, err)
			_, missingErr := v.authorizedResult(context.Background(), s.adapter, "uh_model_invalid", "other", "missing")
			_, existingErr := v.authorizedResult(context.Background(), s.adapter, "uh_model_invalid", "other", row.CallID)
			assert.ErrorIs(t, missingErr, ErrInvalid)
			assert.ErrorIs(t, existingErr, ErrInvalid)
			grant.AccountID = "other"
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			grant.AccountID = row.AccountID
			remote.TaskID = "other"
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			remote.TaskID = row.TaskID
			remote.FinalDigest = strings.Repeat("0", 64)
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			remote.FinalDigest = hex.EncodeToString(digest[:])
			remote.Status = "RESERVED"
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			remote.Status = "SETTLED"
			remote.Usage = nil
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			remote.Usage = &intent.Usage
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_key_original", row.TaskID, row.CallID)
			assert.Error(t, err)
			// A valid v0 ciphertext must remain unreadable through the new boundary.
			require.NoError(t, s.adapter.db.Where("call_id = ?", row.CallID).Delete(&knowledgeResultRow{}).Error)
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("status", "UNKNOWN").Error)
			require.NoError(t, v.capture(context.Background(), row.CallID, []byte("legacy")))
			require.NoError(t, s.adapter.db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("status", "SETTLED").Error)
			_, err = v.authorizedResult(context.Background(), s.adapter, "uh_model_original", row.TaskID, row.CallID)
			assert.Error(t, err)
		})
	}
}
