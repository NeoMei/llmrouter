package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

var ErrRelayMediaUnavailable = errors.New("relay media unavailable")

// RelayMediaResource is private, nonfinancial bookkeeping. Never serialize this
// row in client responses. Staging precedes object Put; only Ready is servable.
type RelayMediaResource struct {
	ID         string  `gorm:"primaryKey;size:36" json:"-"`
	UserID     int     `json:"-"`
	TokenID    int     `json:"-"`
	RequestID  string  `gorm:"size:128" json:"-"`
	ObjectKey  string  `gorm:"size:100" json:"-"`
	Format     string  `gorm:"size:16" json:"-"`
	Size       int64   `json:"-"`
	SHA256     string  `gorm:"size:64" json:"-"`
	Width      int     `json:"-"`
	Height     int     `json:"-"`
	Seconds    float64 `json:"-"`
	AudioKnown bool    `json:"-"`
	HasAudio   bool    `json:"-"`
	State      string  `gorm:"size:16;index" json:"-"`
	ExpiresAt  int64   `gorm:"index" json:"-"`
	CreatedAt  int64   `gorm:"autoCreateTime" json:"-"`
	UpdatedAt  int64   `gorm:"autoUpdateTime" json:"-"`
}

// RelayMediaReady never revives revoked/expired records. An uncertain database
// result leaves the object tracked; callers must not delete it independently.
func RelayMediaReady(ctx context.Context, db *gorm.DB, id string, now int64) error {
	res := db.WithContext(ctx).Model(&RelayMediaResource{}).Where("id = ? AND state = ? AND expires_at > ?", id, "staging", now).Updates(map[string]any{"state": "ready", "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrRelayMediaUnavailable
	}
	return nil
}

// ReadRelayMedia holds the row lock through the bounded object open/read. Revoke
// wins before a later open, while an already-open immutable read may finish.
func ReadRelayMedia(ctx context.Context, db *gorm.DB, id string, read func(RelayMediaResource) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r RelayMediaResource
		if err := lockForUpdate(tx).Where("id = ?", id).First(&r).Error; err != nil {
			return err
		}
		return read(r)
	})
}
func RevokeRelayMedia(ctx context.Context, db *gorm.DB, id string, userID, tokenID int) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r RelayMediaResource
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ? AND token_id = ?", id, userID, tokenID).First(&r).Error; err != nil {
			return ErrRelayMediaUnavailable
		}
		return tx.Model(&RelayMediaResource{}).Where("id = ?", id).Updates(map[string]any{"state": "revoked", "updated_at": time.Now().Unix()}).Error
	})
}

// PurgeRelayMedia first revokes, then deletes outside SQL, and only then removes
// bookkeeping. Expired staging covers crash/uncertain Put/ready acknowledgement.
func PurgeRelayMedia(ctx context.Context, db *gorm.DB, now int64, limit int, remove func(context.Context, string) error) error {
	if limit < 1 || limit > 100 {
		return ErrRelayMediaUnavailable
	}
	var rows []RelayMediaResource
	if err := db.WithContext(ctx).Where("state = ? OR expires_at <= ?", "revoked", now).Order("updated_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	var first error
	for _, r := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		eligible := false
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current RelayMediaResource
			if err := lockForUpdate(tx).Where("id = ?", r.ID).First(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if current.State != "revoked" && current.ExpiresAt > now {
				return nil
			}
			eligible = true
			return tx.Model(&RelayMediaResource{}).Where("id = ?", r.ID).Updates(map[string]any{"state": "revoked", "updated_at": now}).Error
		})
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if !eligible {
			continue
		}
		if err := remove(ctx, r.ObjectKey); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := db.WithContext(ctx).Where("id = ? AND state = ?", r.ID, "revoked").Delete(&RelayMediaResource{}).Error; err != nil && first == nil {
			first = err
		}
	}
	return first
}
