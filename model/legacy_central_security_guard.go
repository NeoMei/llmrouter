package model

import (
	"errors"

	"gorm.io/gorm"
)

// ErrLegacyCentralSecurityConflict protects a mixed identity until its local
// and central authentication versions have an explicit migration contract.
var ErrLegacyCentralSecurityConflict = errors.New("local security settings are unavailable for this mixed account")

// The caller holds the owner row lock. Central state and credentials are
// created under that same lock. Inactive or historical evidence still counts.
func guardLegacyCentralSecurityTx(tx *gorm.DB, userID int, realm string) error {
	if realm != "" {
		return nil
	}
	var count int64
	if err := tx.Model(&UserSession{}).Where("user_id = ? AND auth_realm = ?", userID, CentralAuthRealm).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrLegacyCentralSecurityConflict
	}
	for _, table := range []interface{}{&CentralAuthState{}, &CentralLoginCredential{}, &CentralSessionCredential{}} {
		count = 0
		if err := tx.Model(table).Where("user_id = ?", userID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrLegacyCentralSecurityConflict
		}
	}
	return nil
}
