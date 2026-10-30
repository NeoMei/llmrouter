package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
	"strconv"
)

// ReconcileProductIdentity refreshes only a previously proven identity's cached
// role/status. It cannot bind by profile similarity or change native credentials.
func ReconcileProductIdentity(ctx context.Context, user *User) error {
	if !platformaccount.RuntimeEnabled() || user == nil || user.Id <= 0 || user.IdentitySource != UniHubIdentitySource {
		return nil
	}
	var current User
	if e := DB.WithContext(ctx).First(&current, user.Id).Error; e != nil {
		return e
	}
	if current.FundingRealm == NativeFundingLocalOnly {
		*user = current
		return nil
	}
	v, e := platformaccount.IdentityForLocal(ctx, strconv.Itoa(user.Id))
	if e != nil {
		return e
	}
	if e = VerifyUniHubIdentityBinding(user.Id, v.Access.Issuer, v.Access.Subject); e != nil {
		return e
	}
	if v.AuthVersion != user.AuthVersion {
		return ErrUserAuthVersionConflict
	}
	status := common.UserStatusDisabled
	if (userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(user.Id)}, Access: v.Access}).HasPermission("product:use") {
		status = common.UserStatusEnabled
	}
	if v.ProductRole != 1 && v.ProductRole != 10 && v.ProductRole != 100 {
		return ErrUniHubProjectionInvalid
	}
	// Enrollment and central projection serialize on the same owner. A remote
	// response obtained before enrollment cannot overwrite native authority.
	e = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockForUpdate(tx).First(&current, user.Id).Error; e != nil {
			return e
		}
		if current.FundingRealm == NativeFundingLocalOnly {
			*user = current
			return nil
		}
		if current.AuthVersion != v.AuthVersion {
			return ErrUserAuthVersionConflict
		}
		if e := tx.Model(&current).Updates(map[string]any{"role": v.ProductRole, "status": status}).Error; e != nil {
			return e
		}
		current.Role = v.ProductRole
		current.Status = status
		*user = current
		return nil
	})
	if e != nil {
		return e
	}
	return updateUserCache(*user)
}
