package router

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/stretchr/testify/require"
	"testing"
)

type authorityTaskReceipt struct {
	platformbilling.RemoteCall
	TaskID       string                 `json:"task_id"`
	ServiceID    string                 `json:"service_id"`
	Usage        *platformbilling.Usage `json:"usage,omitempty"`
	FinalDigest  string                 `json:"final_digest,omitempty"`
	ProviderCost string                 `json:"provider_cost,omitempty"`
}

type taskSettlementInput struct {
	Usage        *platformbilling.Usage `json:"usage"`
	ProviderCost string                 `json:"provider_cost"`
}

func authorityReceipt(t *testing.T, remote platformbilling.RemoteCall, task string, settlement *taskSettlementInput) authorityTaskReceipt {
	t.Helper()
	receipt := authorityTaskReceipt{RemoteCall: remote, TaskID: task, ServiceID: "llmrouter"}
	if settlement != nil {
		receipt.Usage, receipt.ProviderCost = settlement.Usage, settlement.ProviderCost
		b, err := common.Marshal(struct {
			CallID       string                 `json:"call_id"`
			Usage        *platformbilling.Usage `json:"usage"`
			ProviderCost string                 `json:"provider_cost"`
		}{remote.CallID, settlement.Usage, settlement.ProviderCost})
		require.NoError(t, err)
		sum := sha256.Sum256(b)
		receipt.FinalDigest = hex.EncodeToString(sum[:])
	}
	return receipt
}
