package model

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ChangePasskeyForSession commits a local passkey change with its authenticated
// session's exact versions. A nil credential deletes the existing passkey.
func ChangePasskeyForSession(userID int, sid string, sessionVersion, userVersion int64, realm string, credential *PasskeyCredential) (*UserSession, error) {
	if userID <= 0 || sid == "" || sessionVersion <= 0 || userVersion <= 0 || (realm != "" && realm != NativeFundingLocalOnly) || (credential != nil && (credential.UserID != userID || credential.CredentialID == "" || credential.PublicKey == "")) {
		return nil, ErrUserSessionInvalid
	}
	var session UserSession
	var revoked []UserSession
	deadline := userSessionCacheDeadline()
	err := DB.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error; err != nil {
				return err
			}
		}
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled || user.AuthVersion != userVersion {
			return ErrUserSessionInactive
		}
		if err := lockForUpdate(tx).Where("sid = ? AND user_id = ?", sid, userID).First(&session).Error; err != nil {
			return err
		}
		if session.AuthRealm != realm || session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() || session.Version != sessionVersion || session.UserAuthVersion != userVersion {
			return ErrUserSessionInactive
		}
		if err := guardLegacyCentralSecurityTx(tx, userID, realm); err != nil {
			return err
		}
		if realm == "" {
			if tx.Migrator().HasTable(&CentralLoginCredential{}) {
				var central CentralLoginCredential
				err := tx.Where("user_id = ? AND kind = ?", userID, "passkey").First(&central).Error
				if err == nil || !errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrNativeCredentialUnverified
				}
			} else if os.Getenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED") == "true" {
				return ErrNativeCredentialUnverified
			}
		}
		if credential != nil {
			if err := SetNativeLoginCredentialTx(tx, userID, "passkey", credential.CredentialID, realm); err != nil {
				return err
			}
			if err := upsertPasskeyCredentialWithTx(tx, credential); err != nil {
				return err
			}
		} else {
			if err := SetNativeLoginCredentialTx(tx, userID, "passkey", "", realm); err != nil {
				return err
			}
			var existing PasskeyCredential
			if err := lockForUpdate(tx).Where("user_id = ?", userID).First(&existing).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrPasskeyNotFound
				}
				return err
			}
			result := tx.Unscoped().Delete(&existing)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrPasskeyNotFound
			}
		}
		next, err := IncrementUserAuthVersionWithTx(tx, userID)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		result := tx.Model(&UserSession{}).Where("sid = ? AND user_id = ? AND status = ? AND version = ? AND user_auth_version = ?", sid, userID, UserSessionStatusActive, sessionVersion, userVersion).Updates(map[string]interface{}{"version": sessionVersion + 1, "user_auth_version": next, "last_active_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUserSessionInactive
		}
		session.Version, session.UserAuthVersion, session.LastActiveAt = sessionVersion+1, next, now
		query := tokenRealmQuery(tx, []string{realm}).Where("user_id = ? AND sid <> ? AND status = ? AND expires_at > ?", userID, sid, UserSessionStatusActive, now)
		if err := lockForUpdate(query).Order("sid").Find(&revoked).Error; err != nil {
			return err
		}
		if len(revoked) == 0 {
			return nil
		}
		sids := make([]string, len(revoked))
		for i := range revoked {
			sids[i] = revoked[i].SID
		}
		return tx.Model(&UserSession{}).Where("sid IN ? AND status = ?", sids, UserSessionStatusActive).Updates(map[string]interface{}{"status": UserSessionStatusRevoked, "revoked_at": now, "revoked_reason": "passkey_changed"}).Error
	})
	if err != nil {
		return nil, err
	}
	if err := writeUserSessionCache(session.cacheEntry(), deadline); err != nil {
		if errors.Is(err, errUserSessionCacheObservationStale) {
			if confirmErr := confirmUserSessionActiveSnapshot(&session); confirmErr != nil {
				return nil, confirmErr
			}
		} else {
			return nil, err
		}
	}
	if err := PublishUserAuthCache(userID); err != nil {
		return nil, err
	}
	for i := range revoked {
		revoked[i].Status, revoked[i].RevokedAt, revoked[i].RevokedReason = UserSessionStatusRevoked, session.LastActiveAt, "passkey_changed"
		if err := writeUserSessionCache(revoked[i].cacheEntry(), time.Time{}); err != nil {
			common.SysLog(fmt.Sprintf("failed to finalize passkey session revoke tombstone: %v", err))
		}
	}
	return &session, nil
}
