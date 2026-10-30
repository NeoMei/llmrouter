package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// ValidateCentralCredentialSessionTx fences a credential mutation to the exact
// authenticated session. The caller must verify the central source before the
// transaction; this guard does not replace live source or step-up verification.
func ValidateCentralCredentialSessionTx(tx *gorm.DB, userID int, sid string, sessionVersion, userAuthVersion int64) error {
	if tx == nil || userID <= 0 || sid == "" || sessionVersion <= 0 || userAuthVersion <= 0 {
		return ErrNativeCredentialUnverified
	}
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error; err != nil {
			return err
		}
	}
	var user User
	if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
		return err
	}
	var session UserSession
	if err := lockForUpdate(tx).First(&session, "sid = ? AND user_id = ?", sid, userID).Error; err != nil {
		return err
	}
	if session.AuthRealm != CentralAuthRealm || session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() || session.Version != sessionVersion || session.UserAuthVersion != userAuthVersion {
		return ErrNativeCredentialUnverified
	}
	var state CentralAuthState
	if err := lockForUpdate(tx).First(&state, "user_id = ?", userID).Error; err != nil {
		return err
	}
	if !state.Active || state.BindingID == "" || state.AuthVersion != userAuthVersion {
		return ErrNativeCredentialUnverified
	}
	if session.LoginMethod == "central_credential" {
		var source CentralSessionCredential
		if err := lockForUpdate(tx).First(&source, "sid = ? AND user_id = ?", sid, userID).Error; err != nil {
			return err
		}
		var proof CentralLoginCredential
		if err := lockForUpdate(tx).First(&proof, "user_id = ? AND kind = ?", userID, source.Kind).Error; err != nil {
			return err
		}
		value, err := centralStoredCredential(tx, &user, source.Kind)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(value))
		if value == "" || source.Digest != hex.EncodeToString(digest[:]) || proof.Digest != source.Digest || proof.BindingID != state.BindingID || proof.AuthVersion != state.AuthVersion {
			return ErrNativeCredentialUnverified
		}
	}
	return nil
}
