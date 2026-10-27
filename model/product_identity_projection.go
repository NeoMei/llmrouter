package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"strconv"
)

// ReconcileProductIdentity refreshes only a previously proven identity's cached
// role/status. It cannot bind by profile similarity or change native credentials.
func ReconcileProductIdentity(ctx context.Context, user *User) error {
	if !platformaccount.RuntimeEnabled() || user == nil || user.Id <= 0 || user.IdentitySource != UniHubIdentitySource {
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
	if e = DB.Model(&User{}).Where("id = ? AND auth_version = ?", user.Id, user.AuthVersion).Updates(map[string]any{"role": v.ProductRole, "status": status}).Error; e != nil {
		return e
	}
	user.Role = v.ProductRole
	user.Status = status
	return updateUserCache(*user)
}
