package platformbilling

// The vault backs certified relay capture. Its result-read route remains
// separately disabled by default and expiry cleanup is unscheduled.

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
	"regexp"
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
	KeyID          string `gorm:"size:32"`
}

func (knowledgeResultRow) TableName() string { return "platform_knowledge_results" }

type knowledgeResultKey struct {
	ID       string `gorm:"primaryKey;size:32"`
	Verifier string `gorm:"size:64"`
}

func (knowledgeResultKey) TableName() string { return "platform_knowledge_result_keys" }

type knowledgeResultActive struct {
	ID    string `gorm:"primaryKey;size:32"`
	KeyID string `gorm:"size:32"`
}

func (knowledgeResultActive) TableName() string { return "platform_knowledge_result_active" }

type knowledgeResultVault struct {
	db         *gorm.DB
	active     string
	keys       map[string]cipher.AEAD
	digestKeys map[string][]byte
	aead       cipher.AEAD
	sealKey    []byte
	ttl        time.Duration
	now        func() time.Time
}

var knowledgeKeyID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

func newKnowledgeResultVault(db *gorm.DB, key []byte, ttl time.Duration) (*knowledgeResultVault, error) {
	return newKnowledgeResultVaultWithKeys(db, map[string][]byte{"v1": key}, "v1", ttl)
}

// Keys are deployment secrets shared by all nodes, never stored in the database.
// Every recorded version must remain supplied; this deliberately does not offer
// key retirement while old ciphertext, tombstones or backups may need it.
func newKnowledgeResultVaultWithKeys(db *gorm.DB, keys map[string][]byte, active string, ttl time.Duration) (*knowledgeResultVault, error) {
	if db == nil || ttl <= 0 || ttl > 24*time.Hour || !knowledgeKeyID.MatchString(active) || len(keys) == 0 || len(keys) > 32 {
		return nil, ErrInvalid
	}
	db = db.Session(&gorm.Session{Logger: logger.Discard})
	aeads := make(map[string]cipher.AEAD, len(keys))
	digests := make(map[string][]byte, len(keys))
	verifiers := make(map[string]string, len(keys))
	seen := make(map[string]bool, len(keys))
	for id, key := range keys {
		if !knowledgeKeyID.MatchString(id) || len(key) != 32 {
			return nil, ErrInvalid
		}
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte("platform-knowledge-result-key-v1"))
		fingerprint := hex.EncodeToString(mac.Sum(nil))
		if seen[fingerprint] {
			return nil, ErrInvalid
		}
		seen[fingerprint] = true
		verifiers[id] = fingerprint
		digestMAC := hmac.New(sha256.New, key)
		_, _ = digestMAC.Write([]byte("platform-knowledge-request-digest-key-v1"))
		digests[id] = digestMAC.Sum(nil)
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		aeads[id] = aead
	}
	selected, ok := aeads[active]
	if !ok {
		return nil, ErrInvalid
	}
	if err := db.AutoMigrate(&knowledgeResultKey{}, &knowledgeResultRow{}, &knowledgeResultActive{}); err != nil {
		return nil, err
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		// Legacy rows without an active marker can only start with their original
		// v1 writer. The existing fingerprint is verified below.
		var marker knowledgeResultActive
		markerErr := tx.First(&marker, "id = ?", "active").Error
		if errors.Is(markerErr, gorm.ErrRecordNotFound) {
			var count int64
			if err := tx.Model(&knowledgeResultKey{}).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 && active != "v1" {
				return ErrInvalid
			}
		} else if markerErr != nil {
			return markerErr
		}
		// A single serialized row binds all nodes to one writer generation.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&knowledgeResultActive{ID: "active", KeyID: active}).Error; err != nil {
			return err
		}
		if err := tx.Model(&knowledgeResultActive{}).Where("id = ?", "active").UpdateColumn("key_id", gorm.Expr("key_id")).Error; err != nil {
			return err
		}
		var current knowledgeResultActive
		if err := tx.First(&current, "id = ?", "active").Error; err != nil || current.KeyID != active {
			return ErrInvalid
		}
		var registered []knowledgeResultKey
		if err := tx.Find(&registered).Error; err != nil {
			return err
		}
		known := make(map[string]bool, len(registered))
		for _, existing := range registered {
			known[existing.ID] = true
			want, present := verifiers[existing.ID]
			if !present || !hmac.Equal([]byte(existing.Verifier), []byte(want)) {
				return ErrInvalid
			}
		}
		// Existing data without its original key fingerprint must never be adopted.
		if len(registered) == 0 {
			var n int64
			if err := tx.Model(&knowledgeResultRow{}).Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return ErrInvalid
			}
		}
		var used []struct{ KeyID string }
		if err := tx.Model(&knowledgeResultRow{}).Select("DISTINCT COALESCE(key_id, '') AS key_id").Scan(&used).Error; err != nil {
			return err
		}
		for _, item := range used {
			id := item.KeyID
			if id == "" {
				id = "v1"
			}
			// Never establish a fresh fingerprint for a version that already
			// protects a result: a missing verifier cannot authenticate its key.
			if !known[id] {
				return ErrInvalid
			}
		}
		for id, verifier := range verifiers {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&knowledgeResultKey{ID: id, Verifier: verifier}).Error; err != nil {
				return err
			}
			var saved knowledgeResultKey
			if err := tx.First(&saved, "id = ?", id).Error; err != nil || !hmac.Equal([]byte(saved.Verifier), []byte(verifier)) {
				return ErrInvalid
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seal := hmac.New(sha256.New, keys[active])
	_, _ = seal.Write([]byte("platform-knowledge-response-seal-v1"))
	return &knowledgeResultVault{db: db, active: active, keys: aeads, digestKeys: digests, aead: selected, sealKey: seal.Sum(nil), ttl: ttl, now: time.Now}, nil
}

// activateKey is an explicit administrative transition. Before invoking it,
// operators must stop old binaries and ensure every live node has the full ring.
func (v *knowledgeResultVault) activateKey(ctx context.Context, next string) error {
	if v == nil || next == v.active || v.keys[next] == nil {
		return ErrInvalid
	}
	return v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&knowledgeResultActive{}).Where("id = ? AND key_id = ?", "active", v.active).Update("key_id", next)
		if res.Error != nil || res.RowsAffected != 1 {
			return ErrInvalid
		}
		return nil
	})
}

// checkWriter locks the generation row for the duration of the write transaction.
func (v *knowledgeResultVault) checkWriter(tx *gorm.DB) error {
	if err := tx.Model(&knowledgeResultActive{}).Where("id = ?", "active").UpdateColumn("key_id", gorm.Expr("key_id")).Error; err != nil {
		return err
	}
	var current knowledgeResultActive
	if err := tx.First(&current, "id = ?", "active").Error; err != nil || current.KeyID != v.active {
		return ErrInvalid
	}
	return nil
}

func (v *knowledgeResultVault) resultAEAD(row knowledgeResultRow) (cipher.AEAD, error) {
	id := row.KeyID
	if id == "" {
		id = "v1"
	}
	aead := v.keys[id]
	if aead == nil {
		return nil, ErrInvalid
	}
	return aead, nil
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
	if (stored.BindingVersion != 1 && stored.BindingVersion != 2) || stored.AttemptID == 0 || len(stored.ObservationTag) != 64 {
		return nil, ErrInvalid
	}
	if stored.BindingVersion == 1 {
		return common.Marshal([]any{"knowledge-result-bound-v1", base, stored.AttemptID, stored.ObservationTag})
	}
	// Version 2 additionally authenticates the reservation and exact settlement
	// intent. Status and charged are not fixed at capture time; the authorized
	// read must verify the settled receipt with the authoritative billing service.
	return common.Marshal([]any{"knowledge-result-bound-v2", base, stored.AttemptID, stored.ObservationTag,
		row.DigestVersion, row.WirePathModel, row.Operation, row.MediaSnapshot,
		row.RateSnapshot, row.RateID, row.Asset, row.FundingMode, row.FundingProductID,
		row.FundingSnapshot, row.BillingProfileVersion, row.Amount, row.ReservationQuantity,
		row.ReservationOutput, row.Intent})
}

func (v *knowledgeResultVault) capture(ctx context.Context, callID string, result []byte) error {
	if v == nil || !stableID.MatchString(callID) || len(result) == 0 || len(result) > knowledgeResultMaxBytes {
		return ErrInvalid
	}
	return v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := v.checkWriter(tx); err != nil {
			return err
		}
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
		item := knowledgeResultRow{CallID: callID, KeyID: v.active, ExpiresAt: expires, Ciphertext: v.aead.Seal(nonce, nonce, result, aad)}
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
		aead, err := v.resultAEAD(stored)
		if err != nil {
			return err
		}
		if stored.ExpiresAt <= v.now().UnixMilli() || len(stored.Ciphertext) < aead.NonceSize() {
			return ErrPending
		}
		aad, err := knowledgeResultStoredAAD(row, stored)
		if err != nil {
			return err
		}
		result, err = aead.Open(nil, stored.Ciphertext[:aead.NonceSize()], stored.Ciphertext[aead.NonceSize():], aad)
		if err != nil {
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
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
			aead, err := v.resultAEAD(item)
			if err != nil {
				return err
			}
			if len(item.Ciphertext) < aead.NonceSize() {
				return ErrInvalid
			}
			aad, err := knowledgeResultStoredAAD(row, item)
			if err != nil {
				return err
			}
			if _, err = aead.Open(nil, item.Ciphertext[:aead.NonceSize()], item.Ciphertext[aead.NonceSize():], aad); err != nil {
				return ErrInvalid
			}
			saved := tx.Model(&knowledgeResultRow{}).Where("call_id = ? AND expires_at = ? AND ciphertext = ? AND COALESCE(binding_version, 0) = ? AND COALESCE(attempt_id, 0) = ? AND COALESCE(observation_tag, '') = ? AND COALESCE(key_id, '') = ?", item.CallID, item.ExpiresAt, item.Ciphertext, item.BindingVersion, item.AttemptID, item.ObservationTag, item.KeyID).Update("ciphertext", nil)
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
