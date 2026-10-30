package model

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

const NativeFundingLocalOnly = "LOCAL_ONLY"

var ErrNativeFundingLocalOnly = errors.New("native funding ownership is permanently local")

// PinNativeFundingRealm is a trusted migration boundary, not authentication.
// The caller must supply an explicitly reviewed native cohort evidence reference;
// neither credentials, login methods nor identity mappings establish this policy.
// There is deliberately no online unpin/cutover operation or HTTP entry point.
func PinNativeFundingRealm(ctx context.Context, userID int, evidence string) error {
	if DB == nil || userID <= 0 || strings.TrimSpace(evidence) != evidence || evidence == "" || len(evidence) > 4096 {
		return ErrProductFundingConflict
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if user.ProductRegistrationPending || user.ProductJITPending {
			return ErrProductFundingPending
		}
		var account ProductFundingAccount
		err := tx.First(&account, "user_id = ?", userID).Error
		if err == nil && account.State != "local" {
			return ErrProductFundingConflict
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if user.FundingRealm != "" {
			if user.FundingRealm == NativeFundingLocalOnly && user.FundingRealmEvidence == evidence {
				return nil
			}
			return ErrProductFundingConflict
		}
		return tx.Model(&user).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": evidence}).Error
	})
}

// Every central funding ownership transition uses this same User row fence.
// Empty means the pre-migration policy, never evidence of native authentication.
func lockCentralFundingOwnerTx(tx *gorm.DB, userID int) (User, error) {
	var user User
	if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
		return user, err
	}
	if user.FundingRealm == NativeFundingLocalOnly {
		return user, ErrNativeFundingLocalOnly
	}
	if user.FundingRealm != "" {
		return user, ErrProductFundingConflict
	}
	return user, nil
}

// lockFundingManagementOwnerTx serializes a scoped administrative mutation with
// offline realm enrollment. Optional scope preserves internal legacy callers.
func lockFundingManagementOwnerTx(tx *gorm.DB, userID int, realm []string) error {
	if len(realm) == 0 {
		return nil
	}
	var user User
	if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
		return err
	}
	if (user.FundingRealm == NativeFundingLocalOnly) != (realm[0] == NativeFundingLocalOnly) {
		return ErrNativeCredentialUnverified
	}
	return nil
}
