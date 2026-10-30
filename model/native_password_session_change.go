package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrPasswordSessionMismatch = errors.New("current password is incorrect")

// ChangePasswordForSession verifies the current password and advances the
// exact authenticated local session in the same transaction as the change.
func ChangePasswordForSession(change *User, originalPassword, sid string, sessionVersion, userVersion int64, realm string) (*UserSession, error) {
	if change == nil || change.Id <= 0 || change.Password == "" || sid == "" || sessionVersion <= 0 || userVersion <= 0 || (realm != "" && realm != NativeFundingLocalOnly) {
		return nil, ErrUserSessionInvalid
	}
	userID := change.Id
	var session UserSession
	var revoked []UserSession
	deadline := userSessionCacheDeadline()
	err := DB.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error; err != nil {
				return err
			}
		}
		var current User
		if err := lockForUpdate(tx).First(&current, userID).Error; err != nil {
			return err
		}
		if current.Status != common.UserStatusEnabled || current.AuthVersion != userVersion {
			return ErrUserSessionInactive
		}
		if realm == NativeFundingLocalOnly && (current.FundingRealm != NativeFundingLocalOnly || current.FundingRealmEvidence == "" || !current.NativePassword || current.ProductRegistrationPending || current.ProductJITPending || current.IdentitySource == ProductIdentityPending) {
			return ErrNativeCredentialUnverified
		}
		if realm == "" && current.FundingRealm == NativeFundingLocalOnly {
			return ErrNativeCredentialUnverified
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
		if current.Password == "" || !common.ValidatePasswordAndHash(originalPassword, current.Password) {
			return ErrPasswordSessionMismatch
		}
		if err := change.UpdateWithTx(tx, true); err != nil {
			return err
		}
		next := change.AuthVersion
		if next <= userVersion {
			return ErrUserSessionInactive
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
		return tx.Model(&UserSession{}).Where("sid IN ? AND status = ?", sids, UserSessionStatusActive).Updates(map[string]interface{}{"status": UserSessionStatusRevoked, "revoked_at": now, "revoked_reason": "password_changed"}).Error
	})
	if err != nil {
		return nil, err
	}
	if err := writeUserSessionCache(session.cacheEntry(), deadline); err != nil {
		if errors.Is(err, errUserSessionCacheObservationStale) {
			if e := confirmUserSessionActiveSnapshot(&session); e != nil {
				return nil, e
			}
		} else {
			return nil, err
		}
	}
	if err := PublishUserAuthCache(userID); err != nil {
		return nil, err
	}
	for i := range revoked {
		revoked[i].Status, revoked[i].RevokedAt, revoked[i].RevokedReason = UserSessionStatusRevoked, session.LastActiveAt, "password_changed"
		if err := writeUserSessionCache(revoked[i].cacheEntry(), time.Time{}); err != nil {
			common.SysLog(fmt.Sprintf("failed to finalize password session revoke tombstone: %v", err))
		}
	}
	return &session, nil
}
