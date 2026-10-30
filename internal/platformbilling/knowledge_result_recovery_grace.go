package platformbilling

import (
	"math"
	"math/big"
	"reflect"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
)

// The deadline is a runtime read/retention policy. The original expires_at stays
// authenticated in AAD and is never rewritten, resealed or made into a bearer.
func (v *knowledgeResultVault) effectiveReadDeadline(stored knowledgeResultRow) int64 {
	grace := v.settledRecoveryGrace.Milliseconds()
	if stored.BindingVersion != 2 || grace <= 0 || grace > (23*time.Hour).Milliseconds() || stored.ExpiresAt <= 0 || stored.ExpiresAt > math.MaxInt64-grace {
		return stored.ExpiresAt
	}
	return stored.ExpiresAt + grace
}

func validKnowledgeResultBinding(row Call, attempt Attempt, f frozenKnowledge) bool {
	b := f.Binding
	return b != nil && ApprovedKnowledgePair(row.Model, row.WireOperation) && opaqueKnowledgeID(b.PolicyDigest) && opaqueKnowledgeID(b.RouteGeneration) && b.InstanceID == row.InstanceID && b.OriginProductID == row.OriginProductID && b.Model == row.Model && b.WireOperation == row.WireOperation && b.ChannelID == attempt.ChannelID && attempt.CallID == row.CallID && row.CredentialID != ""
}

// Local financial validity is sufficient only to retain encrypted paid output.
// It never authorizes delivery; authorizedResult also requires the independent
// central SETTLED receipt, current actor and credential, and original v2 AAD.
func knowledgeSettledFinancialIntent(row Call) error {
	if row.Status != "SETTLED" || row.EvidenceConflict || row.FundingSnapshot == "" || !ApprovedKnowledgePair(row.Model, row.WireOperation) {
		return ErrInvalid
	}
	var rate Rate
	var evidence metering.Usage
	var intent struct {
		Usage        Usage  `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}
	if common.UnmarshalUnique([]byte(row.RateSnapshot), &rate) != nil || rate.Metered == nil || common.UnmarshalUnique([]byte(row.Evidence), &evidence) != nil || common.UnmarshalUnique([]byte(row.Intent), &intent) != nil || intent.Usage.Metered == nil || !reflect.DeepEqual(*intent.Usage.Metered, evidence) || metering.ValidateUsage(*rate.Metered, evidence) != nil || evidence.Operation != row.Operation {
		return ErrInvalid
	}
	price, err := metering.Calculate(*rate.Metered, evidence)
	if err != nil || price.Sign() < 0 {
		return ErrInvalid
	}
	amount, remainder := new(big.Int), new(big.Int)
	amount.QuoRem(price.Num(), price.Denom(), remainder)
	if remainder.Sign() > 0 {
		amount.Add(amount, big.NewInt(1))
	}
	held, ok := new(big.Int).SetString(row.Amount, 10)
	if !ok || held.Sign() < 0 || held.String() != row.Amount {
		return ErrInvalid
	}
	if amount.Cmp(held) > 0 {
		amount.Set(held)
	}
	if amount.String() != row.Charged {
		return ErrInvalid
	}
	return nil
}
