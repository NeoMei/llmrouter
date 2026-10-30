package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"net/http"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
)

// VerifyTaskSettlement checks the exact durable intent against the authoritative
// billing receipt. It authorizes no user and must follow an owned task check.
func (a *Adapter) VerifyTaskSettlement(ctx context.Context, id string) error {
	if !a.Enabled() || a.db == nil || a.client == nil || !stableID.MatchString(id) {
		return ErrInvalid
	}
	var row Call
	if a.db.WithContext(ctx).First(&row, "call_id = ?", id).Error != nil || row.Status != "SETTLED" || row.EvidenceConflict || row.ProviderTaskID == "" || row.FundingSnapshot == "" || !((row.Operation == "ocr" && row.WireOperation == "ocr.jobs.create") || (row.Operation == "video" && row.WireOperation == "videos.create")) {
		return ErrPending
	}
	var evidence metering.Usage
	var intent struct {
		Usage        Usage  `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}
	if common.UnmarshalUnique([]byte(row.Evidence), &evidence) != nil || evidence.State != "confirmed" || evidence.ProviderRequestID != row.ProviderTaskID || common.UnmarshalUnique([]byte(row.Intent), &intent) != nil || intent.Usage.Metered == nil || !reflect.DeepEqual(*intent.Usage.Metered, evidence) {
		return ErrInvalid
	}
	var receipt knowledgeSettledReceipt
	if err := readKnowledgeAuthority(ctx, a.client, http.MethodGet, "/internal/v1/calls/"+id, nil, &receipt, ""); err != nil {
		return ErrPending
	}
	if receipt.Status != "SETTLED" || receipt.ServiceID != "llmrouter" || receipt.TaskID != row.TaskID || !receipt.RemoteCall.matches(row) || receipt.fundingSnapshot() != row.FundingSnapshot || receipt.Charged != row.Charged || receipt.Usage == nil || !reflect.DeepEqual(*receipt.Usage, intent.Usage) || receipt.ProviderCost != intent.ProviderCost {
		return ErrInvalid
	}
	charged, ok := new(big.Int).SetString(receipt.Charged, 10)
	if !ok || charged.Sign() < 0 || charged.String() != receipt.Charged {
		return ErrInvalid
	}
	payload, err := common.Marshal(struct {
		CallID       string `json:"call_id"`
		Usage        *Usage `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}{id, &intent.Usage, intent.ProviderCost})
	if err != nil {
		return ErrInvalid
	}
	digest := sha256.Sum256(payload)
	if receipt.FinalDigest != hex.EncodeToString(digest[:]) {
		return ErrInvalid
	}
	var current Call
	if a.db.WithContext(ctx).First(&current, "call_id = ?", id).Error != nil || current.Status != "SETTLED" || current.EvidenceConflict || current.ProviderTaskID != row.ProviderTaskID || current.Evidence != row.Evidence || current.Intent != row.Intent || current.Charged != row.Charged || !sameKnowledgeCallBinding(current, row) || ctx.Err() != nil {
		return ErrPending
	}
	return nil
}
