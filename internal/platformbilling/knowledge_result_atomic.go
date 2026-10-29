package platformbilling

// This transaction is private and intentionally not wired to any endpoint.
// Callers must validate the complete response and preserve the billing rules
// for invalid or interrupted results before this primitive can be used.

import (
	"context"
	"crypto/rand"
	"io"
	"math/big"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

// captureAndPrepare atomically records the encrypted observation and the
// terminal settlement command. It does not send the command or reveal output.
func (v *knowledgeResultVault) captureAndPrepare(ctx context.Context, callID string, result []byte) error {
	if v == nil || !stableID.MatchString(callID) || len(result) == 0 || len(result) > knowledgeResultMaxBytes {
		return ErrInvalid
	}
	return v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Call{}).Where("call_id = ?", callID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return ErrPending
		}
		var row Call
		if err := tx.First(&row, "call_id = ?", callID).Error; err != nil {
			return ErrPending
		}
		if row.Status != "UNKNOWN" || row.EvidenceConflict || row.ActorID == "" || row.AccountID == "" || row.OriginProductID == "" || row.TaskID == "" || row.Model == "" || row.Digest == "" || row.ReservationQuantity < 0 || !((row.WireOperation == "embeddings.create" && row.Operation == "embedding") || (row.WireOperation == "rerank.create" && row.Operation == "rerank")) {
			return ErrPending
		}
		var rate Rate
		var usage metering.Usage
		if common.UnmarshalUnique([]byte(row.RateSnapshot), &rate) != nil || rate.Metered == nil || common.UnmarshalUnique([]byte(row.Evidence), &usage) != nil || metering.ValidateUsage(*rate.Metered, usage) != nil || usage.Operation != row.Operation {
			return ErrInvalid
		}
		for _, measurement := range usage.Measurements {
			value, ok := new(big.Int).SetString(measurement.Quantity.Value, 10)
			if !ok || measurement.Unit != "token" || measurement.Quantity.Scale != 0 || value.Cmp(big.NewInt(row.ReservationQuantity)) > 0 {
				return ErrInvalid
			}
		}
		var frozen frozenKnowledge
		if common.UnmarshalUnique([]byte(row.MediaSnapshot), &frozen) != nil || frozen.Policy.ChannelID < 1 {
			return ErrInvalid
		}
		var attempts []Attempt
		if err := tx.Where("call_id = ? AND status = ?", callID, "DISPATCHED").Limit(2).Find(&attempts).Error; err != nil || len(attempts) != 1 || attempts[0].ChannelID != frozen.Policy.ChannelID {
			return ErrPending
		}
		intent, err := common.Marshal(struct {
			Usage        Usage  `json:"usage"`
			ProviderCost string `json:"provider_cost"`
		}{Usage{Metered: &usage}, ""})
		if err != nil {
			return ErrInvalid
		}
		expires := v.now().Add(v.ttl).UnixMilli()
		aad, err := knowledgeResultAAD(row, expires)
		if err != nil {
			return ErrInvalid
		}
		nonce := make([]byte, v.aead.NonceSize())
		if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
			return ErrPending
		}
		item := knowledgeResultRow{CallID: callID, ExpiresAt: expires, Ciphertext: v.aead.Seal(nonce, nonce, result, aad)}
		if err = tx.Create(&item).Error; err != nil {
			return ErrPending
		}
		saved := tx.Model(&Call{}).Where("call_id = ? AND status = ? AND evidence = ? AND evidence_conflict = ?", callID, "UNKNOWN", row.Evidence, false).Updates(map[string]any{"status": "SETTLE_PENDING", "intent": string(intent)})
		if saved.Error != nil || saved.RowsAffected != 1 {
			return ErrPending
		}
		return nil
	})
}
