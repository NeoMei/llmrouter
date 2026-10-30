package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"gorm.io/gorm"
)

// CentralAuthState is populated only from authenticated central identity
// evidence. No native credential or client request may create this state.
type CentralAuthState struct {
	UserID        int    `gorm:"primaryKey"`
	AuthVersion   int64  `gorm:"not null"`
	AccessVersion int64  `gorm:"not null"`
	Active        bool   `gorm:"not null"`
	Issuer        string `gorm:"size:255;not null"`
	Subject       string `gorm:"size:128;not null"`
	BindingID     string `gorm:"size:128;not null"`
	ProductID     string `gorm:"size:128;not null"`
	InstanceID    string `gorm:"size:128;not null"`
}

const CentralAuthRealm = "CENTRAL"

func ReadCentralAuthState(ctx context.Context, userID int) (*CentralAuthState, error) {
	var state CentralAuthState
	if err := DB.WithContext(ctx).First(&state, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func RecordCentralAuthState(ctx context.Context, userID int, identity userkit.ProductIdentity, source userkit.Access) error {
	access, binding := identity.Access, identity.Binding
	if userID <= 0 || binding.ID == "" || binding.LocalUserID != strconv.Itoa(userID) || binding.UserID == "" || binding.UserID != access.Subject || access.User.ID != access.Subject ||
		source.Issuer != access.Issuer || source.Subject != access.Subject || source.User.ID != access.Subject || source.ProductID != access.ProductID || source.InstanceID != access.InstanceID ||
		binding.ProductID != access.ProductID || binding.InstanceID != access.InstanceID || access.Issuer == "" || access.ProductID == "" || access.InstanceID == "" ||
		identity.AuthVersion < 1 || access.Version < 1 || !source.Active || source.ExpiresAt <= time.Now().Unix() ||
		!(userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(userID)}, Access: source}).HasPermission("product:use") ||
		!(userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(userID)}, Access: access}).HasPermission("product:use") {
		return ErrNativeCredentialUnverified
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error; err != nil {
				return err
			}
		}
		var owner User
		if err := lockForUpdate(tx).First(&owner, userID).Error; err != nil {
			return err
		}
		// A pending legacy lifecycle may still advance the central version.
		// Do not create or refresh independent central authority until the
		// existing event is reconciled and durably marked delivered.
		var pending int64
		if err := tx.Model(&ProductAuthLifecycle{}).Where("user_id = ? AND delivered = ?", userID, false).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return ErrProductFundingPending
		}
		var latest ProductAuthLifecycle
		err := tx.Where("user_id = ?", userID).Order("next_version DESC").First(&latest).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && identity.AuthVersion < latest.NextVersion {
			return ErrUserAuthVersionConflict
		}
		var mapping UniHubIdentityMapping
		if err := tx.Where("user_id = ?", userID).First(&mapping).Error; err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(mapping.Issuer + "\x00" + mapping.Subject))
		if mapping.Issuer != access.Issuer || mapping.Subject != access.Subject || mapping.IdentityKey != hex.EncodeToString(digest[:]) {
			return ErrUniHubProjectionInvalid
		}
		var current CentralAuthState
		err = lockForUpdate(tx).First(&current, "user_id = ?", userID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		next := CentralAuthState{UserID: userID, AuthVersion: identity.AuthVersion, AccessVersion: access.Version, Active: true, Issuer: access.Issuer, Subject: access.Subject, BindingID: binding.ID, ProductID: access.ProductID, InstanceID: access.InstanceID}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&next).Error
		}
		if current.Issuer != next.Issuer || current.Subject != next.Subject || current.BindingID != next.BindingID || current.ProductID != next.ProductID || current.InstanceID != next.InstanceID || next.AuthVersion < current.AuthVersion || next.AccessVersion < current.AccessVersion || (!current.Active && next.AccessVersion == current.AccessVersion) {
			return ErrUniHubProjectionInvalid
		}
		return tx.Model(&current).Updates(map[string]any{"auth_version": next.AuthVersion, "access_version": next.AccessVersion, "active": true}).Error
	})
}

// RecordCentralAuthDenial only records a fresh, authenticated central denial
// against an already proven immutable binding; transient transport errors must
// never be passed here or change persistent state.
func RecordCentralAuthDenial(ctx context.Context, userID int, identity userkit.ProductIdentity) error {
	access, binding := identity.Access, identity.Binding
	if access.Active || access.Version <= 0 || identity.AuthVersion <= 0 || binding.LocalUserID != strconv.Itoa(userID) || binding.UserID != access.Subject || access.User.ID != access.Subject || binding.ProductID != access.ProductID || binding.InstanceID != access.InstanceID {
		return ErrUniHubProjectionInvalid
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&CentralAuthState{}).Where("user_id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error; err != nil {
				return err
			}
		}
		var state CentralAuthState
		if err := lockForUpdate(tx).First(&state, "user_id = ?", userID).Error; err != nil {
			return err
		}
		if state.BindingID != binding.ID || state.Issuer != access.Issuer || state.Subject != access.Subject || state.ProductID != access.ProductID || state.InstanceID != access.InstanceID || access.Version < state.AccessVersion || identity.AuthVersion < state.AuthVersion || (state.Active && access.Version == state.AccessVersion) {
			return ErrUniHubProjectionInvalid
		}
		return tx.Model(&state).Updates(map[string]any{"active": false, "access_version": access.Version, "auth_version": identity.AuthVersion}).Error
	})
}
