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
)

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
	if e := DB.First(&b, "sid = ? AND user_id = ?", sid, userID).Error; e != nil {
		return b, "", userkit.ErrDenied
	}
	instance := c.InstanceID
	if instance == "" {
		instance = "default"
	}
	if !c.Enabled || b.ExpiresAt <= time.Now().Unix() || b.Issuer != c.PublicOrigin || b.ProductID != c.ProductID || b.InstanceID != instance {
		return b, "", userkit.ErrDenied
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
	return b, string(plain), nil
}

// PurgeUniHubSessionBindings removes expired/revoked encrypted proofs. No
// expired binding is ever decryptable through OpenUniHubSession before cleanup.
func PurgeUniHubSessionBindings(now int64) error {
	return DB.Where("expires_at <= ? OR sid NOT IN (?)", now, DB.Model(&UserSession{}).Select("sid").Where("status = ? AND expires_at > ?", UserSessionStatusActive, now)).Delete(&UniHubSessionBinding{}).Error
}
