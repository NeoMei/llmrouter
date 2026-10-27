package model

import (
	"errors"
	"gorm.io/gorm"
)

// Every original journal admission shares the same user-row fence as funding
// import. An in-flight local admission is visible before cutover can commit.
func lockProductFundingAdmission(tx *gorm.DB, userID int) error {
	if !tx.Migrator().HasTable(&ProductFundingAccount{}) {
		return nil
	}
	var user User
	if err := lockForUpdate(tx).Select("id").First(&user, userID).Error; err != nil {
		return err
	}
	var a ProductFundingAccount
	err := tx.First(&a, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if a.State != "central" && a.State != "local" {
		return ErrProductFundingPending
	}
	return nil
}
