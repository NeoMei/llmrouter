package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
)

var brokerSourceProofPattern = regexp.MustCompile(`^ubsp_[0-9a-f]{64}$`)

// ProductBrokerSessionProof is an opaque central source-session handle, never
// a browser credential. Every metadata field is authenticated with the SID.
type ProductBrokerSessionProof struct {
	SID        string `gorm:"column:sid;type:varchar(64);primaryKey" json:"-"`
	UserID     int    `gorm:"not null" json:"-"`
	Issuer     string `gorm:"type:varchar(255);not null" json:"-"`
	Subject    string `gorm:"type:varchar(128);not null" json:"-"`
	ProductID  string `gorm:"type:varchar(128);not null" json:"-"`
	InstanceID string `gorm:"type:varchar(128);not null" json:"-"`
	ClientID   string `gorm:"type:varchar(128);not null" json:"-"`
	ExpiresAt  int64  `gorm:"not null;index" json:"-"`
	Ciphertext string `gorm:"type:text;not null" json:"-"`
}

func (b ProductBrokerSessionProof) aad() []byte {
	raw, _ := common.Marshal([]any{"unihub-broker-source/v1", b.ProductID, b.InstanceID, b.ClientID, b.Issuer, b.Subject, b.UserID, b.SID, b.ExpiresAt})
	return raw
}
func brokerProofAEAD() (cipher.AEAD, error) {
	if len(common.SessionSecret) < 16 {
		return nil, errors.New("broker source key unavailable")
	}
	mac := hmac.New(sha256.New, []byte(common.SessionSecret))
	_, _ = mac.Write([]byte("llmrouter/unihub/broker-source/aes-gcm/v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// BindBrokerSourceProof is called only after verified central browser exchange
// and local SID creation. Existing rows cannot be replaced or retargeted.
func BindBrokerSourceProof(sid string, userID int, access userkit.Access, config platformaccount.Config, proof string, expires int64) error {
	if DB == nil || !brokerSourceProofPattern.MatchString(proof) || userID <= 0 || sid == "" || config.ClientID == "" || config.ProductID == "" || config.InstanceID == "" || access.Issuer != config.PublicOrigin || access.ProductID != config.ProductID || access.InstanceID != config.InstanceID || access.Subject == "" || access.User.ID != access.Subject || !access.Active || access.ExpiresAt <= time.Now().Unix() || expires <= time.Now().Unix() {
		return userkit.ErrDenied
	}
	var session UserSession
	if err := DB.First(&session, "sid = ? AND user_id = ?", sid, userID).Error; err != nil {
		return err
	}
	if session.LoginMethod != "product_broker" || session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() {
		return userkit.ErrDenied
	}
	if err := VerifyUniHubIdentityBinding(userID, access.Issuer, access.Subject); err != nil {
		return userkit.ErrDenied
	}
	b := ProductBrokerSessionProof{SID: sid, UserID: userID, Issuer: access.Issuer, Subject: access.Subject, ProductID: access.ProductID, InstanceID: access.InstanceID, ClientID: config.ClientID, ExpiresAt: min(expires, session.ExpiresAt)}
	aead, err := brokerProofAEAD()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	b.Ciphertext = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(proof), b.aad()))
	return DB.Create(&b).Error
}

func OpenBrokerSourceProof(sid string, userID int, subject string, config platformaccount.Config) (ProductBrokerSessionProof, string, error) {
	var b ProductBrokerSessionProof
	if DB == nil {
		return b, "", errors.New("broker source database unavailable")
	}
	if err := DB.First(&b, "sid = ?", sid).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return b, "", userkit.ErrDenied
		}
		return b, "", err
	}
	aead, err := brokerProofAEAD()
	if err != nil {
		return b, "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(b.Ciphertext)
	if err != nil || len(raw) < aead.NonceSize() {
		return b, "", errors.New("broker source proof unavailable")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], b.aad())
	if err != nil || !brokerSourceProofPattern.Match(plain) {
		return b, "", errors.New("broker source proof unavailable")
	}
	if b.SID != sid || b.UserID != userID || b.Issuer != config.PublicOrigin || b.ProductID != config.ProductID || b.InstanceID != config.InstanceID || b.ClientID != config.ClientID || b.Subject != subject {
		return b, "", errors.New("broker source identity unavailable")
	}
	if b.ExpiresAt <= time.Now().Unix() {
		return b, "", userkit.ErrDenied
	}
	var session UserSession
	if err := DB.First(&session, "sid = ? AND user_id = ?", sid, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return b, "", userkit.ErrDenied
		}
		return b, "", err
	}
	if session.LoginMethod != "product_broker" || session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() {
		return b, "", userkit.ErrDenied
	}
	if err := VerifyUniHubIdentityBinding(userID, b.Issuer, b.Subject); err != nil {
		return b, "", err
	}
	return b, string(plain), nil
}

func PurgeBrokerSourceProofs(now int64) error {
	if DB == nil {
		return errors.New("broker source database unavailable")
	}
	aead, err := brokerProofAEAD()
	if err != nil {
		return err
	}
	var candidates []ProductBrokerSessionProof
	if err := DB.Where("expires_at <= ? OR sid NOT IN (?)", now, DB.Model(&UserSession{}).Select("sid").Where("status = ? AND expires_at > ?", UserSessionStatusActive, now)).Find(&candidates).Error; err != nil {
		return err
	}
	for _, candidate := range candidates {
		err := DB.Transaction(func(tx *gorm.DB) error {
			// SQLite has no row-level FOR UPDATE. Acquire its writer lock before
			// reading the proof so a replacement cannot race with deletion.
			if tx.Dialector.Name() == "sqlite" {
				if err := tx.Model(&ProductBrokerSessionProof{}).Where("sid = ?", candidate.SID).UpdateColumn("expires_at", gorm.Expr("expires_at")).Error; err != nil {
					return err
				}
			}
			// Match session lifecycle lock ordering before taking the proof lock.
			var session UserSession
			sessionErr := lockForUpdate(tx).First(&session, "sid = ?", candidate.SID).Error
			if sessionErr != nil && !errors.Is(sessionErr, gorm.ErrRecordNotFound) {
				return sessionErr
			}
			var current ProductBrokerSessionProof
			if err := lockForUpdate(tx).First(&current, "sid = ?", candidate.SID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			// Compare bytes in Go, avoiding case-insensitive MySQL collations.
			if current != candidate {
				return nil
			}
			raw, err := base64.RawURLEncoding.DecodeString(current.Ciphertext)
			if err != nil || len(raw) < aead.NonceSize() {
				return nil
			}
			plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], current.aad())
			if err != nil || !brokerSourceProofPattern.Match(plain) {
				return nil
			}
			if current.ExpiresAt > now {
				// A missing row cannot be locked. Keep a live proof in case a
				// session is concurrently being created with the same SID.
				if sessionErr != nil || (session.Status == UserSessionStatusActive && session.RevokedAt == 0 && session.ExpiresAt > now) {
					return nil
				}
			}
			return tx.Delete(&current).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}
