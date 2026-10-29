package platformbilling

// Private experimental primitives. A future relay integration must bind the
// actual supplier connection to this session and preserve usage on failure.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

// The staged artifact contains only immutable values; copies cannot share a
// mutable output or measurement slice with a concurrent caller.
type sealedKnowledgeResponse struct {
	owner         *Session
	snapshot      Call
	attempt       Attempt
	rawDigest     [32]byte
	seal          [32]byte
	callID        string
	attemptID     uint
	requestDigest string
	wire          WireOperation
	body          string
	usage         string
}

func (v *knowledgeResultVault) responseSeal(r sealedKnowledgeResponse) ([]byte, error) {
	bodyHash := sha256.Sum256([]byte(r.body))
	data, err := common.Marshal([]any{"knowledge-response-binding-v1", fmt.Sprintf("%p", r.owner), r.snapshot, r.attempt, r.callID, r.attemptID, r.requestDigest, r.wire, r.rawDigest, bodyHash, r.usage})
	if err != nil {
		return nil, ErrInvalid
	}
	mac := hmac.New(sha256.New, v.sealKey)
	_, _ = mac.Write(data)
	return mac.Sum(nil), nil
}

func (v *knowledgeResultVault) validateForCapture(ctx context.Context, s *Session, channel int, upstream, key string, response *http.Response) (sealedKnowledgeResponse, error) {
	if v == nil || s == nil || s.adapter == nil || s.adapter.db == nil {
		return sealedKnowledgeResponse{}, ErrInvalid
	}
	a, err := v.db.DB()
	if err != nil {
		return sealedKnowledgeResponse{}, ErrInvalid
	}
	b, err := s.adapter.db.DB()
	if err != nil || a != b {
		return sealedKnowledgeResponse{}, ErrInvalid
	}
	result, err := validateKnowledgeResponse(ctx, s, channel, upstream, key, response)
	if err != nil {
		return sealedKnowledgeResponse{}, err
	}
	usage, err := common.Marshal(result.usage)
	if err != nil {
		return sealedKnowledgeResponse{}, ErrInvalid
	}
	staged := sealedKnowledgeResponse{owner: s, snapshot: result.snapshot, attempt: result.attempt, rawDigest: result.rawDigest, callID: result.callID, attemptID: result.attemptID, requestDigest: result.requestDigest, wire: result.wire, body: string(result.body), usage: string(usage)}
	seal, err := v.responseSeal(staged)
	if err != nil {
		return sealedKnowledgeResponse{}, err
	}
	copy(staged.seal[:], seal)
	return staged, nil
}

// captureValidated records validated usage, encrypted output and settlement
// intent together. It never invokes settlement or marks uncertain usage free.
func (v *knowledgeResultVault) captureValidated(ctx context.Context, s *Session, result sealedKnowledgeResponse) error {
	if v == nil || s == nil || s.adapter == nil || s.adapter.db == nil || result.owner != s || len(result.body) == 0 || len(result.body) > knowledgeResultMaxBytes {
		return ErrInvalid
	}
	a, err := v.db.DB()
	if err != nil {
		return ErrInvalid
	}
	b, err := s.adapter.db.DB()
	if err != nil || a != b {
		return ErrInvalid
	}
	seal, err := v.responseSeal(result)
	if err != nil || !hmac.Equal(seal, result.seal[:]) {
		return ErrInvalid
	}
	if result.callID != result.snapshot.CallID || result.attemptID != result.attempt.ID || result.requestDigest != result.snapshot.Digest || result.wire.ID != result.snapshot.WireOperation {
		return ErrInvalid
	}
	var usage metering.Usage
	if common.UnmarshalUnique([]byte(result.usage), &usage) != nil {
		return ErrInvalid
	}
	var conflict bool
	err = v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Rejected locks Attempt before Call. Match its order so a pending
		// rejection cannot leave a stale DISPATCHED observation in this commit.
		if err := tx.Model(&Attempt{}).Where("id = ? AND call_id = ?", result.attemptID, result.callID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return ErrPending
		}
		var attempt Attempt
		if err := tx.First(&attempt, "id = ? AND call_id = ?", result.attemptID, result.callID).Error; err != nil || attempt.Status != "DISPATCHED" || attempt.ChannelID != result.attempt.ChannelID || attempt.UpstreamIdentity != result.attempt.UpstreamIdentity {
			return ErrPending
		}
		if err := tx.Model(&Call{}).Where("call_id = ?", result.callID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return ErrPending
		}
		var row Call
		if err := tx.First(&row, "call_id = ?", result.callID).Error; err != nil || row.EvidenceConflict || !sameKnowledgeCallBinding(row, result.snapshot) {
			return ErrPending
		}
		var frozen frozenKnowledge
		var rate Rate
		if common.UnmarshalUnique([]byte(row.MediaSnapshot), &frozen) != nil || !frozen.Policy.valid(s.adapter.config, row.Model, row.WireOperation) || frozen.Policy.ChannelID != attempt.ChannelID || common.UnmarshalUnique([]byte(row.RateSnapshot), &rate) != nil || rate.Metered == nil {
			return ErrInvalid
		}
		asset, err := normalizeAsset(row.Asset)
		_, certified := TokenProducer(row.WireOperation, frozen.Policy.ChannelType)
		if err != nil || !certified || rate.ID != row.RateID || rate.Model != row.Model || rate.Asset != asset || rate.Status != "published" || rate.Semantics != "metered_v2" || frozen.Policy.RateIDs[asset] != rate.ID || rate.Metered.Operation != row.Operation || metering.ValidateUsage(*rate.Metered, usage) != nil {
			return ErrInvalid
		}
		if (row.Operation == "embedding" && usage.Source != "oai_chat") || (row.Operation == "rerank" && usage.Source != "ali_rerank_actual_v1") || (row.Operation != "embedding" && row.Operation != "rerank") {
			return ErrInvalid
		}
		total := new(big.Int)
		for _, measurement := range usage.Measurements {
			amount, ok := new(big.Int).SetString(measurement.Quantity.Value, 10)
			if !ok || amount.Sign() < 0 || measurement.Quantity.Scale != 0 || measurement.Unit != "token" {
				return ErrInvalid
			}
			total.Add(total, amount)
		}
		if row.ReservationQuantity < 1 || total.Cmp(big.NewInt(row.ReservationQuantity)) > 0 {
			return ErrInvalid
		}
		usageBytes, err := common.Marshal(usage)
		if err != nil {
			return ErrInvalid
		}
		oldEvidence := row.Evidence
		if oldEvidence != "" {
			var existing metering.Usage
			if common.UnmarshalUnique([]byte(oldEvidence), &existing) != nil {
				return ErrPending
			}
			if !reflect.DeepEqual(existing, usage) {
				if existing.State != "confirmed" {
					return ErrPending
				}
				saved := tx.Model(&Call{}).Where("call_id = ? AND evidence = ? AND evidence_conflict = ?", row.CallID, oldEvidence, false).Update("evidence_conflict", true)
				if saved.Error != nil || saved.RowsAffected != 1 {
					return ErrPending
				}
				conflict = true
				return nil
			}
		} else {
			row.Evidence = string(usageBytes)
		}
		if row.Status != "UNKNOWN" {
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
		observation := hmac.New(sha256.New, v.sealKey)
		_, _ = observation.Write([]byte("provider-response-digest-v1"))
		_, _ = observation.Write(result.rawDigest[:])
		item := knowledgeResultRow{CallID: row.CallID, ExpiresAt: expires, BindingVersion: 1, AttemptID: attempt.ID, ObservationTag: hex.EncodeToString(observation.Sum(nil))}
		aad, err := knowledgeResultStoredAAD(row, item)
		if err != nil {
			return ErrInvalid
		}
		nonce := make([]byte, v.aead.NonceSize())
		if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
			return ErrPending
		}
		item.Ciphertext = v.aead.Seal(nonce, nonce, []byte(result.body), aad)
		if err := tx.Create(&item).Error; err != nil {
			return ErrPending
		}
		saved := tx.Model(&Call{}).Where("call_id = ? AND status = ? AND evidence = ? AND evidence_conflict = ?", row.CallID, "UNKNOWN", oldEvidence, false).Updates(map[string]any{"evidence": row.Evidence, "status": "SETTLE_PENDING", "intent": string(intent)})
		if saved.Error != nil || saved.RowsAffected != 1 {
			return ErrPending
		}
		return nil
	})
	if err != nil {
		return err
	}
	if conflict {
		return ErrConflict
	}
	return nil
}
