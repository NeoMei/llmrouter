package model

import (
	"context"
	"fmt"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
	"strconv"
)

// ProductAuthLifecycle is committed with the original authenticator mutation.
// It contains version metadata only; replay cannot enable or grant an account.
type ProductAuthLifecycle struct {
	ID              string `gorm:"primaryKey;size:128"`
	UserID          int    `gorm:"not null;index"`
	PreviousVersion int64  `gorm:"not null"`
	NextVersion     int64  `gorm:"not null"`
	Delivered       bool   `gorm:"not null;default:false;index"`
}

const ProductIdentityPending = "unihub_pending"

func PrepareProductIdentity(user *User) {
	if user != nil && platformaccount.RuntimeEnabled() && user.IdentitySource == "" {
		user.IdentitySource = ProductIdentityPending
		user.ProductRegistrationPending = true
	}
}
func ApplyManagedCredentialChange(userID int, change func(*gorm.DB) error) error {
	if e := DB.Transaction(func(tx *gorm.DB) error {
		if e := change(tx); e != nil {
			return e
		}
		_, e := IncrementUserAuthVersionWithTx(tx, userID)
		return e
	}); e != nil {
		return e
	}
	if e := PublishUserAuthCache(userID); e != nil {
		return e
	}
	_, e := RevokeAllUserSessions(userID, "product_authenticator_changed")
	return e
}

func queueProductAuthVersion(tx *gorm.DB, userID int, previous, next int64) error {
	if !platformaccount.RuntimeEnabled() {
		return nil
	}
	var user User
	if e := tx.Unscoped().Select("identity_source", "funding_realm").First(&user, userID).Error; e != nil {
		return e
	}
	if user.FundingRealm == NativeFundingLocalOnly || user.IdentitySource != UniHubIdentitySource {
		return nil
	}
	return tx.Create(&ProductAuthLifecycle{ID: fmt.Sprintf("auth-version:%d:%d", userID, next), UserID: userID, PreviousVersion: previous, NextVersion: next}).Error
}
func SyncProductAuthVersions(ctx context.Context, userID int) error {
	if !platformaccount.RuntimeEnabled() {
		return nil
	}
	var events []ProductAuthLifecycle
	if e := DB.Where("user_id = ? AND delivered = ?", userID, false).Order("next_version").Find(&events).Error; e != nil {
		return e
	}
	if len(events) == 0 {
		return nil
	}
	c, e := platformaccount.RuntimeClient()
	if e != nil {
		return e
	}
	for _, event := range events {
		if _, e = c.SyncProductLifecycle(ctx, userkit.ProductLifecycle{LocalUserID: strconv.Itoa(userID), OperationID: event.ID, PreviousAuthVersion: event.PreviousVersion, NextAuthVersion: event.NextVersion}); e != nil {
			return e
		}
		if e = DB.Model(&ProductAuthLifecycle{}).Where("id = ?", event.ID).Update("delivered", true).Error; e != nil {
			return e
		}
	}
	return nil
}
