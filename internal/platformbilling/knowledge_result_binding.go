package platformbilling

// Private experimental primitives. A future relay integration must bind the
// actual supplier connection to this session and preserve usage on failure.

import (
	"bytes"
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
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

// CaptureAliEmbeddingResult is enabled only by the programmatic test gate. The
// caller must pass the exact response read from the certified supplier request.
// It never redispatches, and no public recovery endpoint is registered.
func (s *Session) CaptureKnowledgeResult(ctx context.Context, channel int, upstream, key string, raw []byte) ([]byte, error) {
	if (!s.AliEmbeddingV4() && !s.AliRerank()) || s.adapter.knowledgeResults == nil || len(raw) == 0 || len(raw) > knowledgeResultMaxBytes {
		return nil, ErrInvalid
	}
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	vault := s.adapter.knowledgeResults
	sealed, err := vault.validateForCapture(work, s, channel, upstream, key, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw))})
	if err != nil {
		return nil, err
	}
	if err = vault.captureValidated(work, s, sealed); err != nil {
		return nil, err
	}
	if err = s.Finish(work); err != nil {
		return nil, err
	}
	var settled Call
	if err = s.adapter.db.WithContext(work).First(&settled, "call_id = ?", s.call.CallID).Error; err != nil || settled.Status != "SETTLED" || settled.EvidenceConflict || !sameKnowledgeCallBinding(settled, s.call) {
		return nil, ErrPending
	}
	if err = s.adapter.verifyKnowledgeSettlement(work, settled); err != nil {
		return nil, err
	}
	result, err := vault.readSettled(work, s.call.CallID)
	if err != nil {
		return nil, err
	}
	var current Call
	if err = s.adapter.db.WithContext(work).First(&current, "call_id = ?", s.call.CallID).Error; err != nil || current.Status != "SETTLED" || current.EvidenceConflict || !sameKnowledgeCallBinding(current, settled) || current.Evidence != settled.Evidence || current.Intent != settled.Intent || current.Charged != settled.Charged {
		return nil, ErrPending
	}
	if ctx.Err() != nil {
		return nil, ErrPending
	}
	return result, nil
}

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
		if err := v.checkWriter(tx); err != nil {
			return err
		}
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
		embeddingSource := "oai_chat"
		if frozen.Policy.Version == "knowledge_ali_text_v1" {
			embeddingSource = "ali_text_embedding_v4"
		}
		if (row.Operation == "embedding" && usage.Source != embeddingSource) || (row.Operation == "rerank" && usage.Source != "ali_rerank_actual_v1") || (row.Operation != "embedding" && row.Operation != "rerank") {
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
		row.Intent = string(intent)
		expires := v.now().Add(v.ttl).UnixMilli()
		observation := hmac.New(sha256.New, v.sealKey)
		_, _ = observation.Write([]byte("provider-response-digest-v1"))
		_, _ = observation.Write(result.rawDigest[:])
		item := knowledgeResultRow{CallID: row.CallID, KeyID: v.active, ExpiresAt: expires, BindingVersion: 2, AttemptID: attempt.ID, ObservationTag: hex.EncodeToString(observation.Sum(nil))}
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
