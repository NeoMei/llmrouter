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
	sum := sha256.Sum256([]byte(mapping.Issuer + "\x00" + mapping.Subject))
	if err != nil || mapping.IdentityKey != hex.EncodeToString(sum[:]) || user.DeletedAt.Valid || user.IdentitySource != UniHubIdentitySource || user.UniHubUserID != mapping.Subject {
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
				if mapping.Issuer != issuer || mapping.Subject != subject || user.IdentitySource != UniHubIdentitySource || user.UniHubUserID != subject || user.DeletedAt.Valid || (!platformaccount.RuntimeEnabled() && user.Status != common.UserStatusEnabled) {
					return errors.New("UniHub identity projection mismatch")
				}
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			username := "unihub_" + identityKey[:12]
			user = User{Username: username, DisplayName: identity.User.Name, Email: identity.User.Email, Password: "", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: 0, AuthVersion: 1, AffCode: username, IdentitySource: UniHubIdentitySource, UniHubUserID: subject}
			if platformaccount.RuntimeEnabled() {
				user.ProductRegistrationPending = true
				user.ProductJITPending = true
				err = user.InsertWithTx(tx, 0)
			} else {
				err = tx.Create(&user).Error
			}
			if err != nil {
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

// BindUniHubIdentity consumes a server-verified central binding. The caller must
// prove the local subject; profile/email similarity is never evidence of a link.
func BindUniHubIdentity(identity platformaccount.Identity, localID int) (*User, error) {
	issuer, subject := strings.TrimSpace(identity.Issuer), strings.TrimSpace(identity.User.ID)
	if localID <= 0 || issuer == "" || subject == "" || len(issuer) > 255 || len(subject) > 128 {
		return nil, ErrUniHubProjectionInvalid
	}
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	key := hex.EncodeToString(sum[:])
	var user User
	e := DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.First(&user, localID).Error; e != nil {
			return e
		}
		if user.DeletedAt.Valid {
			return ErrUniHubProjectionInvalid
		}
		var byIdentity UniHubIdentityMapping
		e := tx.Where("identity_key = ?", key).First(&byIdentity).Error
		if e == nil && byIdentity.UserID != localID {
			return ErrUniHubProjectionInvalid
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var byLocal UniHubIdentityMapping
		e = tx.Where("user_id = ?", localID).First(&byLocal).Error
		if e == nil && (byLocal.IdentityKey != key || byLocal.Issuer != issuer || byLocal.Subject != subject) {
			return ErrUniHubProjectionInvalid
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if user.IdentitySource != "" && user.IdentitySource != ProductIdentityPending && (user.IdentitySource != UniHubIdentitySource || user.UniHubUserID != subject) {
			return ErrUniHubProjectionInvalid
		}
		if errors.Is(e, gorm.ErrRecordNotFound) {
			if e = tx.Create(&UniHubIdentityMapping{Issuer: issuer, Subject: subject, IdentityKey: key, UserID: localID}).Error; e != nil {
				return e
			}
		}
		if e = tx.Model(&user).Updates(map[string]any{"identity_source": UniHubIdentitySource, "unihub_user_id": subject}).Error; e != nil {
			return e
		}
		user.IdentitySource = UniHubIdentitySource
		user.UniHubUserID = subject
		return nil
	})
	if e != nil {
		return nil, e
	}
	_ = populateUserCache(user)
	return &user, nil
}

func VerifyUniHubIdentityBinding(localID int, issuer, subject string) error {
	var mapping UniHubIdentityMapping
	if e := DB.Where("user_id = ?", localID).First(&mapping).Error; e != nil {
		return e
	}
	if mapping.Issuer != issuer || mapping.Subject != subject {
		return ErrUniHubProjectionInvalid
	}
	return nil
}
