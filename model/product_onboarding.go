package model

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting"
	"gorm.io/gorm"
)

// Both original registration and first central entry use the product's policy.
// Existing mappings never run this initializer again.
func initializeNewUserPolicy(user *User) {
	user.Quota = common.QuotaForNewUser
	user.AffCode = common.GetRandomString(4)
	if user.Setting == "" {
		user.SetSetting(dto.UserSetting{SidebarModules: generateDefaultSidebarConfigForRole(user.Role)})
	}
}

// CompleteProductJITOnboarding runs only for a durable, newly created JIT user,
// after the central identity binding has succeeded. The signup award is an
// idempotent outbox; default-key creation and completion are one SQL commit.
func CompleteProductJITOnboarding(ctx context.Context, userID int) error {
	var user User
	if e := DB.WithContext(ctx).First(&user, userID).Error; e != nil {
		return e
	}
	if !user.ProductJITPending {
		return nil
	}
	if user.IdentitySource != UniHubIdentitySource || !user.ProductRegistrationPending {
		return ErrProductFundingConflict
	}
	if e := ActivateRegisteredProductFunding(ctx, &user); e != nil {
		return e
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current User
		if e := lockForUpdate(tx).First(&current, userID).Error; e != nil {
			return e
		}
		if !current.ProductJITPending {
			return nil
		}
		if !current.ProductRegistrationPending || current.IdentitySource != UniHubIdentitySource {
			return ErrProductFundingConflict
		}
		if constant.GenerateDefaultToken {
			key, e := common.GenerateKey()
			if e != nil {
				return e
			}
			token := Token{UserId: userID, Name: current.Username + "的初始令牌", Key: key, CreatedTime: common.GetTimestamp(), AccessedTime: common.GetTimestamp(), ExpiredTime: -1, RemainQuota: 500000, UnlimitedQuota: true, ModelLimitsEnabled: false}
			if setting.DefaultUseAutoGroup {
				token.Group = "auto"
			}
			if e = tx.Create(&token).Error; e != nil {
				return e
			}
		}
		return tx.Model(&current).Updates(map[string]any{"product_registration_pending": false, "product_jit_pending": false}).Error
	})
}
