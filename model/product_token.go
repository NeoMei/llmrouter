package model

import (
	"context"
	"errors"
	"gorm.io/gorm"
)

// All funding sources share the same SQL Key limits after an account cutover.
// In particular an old Redis snapshot must not admit a simultaneous subscription
// request after the central-wallet path has already reserved that same limit.
func managedProductToken(id int) (bool, error) {
	if DB == nil || !DB.Migrator().HasTable(&ProductFundingAccount{}) {
		return false, nil
	}
	var token Token
	if err := DB.Select("id", "user_id").First(&token, id).Error; err != nil {
		return false, err
	}
	state, err := ProductFundingState(context.Background(), token.UserId)
	if err != nil {
		return false, err
	}
	if state == "local" {
		return false, nil
	}
	if state != "central" {
		return true, ErrProductFundingPending
	}
	return true, nil
}

func reserveManagedProductToken(id, quota int) (bool, error) {
	err := DB.Transaction(func(tx *gorm.DB) error { return productTokenDelta(tx, id, quota) })
	if errors.Is(err, ErrOCRInsufficient) {
		return false, nil
	}
	return err == nil, err
}

func mutateManagedProductToken(id int, key string, delta int) error {
	err := DB.Transaction(func(tx *gorm.DB) error { return productTokenApplyDelta(tx, id, delta, true) })
	if err == nil {
		_ = invalidateTokenCacheForMutation(key)
	}
	return err
}
