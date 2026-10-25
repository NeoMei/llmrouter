package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
)

const UniHubIdentitySource = "unihub"

type UniHubIdentityMapping struct {
	ID          int64  `gorm:"primaryKey"`
	Issuer      string `gorm:"type:varchar(255);not null"`
	Subject     string `gorm:"type:varchar(128);not null"`
	IdentityKey string `gorm:"type:char(64);not null;uniqueIndex"`
	UserID      int    `gorm:"not null;uniqueIndex"`
}

var ErrUniHubProjectionInvalid = errors.New("UniHub projection is invalid")
var uniHubProjectionMu sync.Mutex

func CheckUniHubProjection(userID int) (bool, error) {
	var user User
	if err := DB.Unscoped().First(&user, userID).Error; err != nil {
		return false, err
	}
	var mapping UniHubIdentityMapping
	err := DB.Where("user_id = ?", userID).First(&mapping).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		if user.IdentitySource == "" && strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return false, nil
		}
		return false, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) && user.IdentitySource == "" {
		return false, nil
	}
	if err != nil || len(mapping.IdentityKey) < 12 || user.DeletedAt.Valid || user.IdentitySource != UniHubIdentitySource || user.UniHubUserID != mapping.Subject || user.Status != common.UserStatusEnabled || user.Role != common.RoleCommonUser || user.Quota != 0 || user.UsedQuota != 0 || user.RequestCount != 0 || user.Password != "" || user.AccessToken != nil || user.AffCode != "unihub_"+mapping.IdentityKey[:12] || user.AffCount != 0 || user.AffQuota != 0 || user.AffHistoryQuota != 0 || user.InviterId != 0 || user.StripeCustomer != "" {
		return true, ErrUniHubProjectionInvalid
	}
	return true, nil
}

func (UniHubIdentityMapping) TableName() string { return "unihub_identity_mappings" }

func ResolveUniHubProjection(identity platformaccount.Identity) (*User, error) {
	issuer, subject := strings.TrimSpace(identity.Issuer), strings.TrimSpace(identity.User.ID)
	if issuer == "" || subject == "" || len(issuer) > 255 || len(subject) > 128 {
		return nil, errors.New("invalid UniHub identity")
	}
	key := sha256.Sum256([]byte(issuer + "\x00" + subject))
	identityKey := hex.EncodeToString(key[:])
	uniHubProjectionMu.Lock()
	defer uniHubProjectionMu.Unlock()
	var user User
	for attempt := 0; attempt < 10; attempt++ {
		err := DB.Transaction(func(tx *gorm.DB) error {
			var mapping UniHubIdentityMapping
			err := tx.Where("identity_key = ?", identityKey).First(&mapping).Error
			if err == nil {
				if err = tx.First(&user, mapping.UserID).Error; err != nil {
					return err
				}
				if mapping.Issuer != issuer || mapping.Subject != subject || user.IdentitySource != UniHubIdentitySource || user.UniHubUserID != subject || user.DeletedAt.Valid || user.Role != common.RoleCommonUser || user.Quota != 0 || user.UsedQuota != 0 || user.RequestCount != 0 || user.Password != "" || user.AccessToken != nil || user.AffCode != "unihub_"+identityKey[:12] || user.AffCount != 0 || user.AffQuota != 0 || user.AffHistoryQuota != 0 || user.InviterId != 0 || user.StripeCustomer != "" || user.Status != common.UserStatusEnabled {
					return errors.New("UniHub identity projection mismatch")
				}
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			username := "unihub_" + identityKey[:12]
			user = User{Username: username, DisplayName: identity.User.Name, Email: identity.User.Email, Password: "", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: 0, AuthVersion: 1, AffCode: username, IdentitySource: UniHubIdentitySource, UniHubUserID: subject}
			if err = tx.Create(&user).Error; err != nil {
				return err
			}
			return tx.Create(&UniHubIdentityMapping{Issuer: issuer, Subject: subject, IdentityKey: identityKey, UserID: user.Id}).Error
		})
		if err == nil {
			_ = populateUserCache(user)
			return &user, nil
		}
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "unique") && !strings.Contains(lower, "duplicate") && !strings.Contains(lower, "locked") && !strings.Contains(lower, "busy") && !strings.Contains(lower, "deadlock") {
			return nil, err
		}
	}
	return nil, fmt.Errorf("UniHub identity mapping contention")
}
