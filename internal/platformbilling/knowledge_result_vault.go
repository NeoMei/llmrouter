package platformbilling

// This private vault is intentionally not connected to a production route.
// A caller must first add bounded response capture and prove complete response
// validation, atomic terminal intent, authorization and lifecycle cleanup.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const knowledgeResultMaxBytes = 16 << 20

type knowledgeResultRow struct {
	CallID         string `gorm:"primaryKey;size:128"`
	ExpiresAt      int64  `gorm:"index"`
	Ciphertext     []byte
	BindingVersion int
	AttemptID      uint
	ObservationTag string `gorm:"size:64"`
}

func (knowledgeResultRow) TableName() string { return "platform_knowledge_results" }

type knowledgeResultKey struct {
	ID       string `gorm:"primaryKey;size:32"`
	Verifier string `gorm:"size:64"`
}

func (knowledgeResultKey) TableName() string { return "platform_knowledge_result_keys" }

type knowledgeResultVault struct {
	db      *gorm.DB
	aead    cipher.AEAD
	sealKey []byte
	ttl     time.Duration
	now     func() time.Time
}

func newKnowledgeResultVault(db *gorm.DB, key []byte, ttl time.Duration) (*knowledgeResultVault, error) {
	if db == nil || len(key) != 32 || ttl <= 0 || ttl > 24*time.Hour {
		return nil, ErrInvalid
	}
	// GORM can interpolate BLOB contents into SQL logs, including on errors.
	// Suppress SQL logging for every operation on this sensitive store.
	db = db.Session(&gorm.Session{Logger: logger.Discard})
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if err = db.AutoMigrate(&knowledgeResultKey{}, &knowledgeResultRow{}); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("platform-knowledge-result-key-v1"))
	want := hex.EncodeToString(mac.Sum(nil))
	err = db.Transaction(func(tx *gorm.DB) error {
		var existing knowledgeResultKey
		err := tx.First(&existing, "id = ?", "v1").Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var n int64
			if err := tx.Model(&knowledgeResultRow{}).Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return ErrInvalid
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&knowledgeResultKey{ID: "v1", Verifier: want}).Error; err != nil {
				return err
			}
			if err := tx.First(&existing, "id = ?", "v1").Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !hmac.Equal([]byte(existing.Verifier), []byte(want)) {
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seal := hmac.New(sha256.New, key)
	_, _ = seal.Write([]byte("platform-knowledge-response-seal-v1"))
	return &knowledgeResultVault{db: db, aead: aead, sealKey: seal.Sum(nil), ttl: ttl, now: time.Now}, nil
}

func knowledgeResultAAD(row Call, expires int64) ([]byte, error) {
	return common.Marshal([]any{"knowledge-result-v1", row.CallID, row.InstanceID, row.ActorID, row.AccountID, row.OriginProductID, row.CredentialID, row.CredentialKind, row.TaskID, row.Model, row.WireOperation, row.WireMode, row.Digest, row.Evidence, expires})
}

func knowledgeResultStoredAAD(row Call, stored knowledgeResultRow) ([]byte, error) {
	base, err := knowledgeResultAAD(row, stored.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if stored.BindingVersion == 0 {
		return base, nil
	}
	if stored.BindingVersion != 1 || stored.AttemptID == 0 || len(stored.ObservationTag) != 64 {
		return nil, ErrInvalid
	}
	return common.Marshal([]any{"knowledge-result-bound-v1", base, stored.AttemptID, stored.ObservationTag})
}

func (v *knowledgeResultVault) capture(ctx context.Context, callID string, result []byte) error {
	if v == nil || !stableID.MatchString(callID) || len(result) == 0 || len(result) > knowledgeResultMaxBytes {
		return ErrInvalid
	}
	return v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Call{}).Where("call_id = ?", callID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return err
		}
		var row Call
		if err := tx.First(&row, "call_id = ?", callID).Error; err != nil {
			return err
		}
		var usage metering.Usage
		if row.Status != "UNKNOWN" || row.EvidenceConflict || common.Unmarshal([]byte(row.Evidence), &usage) != nil || usage.Version != "metered_v2" || usage.State != "confirmed" || usage.Operation != row.Operation || !((row.WireOperation == "embeddings.create" && row.Operation == "embedding") || (row.WireOperation == "rerank.create" && row.Operation == "rerank")) {
			return ErrPending
		}
		expires := v.now().Add(v.ttl).UnixMilli()
		aad, err := knowledgeResultAAD(row, expires)
		if err != nil {
			return err
		}
		nonce := make([]byte, v.aead.NonceSize())
		if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
			return err
		}
		item := knowledgeResultRow{CallID: callID, ExpiresAt: expires, Ciphertext: v.aead.Seal(nonce, nonce, result, aad)}
		return tx.Create(&item).Error
	})
}

func (v *knowledgeResultVault) readSettled(ctx context.Context, callID string) ([]byte, error) {
	if v == nil || !stableID.MatchString(callID) {
		return nil, ErrInvalid
	}
	var result []byte
	err := v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Call{}).Where("call_id = ?", callID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return err
		}
		var row Call
		if err := tx.First(&row, "call_id = ?", callID).Error; err != nil {
			return err
		}
		if row.Status != "SETTLED" || row.EvidenceConflict || row.Evidence == "" {
			return ErrPending
		}
		var stored knowledgeResultRow
		if err := tx.First(&stored, "call_id = ?", callID).Error; err != nil {
			return err
		}
		if stored.ExpiresAt <= v.now().UnixMilli() || len(stored.Ciphertext) < v.aead.NonceSize() {
			return ErrPending
		}
		aad, err := knowledgeResultStoredAAD(row, stored)
		if err != nil {
			return err
		}
		result, err = v.aead.Open(nil, stored.Ciphertext[:v.aead.NonceSize()], stored.Ciphertext[v.aead.NonceSize():], aad)
		if err != nil {
			return ErrInvalid
		}
		return nil
	})
	return result, err
}

// purgeExpired clears only payloads, retaining their unique call tombstones.
// A future integration must schedule this with a bounded recurring worker.
func (v *knowledgeResultVault) purgeExpired(ctx context.Context, limit int, after string) (int64, string, error) {
	if v == nil || limit < 1 || limit > 1000 {
		return 0, after, ErrInvalid
	}
	now := v.now().UnixMilli()
	var rows []knowledgeResultRow
	if err := v.db.WithContext(ctx).Select("call_id", "expires_at").Where("expires_at <= ? AND ciphertext IS NOT NULL AND call_id > ?", now, after).Order("call_id").Limit(limit).Find(&rows).Error; err != nil {
		return 0, after, ErrPending
	}
	var count int64
	var problem error
	for _, item := range rows {
		after = item.CallID
		err := v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&Call{}).Where("call_id = ?", item.CallID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
			var row Call
			if err := tx.First(&row, "call_id = ?", item.CallID).Error; err != nil {
				return err
			}
			var stored knowledgeResultRow
			if err := tx.First(&stored, "call_id = ? AND expires_at = ?", item.CallID, item.ExpiresAt).Error; err != nil {
				return err
			}
			item = stored
			if len(item.Ciphertext) < v.aead.NonceSize() {
				return ErrInvalid
			}
			aad, err := knowledgeResultStoredAAD(row, item)
			if err != nil {
				return err
			}
			if _, err = v.aead.Open(nil, item.Ciphertext[:v.aead.NonceSize()], item.Ciphertext[v.aead.NonceSize():], aad); err != nil {
				return ErrInvalid
			}
			saved := tx.Model(&knowledgeResultRow{}).Where("call_id = ? AND expires_at = ? AND ciphertext = ? AND COALESCE(binding_version, 0) = ? AND COALESCE(attempt_id, 0) = ? AND COALESCE(observation_tag, '') = ?", item.CallID, item.ExpiresAt, item.Ciphertext, item.BindingVersion, item.AttemptID, item.ObservationTag).Update("ciphertext", nil)
			if saved.Error != nil {
				return saved.Error
			}
			count += saved.RowsAffected
			return nil
		})
		if err != nil {
			problem = ErrPending
		}
	}
	return count, after, problem
}
