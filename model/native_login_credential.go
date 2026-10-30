package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NativeLoginCredential records only bindings explicitly issued under a native
// session. There is no backfill from existing provider IDs or identity mappings.
type NativeLoginCredential struct {
	UserID int    `gorm:"primaryKey"`
	Kind   string `gorm:"primaryKey;size:128"`
	Digest string `gorm:"size:64;not null"`
}

func SetNativeLoginCredentialTx(tx *gorm.DB, userID int, kind, value, realm string) error {
	var user User
	if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
		return err
	}
	if user.FundingRealm != NativeFundingLocalOnly {
		if realm != "" {
			return ErrNativeCredentialUnverified
		}
		return nil
	}
	if realm != NativeFundingLocalOnly || !user.NativePassword || user.FundingRealmEvidence == "" || (value != "" && user.Status != common.UserStatusEnabled) {
		return ErrNativeCredentialUnverified
	}
	if kind == "" || len(kind) > 128 {
		return ErrNativeCredentialUnverified
	}
	if tx.Migrator().HasTable(&CentralLoginCredential{}) {
		var central CentralLoginCredential
		err := tx.Where("user_id = ? AND kind = ?", userID, kind).First(&central).Error
		if err == nil {
			return ErrNativeCredentialUnverified
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else if os.Getenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED") == "true" {
		return ErrNativeCredentialUnverified
	}
	if value == "" {
		return tx.Where("user_id = ? AND kind = ?", userID, kind).Delete(&NativeLoginCredential{}).Error
	}
	sum := sha256.Sum256([]byte(value))
	proof := NativeLoginCredential{UserID: userID, Kind: kind, Digest: hex.EncodeToString(sum[:])}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "kind"}}, DoUpdates: clause.AssignmentColumns([]string{"digest"})}).Create(&proof).Error
}

// Call only after the actual provider/passkey authentication succeeded and the
// current binding resolved this owner. An absent record never grants native scope.
func IsNativeLoginCredential(ctx context.Context, userID int, kind, value string) (bool, error) {
	var user User
	if err := DB.WithContext(ctx).First(&user, userID).Error; err != nil {
		return false, err
	}
	if user.FundingRealm != NativeFundingLocalOnly {
		return false, nil
	}
	if user.Status != common.UserStatusEnabled {
		return false, ErrNativeCredentialUnverified
	}
	if _, err := NativeRealmUser(ctx, userID); err != nil {
		return false, err
	}
	var proof NativeLoginCredential
	if err := DB.WithContext(ctx).Where("user_id = ? AND kind = ?", userID, kind).First(&proof).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrNativeCredentialUnverified
		}
		return false, err
	}
	sum := sha256.Sum256([]byte(value))
	if value == "" || proof.Digest != hex.EncodeToString(sum[:]) {
		return false, ErrNativeCredentialUnverified
	}
	return true, nil
}
