package model

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
)

var ErrUniHubSessionBindingAbsentOrExpired = errors.New("unihub session binding absent or expired")

type UniHubProvisioner struct{}

func (UniHubProvisioner) Ensure(ctx context.Context, a userkit.Access) (userkit.LocalUser, error) {
	if e := ctx.Err(); e != nil {
		return userkit.LocalUser{}, e
	}
	user, e := ResolveUniHubProjection(platformaccount.Identity{Issuer: a.Issuer, User: platformaccount.User{ID: a.Subject, Name: a.User.Name, Email: a.User.Email}})
	if e != nil {
		return userkit.LocalUser{}, e
	}
	return userkit.LocalUser{ID: strconv.Itoa(user.Id)}, nil
}

// No plaintext bearer or browser-readable proof is stored. The AAD binds every
// identity dimension, including SID; moving a ciphertext to another SID fails.
type UniHubSessionBinding struct {
	SID        string `gorm:"column:sid;type:varchar(64);primaryKey" json:"-"`
	UserID     int    `gorm:"not null" json:"-"`
	Issuer     string `gorm:"type:varchar(255);not null" json:"-"`
	Subject    string `gorm:"type:varchar(128);not null" json:"-"`
	ProductID  string `gorm:"type:varchar(128);not null" json:"-"`
	InstanceID string `gorm:"type:varchar(128);not null" json:"-"`
	ExpiresAt  int64  `gorm:"not null;index" json:"-"`
	Ciphertext string `gorm:"type:text;not null" json:"-"`
}

func (b UniHubSessionBinding) aad() []byte {
	raw, _ := common.Marshal([]any{"unihub-session-binding/v1", b.ProductID, b.InstanceID, b.Issuer, b.Subject, b.UserID, b.SID, b.ExpiresAt})
	return raw
}
func bindingAEAD() (cipher.AEAD, error) {
	if len(common.SessionSecret) < 16 {
		return nil, errors.New("session binding key unavailable")
	}
	h := hmac.New(sha256.New, []byte(common.SessionSecret))
	h.Write([]byte("llmrouter/unihub/session-proof/aes-gcm/v1"))
	block, e := aes.NewCipher(h.Sum(nil))
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func BindUniHubSession(sid string, userID int, a userkit.Access, bearer string, expiresAt int64) error {
	if expiresAt > a.ExpiresAt {
		expiresAt = a.ExpiresAt
	}
	if expiresAt <= time.Now().Unix() || bearer == "" {
		return userkit.ErrDenied
	}
	var session UserSession
	if e := DB.First(&session, "sid = ? AND user_id = ?", sid, userID).Error; e != nil {
		return e
	}
	if session.LoginMethod != "unihub" || session.ExpiresAt > expiresAt {
		return userkit.ErrDenied
	}
	b := UniHubSessionBinding{SID: sid, UserID: userID, Issuer: a.Issuer, Subject: a.Subject, ProductID: a.ProductID, InstanceID: a.InstanceID, ExpiresAt: expiresAt}
	aead, e := bindingAEAD()
	if e != nil {
		return e
	}
	nonce := make([]byte, aead.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	b.Ciphertext = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(bearer), b.aad()))
	return DB.Create(&b).Error
}
func OpenUniHubSession(sid string, userID int, c platformaccount.Config) (UniHubSessionBinding, string, error) {
	var b UniHubSessionBinding
	if DB == nil {
		return b, "", errors.New("session binding database unavailable")
	}
	if e := DB.First(&b, "sid = ?", sid).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return b, "", errors.Join(userkit.ErrDenied, ErrUniHubSessionBindingAbsentOrExpired)
		}
		return b, "", e
	}
	aead, e := bindingAEAD()
	if e != nil {
		return b, "", e
	}
	raw, e := base64.RawURLEncoding.DecodeString(b.Ciphertext)
	if e != nil || len(raw) < aead.NonceSize() {
		return b, "", userkit.ErrDenied
	}
	plain, e := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], b.aad())
	if e != nil {
		return b, "", userkit.ErrDenied
	}
	if b.SID != sid || b.UserID != userID {
		return b, "", userkit.ErrDenied
	}
	instance := c.InstanceID
	if instance == "" {
		instance = "default"
	}
	if !c.Enabled || b.Issuer != c.PublicOrigin || b.ProductID != c.ProductID || b.InstanceID != instance {
		return b, "", userkit.ErrDenied
	}
	// ExpiresAt is authenticated as AAD. Never call a modified or
	// unverifiable timestamp proof of expiration before verifying the tag.
	if b.ExpiresAt <= time.Now().Unix() {
		return b, "", errors.Join(userkit.ErrDenied, ErrUniHubSessionBindingAbsentOrExpired)
	}
	return b, string(plain), nil
}

// PurgeUniHubSessionBindings removes expired/revoked encrypted proofs. No
// expired binding is ever decryptable through OpenUniHubSession before cleanup.
func PurgeUniHubSessionBindings(now int64) error {
	if DB == nil {
		return errors.New("session binding database unavailable")
	}
	aead, err := bindingAEAD()
	if err != nil {
		return err
	}
	var candidates []UniHubSessionBinding
	if err := DB.Where("expires_at <= ? OR sid NOT IN (?)", now, DB.Model(&UserSession{}).Select("sid").Where("status = ? AND expires_at > ?", UserSessionStatusActive, now)).Find(&candidates).Error; err != nil {
		return err
	}
	for _, candidate := range candidates {
		err := DB.Transaction(func(tx *gorm.DB) error {
			// SQLite has no row locks; take its writer lock before reading.
			if tx.Dialector.Name() == "sqlite" {
				if err := tx.Model(&UniHubSessionBinding{}).Where("sid = ?", candidate.SID).UpdateColumn("expires_at", gorm.Expr("expires_at")).Error; err != nil {
					return err
				}
			}
			var session UserSession
			sessionErr := lockForUpdate(tx).First(&session, "sid = ?", candidate.SID).Error
			if sessionErr != nil && !errors.Is(sessionErr, gorm.ErrRecordNotFound) {
				return sessionErr
			}
			var current UniHubSessionBinding
			if err := lockForUpdate(tx).First(&current, "sid = ?", candidate.SID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			// Go equality is byte-exact, unlike a MySQL text comparison with a
			// case-insensitive collation. Replacements are not this snapshot.
			if current != candidate {
				return nil
			}
			raw, err := base64.RawURLEncoding.DecodeString(current.Ciphertext)
			if err != nil || len(raw) < aead.NonceSize() {
				return nil
			}
			if _, err = aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], current.aad()); err != nil {
				return nil
			}
			if current.ExpiresAt > now && (sessionErr != nil || session.SID != current.SID || (session.Status == UserSessionStatusActive && session.ExpiresAt > now && session.RevokedAt == 0)) {
				return nil
			}
			return tx.Delete(&current).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}
