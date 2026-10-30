package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

type CentralLoginCredential struct {
	UserID      int    `gorm:"primaryKey"`
	Kind        string `gorm:"primaryKey;size:128"`
	Digest      string `gorm:"size:64;not null"`
	BindingID   string `gorm:"size:128;not null"`
	AuthVersion int64  `gorm:"not null"`
}

type CentralSessionCredential struct {
	SID    string `gorm:"column:sid;primaryKey;size:64"`
	UserID int    `gorm:"not null"`
	Kind   string `gorm:"size:128;not null"`
	Digest string `gorm:"size:64;not null"`
}

// The caller must verify the central session's live source and the external
// authenticator before invoking this within the binding transaction.
func SetCentralLoginCredentialTx(tx *gorm.DB, userID int, sid, kind, value string, bind func(*gorm.DB) error) error {
	if tx == nil || userID <= 0 || sid == "" || kind == "" || len(kind) > 128 || value == "" || bind == nil {
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
	var state CentralAuthState
	if err := lockForUpdate(tx).First(&state, "user_id = ?", userID).Error; err != nil {
		return err
	}
	if session.AuthRealm != CentralAuthRealm || session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() || session.UserAuthVersion != state.AuthVersion || !state.Active || state.BindingID == "" {
		return ErrNativeCredentialUnverified
	}
	var native NativeLoginCredential
	if err := tx.Where("user_id = ? AND kind = ?", userID, kind).First(&native).Error; err == nil {
		return ErrNativeCredentialUnverified
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	existingValue, err := centralStoredCredential(tx, &user, kind)
	if err != nil {
		return err
	}
	var existing CentralLoginCredential
	proofErr := lockForUpdate(tx).First(&existing, "user_id = ? AND kind = ?", userID, kind).Error
	if proofErr != nil && !errors.Is(proofErr, gorm.ErrRecordNotFound) {
		return proofErr
	}
	if existingValue != "" {
		oldDigest := sha256.Sum256([]byte(existingValue))
		if proofErr != nil || existing.Digest != hex.EncodeToString(oldDigest[:]) || existing.BindingID != state.BindingID || existing.AuthVersion != state.AuthVersion {
			return ErrNativeCredentialUnverified
		}
	} else if proofErr == nil {
		return ErrNativeCredentialUnverified
	}
	if err := bind(tx); err != nil {
		return err
	}
	if err := tx.First(&user, userID).Error; err != nil {
		return err
	}
	if err := matchCentralStoredCredential(tx, &user, kind, value); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(value))
	proof := CentralLoginCredential{UserID: userID, Kind: kind, Digest: hex.EncodeToString(sum[:]), BindingID: state.BindingID, AuthVersion: state.AuthVersion}
	if errors.Is(proofErr, gorm.ErrRecordNotFound) {
		return tx.Create(&proof).Error
	}
	return tx.Model(&existing).Update("digest", proof.Digest).Error
}

func IsCentralLoginCredential(ctx context.Context, userID int, kind, value string) (bool, error) {
	var user User
	if err := DB.WithContext(ctx).First(&user, userID).Error; err != nil {
		return false, err
	}
	var proof CentralLoginCredential
	if err := DB.WithContext(ctx).First(&proof, "user_id = ? AND kind = ?", userID, kind).Error; err != nil {
		return false, err
	}
	state, err := ReadCentralAuthState(ctx, userID)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256([]byte(value))
	if value == "" || proof.Digest != hex.EncodeToString(sum[:]) || !state.Active || state.BindingID != proof.BindingID || state.AuthVersion != proof.AuthVersion {
		return false, ErrNativeCredentialUnverified
	}
	if err = matchCentralStoredCredential(DB.WithContext(ctx), &user, kind, value); err != nil {
		return false, err
	}
	return true, nil
}

func ValidateCentralSessionCredential(ctx context.Context, session *UserSession) error {
	if session == nil || session.AuthRealm != CentralAuthRealm || session.LoginMethod != "central_credential" {
		return ErrNativeCredentialUnverified
	}
	var proof CentralSessionCredential
	if err := DB.WithContext(ctx).First(&proof, "sid = ? AND user_id = ?", session.SID, session.UserID).Error; err != nil {
		return err
	}
	var credential CentralLoginCredential
	if err := DB.WithContext(ctx).First(&credential, "user_id = ? AND kind = ?", session.UserID, proof.Kind).Error; err != nil {
		return err
	}
	var user User
	if err := DB.WithContext(ctx).First(&user, session.UserID).Error; err != nil {
		return err
	}
	state, err := ReadCentralAuthState(ctx, session.UserID)
	if err != nil {
		return err
	}
	current, err := centralStoredCredential(DB.WithContext(ctx), &user, proof.Kind)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(current))
	if current == "" || proof.Digest != hex.EncodeToString(digest[:]) || credential.Digest != proof.Digest || credential.BindingID != state.BindingID || credential.AuthVersion != state.AuthVersion || !state.Active {
		return ErrNativeCredentialUnverified
	}
	return nil
}

// DeleteCentralLoginCredentialTx is called after central source and step-up
// verification. It cannot delete a native or unproven shared credential.
func DeleteCentralLoginCredentialTx(tx *gorm.DB, userID int, sid, kind string, unbind func(*gorm.DB) error) error {
	if tx == nil || userID <= 0 || sid == "" || kind == "" || unbind == nil {
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
	var state CentralAuthState
	if err := lockForUpdate(tx).First(&state, "user_id = ?", userID).Error; err != nil {
		return err
	}
	var proof CentralLoginCredential
	if err := lockForUpdate(tx).First(&proof, "user_id = ? AND kind = ?", userID, kind).Error; err != nil {
		return err
	}
	current, err := centralStoredCredential(tx, &user, kind)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(current))
	if session.AuthRealm != CentralAuthRealm || session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() || !state.Active || state.AuthVersion != session.UserAuthVersion || state.AuthVersion != proof.AuthVersion || state.BindingID != proof.BindingID || current == "" || proof.Digest != hex.EncodeToString(digest[:]) {
		return ErrNativeCredentialUnverified
	}
	if kind == "passkey" {
		switch session.LoginMethod {
		case "product_broker", "unihub":
			// These sessions have already passed live source validation.
		case "central_credential":
			var source CentralSessionCredential
			if err := lockForUpdate(tx).First(&source, "sid = ? AND user_id = ?", sid, userID).Error; err != nil {
				return err
			}
			switch source.Kind {
			case "github_id", "discord_id", "oidc_id", "wechat_id", "telegram_id":
				// These kinds are issued only after their external assertion.
			default:
				provider, err := strconv.Atoi(strings.TrimPrefix(source.Kind, "oauth:"))
				if !strings.HasPrefix(source.Kind, "oauth:") || err != nil || provider <= 0 {
					return ErrNativeCredentialUnverified
				}
			}
			var sourceCredential CentralLoginCredential
			if err := lockForUpdate(tx).First(&sourceCredential, "user_id = ? AND kind = ?", userID, source.Kind).Error; err != nil {
				return err
			}
			sourceValue, err := centralStoredCredential(tx, &user, source.Kind)
			if err != nil {
				return err
			}
			sourceDigest := sha256.Sum256([]byte(sourceValue))
			if sourceValue == "" || source.Digest != hex.EncodeToString(sourceDigest[:]) || sourceCredential.Digest != source.Digest || sourceCredential.BindingID != state.BindingID || sourceCredential.AuthVersion != state.AuthVersion {
				return ErrNativeCredentialUnverified
			}
		default:
			return ErrNativeCredentialUnverified
		}
	}
	if err := unbind(tx); err != nil {
		return err
	}
	return tx.Delete(&proof).Error
}

func matchCentralStoredCredential(tx *gorm.DB, user *User, kind, value string) error {
	current, err := centralStoredCredential(tx, user, kind)
	if err != nil {
		return err
	}
	if current == "" || current != value {
		return ErrNativeCredentialUnverified
	}
	return nil
}

func centralStoredCredential(tx *gorm.DB, user *User, kind string) (string, error) {
	var current string
	switch kind {
	case "github_id":
		current = user.GitHubId
	case "discord_id":
		current = user.DiscordId
	case "oidc_id":
		current = user.OidcId
	case "wechat_id":
		current = user.WeChatId
	case "telegram_id":
		current = user.TelegramId
	case "pat":
		if user.AccessToken != nil {
			current = *user.AccessToken
		}
	case "passkey":
		var passkey PasskeyCredential
		if err := tx.Where("user_id = ?", user.Id).First(&passkey).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", nil
			}
			return "", err
		}
		// A credential ID alone does not identify the verified WebAuthn key.
		current = passkey.CredentialID + "\x00" + passkey.PublicKey
	default:
		provider, err := strconv.Atoi(strings.TrimPrefix(kind, "oauth:"))
		if !strings.HasPrefix(kind, "oauth:") || err != nil || provider <= 0 {
			return "", ErrNativeCredentialUnverified
		}
		var binding UserOAuthBinding
		if err := tx.Where("user_id = ? AND provider_id = ?", user.Id, provider).First(&binding).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", nil
			}
			return "", err
		}
		current = binding.ProviderUserId
	}
	return current, nil
}
