package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestTaskSettlementReadRequiresMatchingAuthoritativeReceipt(t *testing.T) {
	s, _, _ := responseFixture(t, "rerank")
	db := s.adapter.db
	row := s.call
	row.Operation, row.WireOperation, row.ProviderTaskID = "ocr", "ocr.jobs.create", "provider-task"
	row.Status, row.Asset, row.FundingMode, row.FundingProductID = "SETTLED", "CREDIT/6", "credits", "llmrouter"
	row.CredentialID, row.Amount, row.Charged = "credential", "10", "3"
	funding := RemoteCall{Allocations: []Allocation{{BatchID: "batch", Amount: "10"}}}
	row.FundingSnapshot = funding.fundingSnapshot()
	row.Evidence = `{"version":"metered_v2","operation":"ocr","state":"confirmed","source":"success_request","provider_request_id":"provider-task","measurements":[{"key":"requests","unit":"request","quantity":{"value":"1"}}]}`
	row.Intent = `{"usage":{"metered":` + row.Evidence + `},"provider_cost":""}`
	require.NoError(t, db.Save(&row).Error)
	var rate Rate
	require.NoError(t, common.Unmarshal([]byte(row.RateSnapshot), &rate))
	var intent struct {
		Usage        Usage  `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}
	require.NoError(t, common.Unmarshal([]byte(row.Intent), &intent))
	raw, err := common.Marshal(struct {
		CallID       string `json:"call_id"`
		Usage        *Usage `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}{row.CallID, &intent.Usage, intent.ProviderCost})
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	remote := knowledgeSettledReceipt{RemoteCall: RemoteCall{CallID: row.CallID, InstanceID: row.InstanceID, ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, CredentialID: row.CredentialID, RequestDigest: row.Digest, WireOperation: row.WireOperation, Rate: rate, Held: row.Amount, Allocations: funding.Allocations, ChargeAsset: row.Asset, FundingMode: row.FundingMode, FundingProductID: row.FundingProductID, Status: "SETTLED", Charged: row.Charged}, TaskID: row.TaskID, ServiceID: "llmrouter", Usage: &intent.Usage, FinalDigest: hex.EncodeToString(sum[:])}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/v1/calls/"+row.CallID, r.URL.Path)
		b, _ := common.Marshal(remote)
		_, _ = w.Write(b)
	}))
	defer server.Close()
	s.adapter.client = &client{origin: server.URL, token: strings.Repeat("s", 32), http: server.Client()}
	require.NoError(t, s.adapter.VerifyTaskSettlement(context.Background(), row.CallID))
	remote.Status = "SETTLE_PENDING"
	require.Error(t, s.adapter.VerifyTaskSettlement(context.Background(), row.CallID))
	remote.Status = "SETTLED"
	remote.FinalDigest = strings.Repeat("0", 64)
	require.Error(t, s.adapter.VerifyTaskSettlement(context.Background(), row.CallID))
	remote.FinalDigest = hex.EncodeToString(sum[:])
	remote.Charged = "4"
	require.Error(t, s.adapter.VerifyTaskSettlement(context.Background(), row.CallID))
	remote.Charged = row.Charged
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("evidence_conflict", true).Error)
	require.Error(t, s.adapter.VerifyTaskSettlement(context.Background(), row.CallID))
}
