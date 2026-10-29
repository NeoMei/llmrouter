package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// NativeRealmManifest is an operator-only, externally reviewed migration input.
// It is never accepted from an HTTP request or inferred from a login method.
// Digests are of the frozen stored password hash/key, never logged or returned.
type NativeRealmManifest struct {
	Version int                     `json:"version"`
	Users   []NativeRealmEnrollment `json:"users"`
}
type NativeRealmEnrollment struct {
	UserID         int                   `json:"user_id"`
	Role           int                   `json:"role"`
	Status         int                   `json:"status"`
	AuthVersion    int64                 `json:"auth_version"`
	PasswordSHA256 string                `json:"password_sha256"`
	Keys           []NativeKeyEnrollment `json:"keys"`
}
type NativeKeyEnrollment struct {
	ID           int    `json:"id"`
	SecretSHA256 string `json:"secret_sha256"`
	Deleted      bool   `json:"deleted"`
}

// EnrollNativeRealm validates the entire approved manifest in one transaction.
// The trusted operator supplies its separately approved SHA256. No asset, secret,
// role, status, key limit or existing session is overwritten or reclassified.
func EnrollNativeRealm(ctx context.Context, db *gorm.DB, raw []byte, approvedSHA256 string) error {
	sum := sha256.Sum256(raw)
	if ctx == nil || db == nil || len(raw) == 0 || len(raw) > 4<<20 || hex.EncodeToString(sum[:]) != approvedSHA256 {
		return ErrProductFundingConflict
	}
	var manifest NativeRealmManifest
	if common.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 || len(manifest.Users) == 0 {
		return ErrProductFundingConflict
	}
	sort.Slice(manifest.Users, func(i, j int) bool { return manifest.Users[i].UserID < manifest.Users[j].UserID })
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		previous := 0
		for _, expected := range manifest.Users {
			if expected.UserID <= previous || expected.AuthVersion <= 0 || !common.IsValidateRole(expected.Role) || (expected.Status != common.UserStatusEnabled && expected.Status != common.UserStatusDisabled) || len(expected.PasswordSHA256) != 64 {
				return ErrProductFundingConflict
			}
			previous = expected.UserID
			var user User
			if err := lockForUpdate(tx).First(&user, expected.UserID).Error; err != nil {
				return err
			}
			passwordDigest := sha256.Sum256([]byte(user.Password))
			if user.Password == "" || hex.EncodeToString(passwordDigest[:]) != expected.PasswordSHA256 || user.Role != expected.Role || user.Status != expected.Status || user.AuthVersion != expected.AuthVersion || user.ProductRegistrationPending || user.ProductJITPending || user.IdentitySource == ProductIdentityPending {
				return ErrProductFundingConflict
			}
			if user.FundingRealm != "" && (user.FundingRealm != NativeFundingLocalOnly || user.FundingRealmEvidence != approvedSHA256) {
				return ErrProductFundingConflict
			}
			var account ProductFundingAccount
			err := lockForUpdate(tx).First(&account, "user_id = ?", user.Id).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && account.State != "local" {
				return ErrProductFundingConflict
			}
			var pending int64
			if err = tx.Model(&ProductAuthLifecycle{}).Where("user_id = ? AND delivered = ?", user.Id, false).Count(&pending).Error; err != nil {
				return err
			}
			if pending != 0 {
				return ErrProductFundingPending
			}
			if err = tx.Model(&user).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": approvedSHA256, "native_password": true}).Error; err != nil {
				return err
			}
			sort.Slice(expected.Keys, func(i, j int) bool { return expected.Keys[i].ID < expected.Keys[j].ID })
			last := 0
			for _, key := range expected.Keys {
				if key.ID <= last || len(key.SecretSHA256) != 64 {
					return ErrProductFundingConflict
				}
				last = key.ID
				var token Token
				if err = lockForUpdate(tx.Unscoped()).First(&token, key.ID).Error; err != nil {
					return err
				}
				digest := sha256.Sum256([]byte(token.Key))
				if token.UserId != user.Id || token.Key == "" || hex.EncodeToString(digest[:]) != key.SecretSHA256 || token.DeletedAt.Valid != key.Deleted || token.FundingAccountID != "" || token.Purpose != "" || (token.AuthRealm != "" && token.AuthRealm != NativeFundingLocalOnly) {
					return ErrProductFundingConflict
				}
				if !key.Deleted {
					if err = tx.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// NativeRealmUser reads authoritative policy. It never derives realm from an
// identity mapping, network route, cookie, username or credential prefix.
func NativeRealmUser(ctx context.Context, userID int) (*User, error) {
	var user User
	if err := DB.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, err
	}
	if user.FundingRealm != NativeFundingLocalOnly || user.FundingRealmEvidence == "" || !user.NativePassword || user.ProductRegistrationPending || user.ProductJITPending {
		return nil, ErrNativeCredentialUnverified
	}
	state, err := ProductFundingState(ctx, userID)
	if err != nil {
		return nil, err
	}
	if state != "local" {
		return nil, ErrProductFundingConflict
	}
	return &user, nil
}

// NativeRealmToken verifies current SQL credential provenance after normal key
// authentication. It deliberately keeps quota/IP/model/group checks on the
// original authentication path and never reclassifies an unlisted key.
func NativeRealmToken(ctx context.Context, authenticated *Token, ownedRead ...bool) (bool, error) {
	return nativeRealmToken(ctx, authenticated, len(ownedRead) > 0 && ownedRead[0], false)
}

// NativeRealmTokenReadOnly keeps the original usage/history policy: expired or
// exhausted keys can read their records; disabled and deleted keys cannot.
func NativeRealmTokenReadOnly(ctx context.Context, authenticated *Token) (bool, error) {
	return nativeRealmToken(ctx, authenticated, false, true)
}

func nativeRealmToken(ctx context.Context, authenticated *Token, ownedRead, historyRead bool) (bool, error) {
	if authenticated == nil {
		return false, ErrNativeCredentialUnverified
	}
	var current Token
	if err := DB.WithContext(ctx).First(&current, authenticated.Id).Error; err != nil {
		return false, err
	}
	if current.AuthRealm == "" {
		return false, nil
	}
	if current.AuthRealm != NativeFundingLocalOnly || current.UserId != authenticated.UserId || current.Key != authenticated.Key || current.FundingAccountID != "" {
		return false, ErrNativeCredentialUnverified
	}
	if historyRead {
		if current.Status == common.TokenStatusDisabled {
			return false, ErrNativeCredentialUnverified
		}
	} else if (current.Status != common.TokenStatusEnabled && !(ownedRead && current.Status == common.TokenStatusExhausted)) || (current.ExpiredTime != -1 && current.ExpiredTime <= common.GetTimestamp()) {
		return false, ErrNativeCredentialUnverified
	}
	user, err := NativeRealmUser(ctx, current.UserId)
	if err != nil {
		return false, err
	}
	if user.Status != common.UserStatusEnabled {
		return false, ErrNativeCredentialUnverified
	}
	// Use current restrictions even when a pre-mutation cache snapshot survived.
	authenticated.UnlimitedQuota = current.UnlimitedQuota
	if !current.UnlimitedQuota && authenticated.RemainQuota > current.RemainQuota {
		authenticated.RemainQuota = current.RemainQuota
	}
	authenticated.AllowIps = current.AllowIps
	authenticated.ModelLimitsEnabled = current.ModelLimitsEnabled
	authenticated.ModelLimits = current.ModelLimits
	authenticated.Group = current.Group
	authenticated.CrossGroupRetry = current.CrossGroupRetry
	authenticated.Purpose = current.Purpose
	authenticated.BoundOrigin = current.BoundOrigin
	return true, nil
}

// ValidateLocalFundingCredential is defense in depth for all original billing
// entry points. Central/unknown credentials cannot fall back to a native wallet
// or subscription just because their shared owner's funding state is local.
func ValidateLocalFundingCredential(ctx context.Context, userID, tokenID int) error {
	return validateLocalFundingCredentialTx(DB.WithContext(ctx), userID, tokenID)
}
func validateLocalFundingCredentialTx(tx *gorm.DB, userID, tokenID int) error {
	var user User
	if err := tx.First(&user, userID).Error; err != nil {
		return err
	}
	if user.FundingRealm != NativeFundingLocalOnly {
		return nil
	}
	if !user.NativePassword || user.FundingRealmEvidence == "" || user.Status != common.UserStatusEnabled {
		return ErrNativeCredentialUnverified
	}
	var token Token
	if err := tx.First(&token, "id = ? AND user_id = ?", tokenID, userID).Error; err != nil {
		return err
	}
	if token.AuthRealm != NativeFundingLocalOnly || token.FundingAccountID != "" || token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= common.GetTimestamp()) {
		return ErrNativeCredentialUnverified
	}
	return nil
}

// CreateNativeUserWithTx is a separate trusted-admin creation boundary, not a
// historical cohort migration. The controller supplies the authenticated native
// actor; the transaction rechecks local authority before issuing a new owner.
func CreateNativeUserWithTx(tx *gorm.DB, actorID int, user *User) error {
	if user == nil || user.Id != 0 || user.Password == "" {
		return ErrNativeCredentialUnverified
	}
	var actor User
	if err := lockForUpdate(tx).First(&actor, actorID).Error; err != nil {
		return err
	}
	if actor.FundingRealm != NativeFundingLocalOnly || !actor.NativePassword || actor.FundingRealmEvidence == "" || actor.Status != common.UserStatusEnabled || actor.Role < common.RoleAdminUser || user.Role >= actor.Role || !common.IsValidateRole(user.Role) {
		return ErrNativeCredentialUnverified
	}
	user.FundingRealm = NativeFundingLocalOnly
	user.FundingRealmEvidence = fmt.Sprintf("native-admin:%d:auth-version:%d", actor.Id, actor.AuthVersion)
	user.NativePassword = true
	user.IdentitySource = ""
	user.ProductRegistrationPending = false
	user.ProductJITPending = false
	return withNormalizedEmailLock(tx, user.Email, func(tx *gorm.DB) error {
		if err := user.prepareForInsert(tx); err != nil {
			return err
		}
		initializeNewUserPolicy(user)
		return tx.Create(user).Error
	})
}
