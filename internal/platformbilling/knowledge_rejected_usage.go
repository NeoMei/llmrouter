package platformbilling

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

// One bounded, nonchargeable operational observation per call. This is never a
// metering.Usage envelope, result, settlement command or recovery queue entry.
// The first observation is immutable; contradictory responses cannot replace it.
type knowledgeRejectedUsage struct {
	CallID            string `gorm:"primaryKey;size:128"`
	AttemptID         uint
	BindingDigest     string `gorm:"size:64"`
	ObservationDigest string `gorm:"size:64"`
	DigestKeyID       string `gorm:"size:32"`
	Measurements      string `gorm:"type:text"`
	Source            string `gorm:"size:64"`
	Reason            string `gorm:"size:64"`
	Disposition       string `gorm:"size:32"`
	CreatedAt         time.Time
}

func (knowledgeRejectedUsage) TableName() string { return "platform_knowledge_rejected_usage" }

// Called only after captureValidated has locked and rechecked the original
// attempt, full immutable call binding, policy, rate and complete response usage.
// A successful commit still rejects delivery; a failed write stays unresolved.
func (v *knowledgeResultVault) retainRejectedKnowledgeUsage(tx *gorm.DB, row Call, attempt Attempt, result sealedKnowledgeResponse, usage metering.Usage, prior knowledgeRejectedUsage, exists bool) error {
	measurements, err := common.Marshal(usage.Measurements)
	if err != nil || len(measurements) == 0 || len(measurements) > 4096 || len(usage.Source) > 64 {
		return ErrInvalid
	}
	binding, err := common.Marshal([]any{"knowledge-rejected-binding-v1",
		row.CallID, row.ActorID, row.AccountID, row.TaskID, row.OriginProductID, row.InstanceID,
		row.CredentialID, row.CredentialKind, row.Model, row.Digest, row.DigestVersion,
		row.WireOperation, row.WirePathModel, row.WireMode, row.Operation, row.MediaSnapshot,
		row.RateSnapshot, row.RateID, row.Asset, row.FundingMode, row.FundingProductID,
		row.FundingSnapshot, row.BillingProfileVersion, row.Amount, row.ReservationQuantity, row.ReservationOutput,
		attempt.ID, attempt.CallID, attempt.ChannelID, attempt.UpstreamIdentity, attempt.CreatedAt.UTC()})
	if err != nil {
		return ErrInvalid
	}
	keyID := v.active
	if exists {
		keyID = prior.DigestKeyID
	}
	digestKey := v.digestKeys[keyID]
	if len(digestKey) == 0 {
		return ErrPending
	}
	bindingMAC := hmac.New(sha256.New, digestKey)
	_, _ = bindingMAC.Write(binding)
	observationMAC := hmac.New(sha256.New, digestKey)
	_, _ = observationMAC.Write([]byte("knowledge-rejected-response-v1"))
	_, _ = observationMAC.Write(result.rawDigest[:])
	item := knowledgeRejectedUsage{CallID: row.CallID, AttemptID: attempt.ID,
		BindingDigest: hex.EncodeToString(bindingMAC.Sum(nil)), ObservationDigest: hex.EncodeToString(observationMAC.Sum(nil)), DigestKeyID: keyID,
		Measurements: string(measurements), Source: usage.Source, Reason: "reservation_quantity_exceeded", Disposition: "NONCHARGEABLE"}
	if exists {
		// Compare every retained field; CreatedAt records the original commit only.
		item.CreatedAt = prior.CreatedAt
		if item != prior {
			return ErrConflict
		}
		return nil
	}
	if err := tx.Create(&item).Error; err != nil {
		return ErrPending
	}
	return nil
}
