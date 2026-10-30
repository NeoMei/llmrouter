package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrTwoFAInvalidCode = errors.New("验证码或备用码错误，请重试")

func lockTwoFASecurityUser(tx *gorm.DB, userID int) (*User, error) {
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error; err != nil {
			return nil, err
		}
	}
	var user User
	if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// ChangeTwoFAForSession commits the factor change and current native session
// advancement together. The identity is the already-authenticated request SID.
func ChangeTwoFAForSession(userID int, sid string, sessionVersion, userVersion int64, operation, code string, backupCodes []string) (*UserSession, error) {
	var session UserSession
	var revoked []UserSession
	invalid := false
	deadline := userSessionCacheDeadline()
	err := DB.Transaction(func(tx *gorm.DB) error {
		user, err := lockTwoFASecurityUser(tx, userID)
		if err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled || user.AuthVersion != userVersion {
			return ErrUserSessionInactive
		}
		if err := lockForUpdate(tx).Where("sid = ? AND user_id = ?", sid, userID).First(&session).Error; err != nil {
			return err
		}
		if (session.AuthRealm != "" && session.AuthRealm != NativeFundingLocalOnly) || session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() || session.Version != sessionVersion || session.UserAuthVersion != userVersion {
			return ErrUserSessionInactive
		}
		if err := guardLegacyCentralSecurityTx(tx, userID, session.AuthRealm); err != nil {
			return err
		}
		var factor TwoFA
		if err := lockForUpdate(tx).Where("user_id = ?", userID).First(&factor).Error; err != nil {
			return err
		}
		if operation == "enable" && factor.IsEnabled {
			return ErrTwoFAAlreadyEnabled
		}
		if operation != "enable" && !factor.IsEnabled {
			return ErrTwoFANotEnabled
		}
		if operation != "enable" && factor.IsLocked() {
			return errors.New("账户已被锁定，请稍后重试")
		}
		cleanCode, _ := common.ValidateNumericCode(code)
		valid := common.ValidateTOTPCode(factor.Secret, cleanCode)
		if !valid && operation == "disable" && common.ValidateBackupCode(code) {
			var candidates []TwoFABackupCode
			if err := lockForUpdate(tx).Where("user_id = ? AND is_used = ?", userID, false).Find(&candidates).Error; err != nil {
				return err
			}
			for _, candidate := range candidates {
				if common.ValidatePasswordAndHash(common.NormalizeBackupCode(code), candidate.CodeHash) {
					valid = true
					break
				}
			}
		}
		if !valid {
			invalid = true
			if operation == "enable" {
				return nil
			}
			attempts := factor.FailedAttempts + 1
			var lockedUntil *time.Time
			if attempts >= common.MaxFailAttempts {
				u := time.Now().Add(time.Duration(common.LockoutDuration) * time.Second)
				lockedUntil = &u
			}
			return tx.Model(&factor).Updates(map[string]interface{}{"failed_attempts": attempts, "locked_until": lockedUntil}).Error
		}
		switch operation {
		case "enable":
			if err := tx.Model(&factor).Updates(map[string]interface{}{"is_enabled": true, "failed_attempts": 0, "locked_until": nil}).Error; err != nil {
				return err
			}
		case "disable":
			if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&TwoFABackupCode{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Delete(&factor).Error; err != nil {
				return err
			}
		case "backup":
			if err := replaceBackupCodesWithTx(tx, userID, backupCodes); err != nil {
				return err
			}
			if err := tx.Model(&factor).Updates(map[string]interface{}{"failed_attempts": 0, "locked_until": nil, "last_used_at": time.Now()}).Error; err != nil {
				return err
			}
		default:
			return errors.New("invalid 2FA operation")
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
		query := tokenRealmQuery(tx, []string{session.AuthRealm}).Where("user_id = ? AND sid <> ? AND status = ? AND expires_at > ?", userID, sid, UserSessionStatusActive, now)
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
		return tx.Model(&UserSession{}).Where("sid IN ? AND status = ?", sids, UserSessionStatusActive).Updates(map[string]interface{}{"status": UserSessionStatusRevoked, "revoked_at": now, "revoked_reason": "twofa_" + operation}).Error
	})
	if err != nil {
		return nil, err
	}
	if invalid {
		return nil, ErrTwoFAInvalidCode
	}
	if err := writeUserSessionCache(session.cacheEntry(), deadline); err != nil {
		return nil, err
	}
	if err := PublishUserAuthCache(userID); err != nil {
		return nil, err
	}
	for i := range revoked {
		revoked[i].Status = UserSessionStatusRevoked
		revoked[i].RevokedAt = session.LastActiveAt
		revoked[i].RevokedReason = "twofa_" + operation
		if err := writeUserSessionCache(revoked[i].cacheEntry(), time.Time{}); err != nil {
			common.SysLog("failed to finalize user session revoke tombstone: " + err.Error())
		}
	}
	return &session, nil
}
