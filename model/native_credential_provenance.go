package model

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	NativeCredentialRelayKey     = "relay_key"
	NativeCredentialSourceNative = "native"
)

var ErrNativeCredentialUnverified = errors.New("native credential provenance is unverified")

// NativeCredentialProvenance stores a server-issued credential binding. It is
// not exposed by any API and has no enrollment writer or automatic backfill.
// Empty/absent records are never native. The binding is an HMAC, never a key.
// This schema is not yet registered in startup migrations: deployment requires
// a reviewed enrollment and rollout/fencing plan.
type NativeCredentialProvenance struct {
	Kind         string `json:"-" gorm:"type:varchar(24);primaryKey"`
	CredentialID string `json:"-" gorm:"type:varchar(64);primaryKey"`
	UserID       int    `json:"-" gorm:"not null;index"`
	Source       string `json:"-" gorm:"type:varchar(24);not null"`
	EvidenceRef  string `json:"-" gorm:"type:text;not null"`
	AuthVersion  int64  `json:"-" gorm:"not null"`
	Binding      string `json:"-" gorm:"type:char(64);not null"`
}

func nativeCredentialKeyBinding(key []byte, p NativeCredentialProvenance, secret string) string {
	b, err := common.Marshal([]any{"llmrouter/native-key-provenance/v1", p.Kind, p.CredentialID, p.UserID, p.Source, p.EvidenceRef, p.AuthVersion, secret})
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckNativeRelayKeyProvenance is an offline foundation, not an authentication
// or authorization boundary. Call only after actual key authentication and
// independent enrollment, and never use its result to lift the current guards.
// No caller is wired in this change. Future use needs atomic admission/fencing,
// credential lifecycle coverage, and a reviewed HMAC-key rotation protocol.
func CheckNativeRelayKeyProvenance(ctx context.Context, db *gorm.DB, bindingKey []byte, userID, tokenID int, now int64) error {
	if ctx == nil || db == nil || len(bindingKey) < 32 || userID <= 0 || tokenID <= 0 || now <= 0 {
		return ErrNativeCredentialUnverified
	}
	var p NativeCredentialProvenance
	if err := db.WithContext(ctx).Where("kind = ? AND credential_id = ?", NativeCredentialRelayKey, strconv.Itoa(tokenID)).First(&p).Error; err != nil {
		return ErrNativeCredentialUnverified
	}
	if p.UserID != userID || p.Source != NativeCredentialSourceNative || p.EvidenceRef == "" || p.AuthVersion <= 0 {
		return ErrNativeCredentialUnverified
	}
	var user User
	if err := db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return ErrNativeCredentialUnverified
	}
	if user.Status != common.UserStatusEnabled || user.IdentitySource == ProductIdentityPending || user.ProductRegistrationPending || user.ProductJITPending || user.FundingRealm != NativeFundingLocalOnly || user.FundingRealmEvidence != p.EvidenceRef || user.AuthVersion != p.AuthVersion {
		return ErrNativeCredentialUnverified
	}
	var account ProductFundingAccount
	err := db.WithContext(ctx).Where("user_id = ?", userID).First(&account).Error
	if (err != nil && !errors.Is(err, gorm.ErrRecordNotFound)) || (err == nil && account.State != "local") {
		return ErrNativeCredentialUnverified
	}
	var token Token
	if err := db.WithContext(ctx).Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error; err != nil {
		return ErrNativeCredentialUnverified
	}
	if token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= now) || token.Key == "" || token.FundingAccountID != "" {
		return ErrNativeCredentialUnverified
	}
	expected := nativeCredentialKeyBinding(bindingKey, p, token.Key)
	if expected == "" || !hmac.Equal([]byte(expected), []byte(p.Binding)) {
		return ErrNativeCredentialUnverified
	}
	return nil
}
