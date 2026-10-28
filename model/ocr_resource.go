package model

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// OCRResource owns only object metadata and a bounded opaque download grant.
// Multi-megabyte payloads never enter SQL TEXT columns.
type OCRResource struct {
	ID          string `gorm:"primaryKey;size:36"`
	JobID       string `gorm:"size:36;index"`
	ObjectKey   string `gorm:"size:180" json:"-"`
	Kind        string `gorm:"size:16"`
	ContentType string `gorm:"size:80"`
	Size        int64
	Checksum    string `gorm:"size:64"`
	ExpiresAt   int64  `gorm:"index"`
	Revoked     bool
}

// PublishOCRResources atomically publishes a complete result set, or returns
// the winner of concurrent ingestions. Losing callers delete their own objects.
func (s *OCRBillingStore) PublishOCRResources(ctx context.Context, id string, resources []OCRResource, resultID string) (bool, error) {
	if len(resources) < 1 || len(resources) > 257 {
		return false, ErrOCRConflict
	}
	published := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		if j.ResultResourceID != "" {
			return nil
		}
		if j.ProviderState != "done" || (j.Funding != "platform" && (j.Decision != "success" || j.BillingState != "settled")) || (j.Funding == "platform" && j.Decision != "" && j.Decision != "success") {
			return ErrOCRConflict
		}
		found := false
		for _, r := range resources {
			if r.JobID != id || r.Size < 1 || r.ExpiresAt <= time.Now().Unix() {
				return ErrOCRConflict
			}
			if r.ID == resultID && r.Kind == "jsonl" {
				found = true
			}
		}
		if !found {
			return ErrOCRConflict
		}
		if err := tx.Create(&resources).Error; err != nil {
			return err
		}
		j.ResultResourceID = resultID
		if err := ocrSaveJob(tx, j); err != nil {
			return err
		}
		published = true
		return nil
	})
	return published, err
}

// Input resources are published only for this already-owned pending job.
func (s *OCRBillingStore) PublishOCRInput(ctx context.Context, r OCRResource) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, r.JobID)
		if err != nil {
			return err
		}
		if j.SubmissionState != "unclaimed" || j.Decision != "" || r.Kind != "input" {
			return ErrOCRConflict
		}
		var count int64
		if err := tx.Model(&OCRResource{}).Where("job_id = ? AND kind = ?", r.JobID, "input").Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrOCRConflict
		}
		return tx.Create(&r).Error
	})
}
func (s *OCRBillingStore) OCRResource(ctx context.Context, jobID, id string) (OCRResource, error) {
	var r OCRResource
	err := s.db.WithContext(ctx).Where("id = ? AND job_id = ?", id, jobID).First(&r).Error
	return r, err
}
func (s *OCRBillingStore) OCRInput(ctx context.Context, jobID string) (OCRResource, error) {
	var r OCRResource
	err := s.db.WithContext(ctx).Where("job_id = ? AND kind = ?", jobID, "input").First(&r).Error
	return r, err
}

// Retention removes only expired resources after marking them revoked. Deletion
// is retriable and happens outside SQL transactions; grants fail before storage.
func (s *OCRBillingStore) PurgeOCRResources(ctx context.Context, limit int, now int64, remove func(context.Context, string) error) error {
	if limit < 1 || limit > 1000 {
		return ErrOCRConflict
	}
	var rows []OCRResource
	if err := s.db.WithContext(ctx).Where("expires_at <= ?", now).Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.db.WithContext(ctx).Model(&OCRResource{}).Where("id = ?", r.ID).Update("revoked", true).Error; err != nil {
			return err
		}
		if err := remove(ctx, r.ObjectKey); err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Where("resource_id = ?", r.ID).Delete(&OCRResourceGrant{}).Error; err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Delete(&OCRResource{}, "id = ?", r.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

var ErrOCRGrant = errors.New("OCR resource grant unavailable")

// OCRResourceGrant retains digest-only scoped capabilities across secret rotation
// and concurrent status requests. One row per resource/half-TTL bucket bounds issuance.
type OCRResourceGrant struct {
	ResourceID string `gorm:"primaryKey;size:36"`
	ExpiresAt  int64  `gorm:"primaryKey;index"`
	JobID      string `gorm:"size:36;index"`
	Digest     string `gorm:"size:64" json:"-"`
}

func (s *OCRBillingStore) PutOCRGrant(ctx context.Context, g OCRResourceGrant, now int64) error {
	if len(g.Digest) != 64 {
		return ErrOCRGrant
	}
	if err := s.db.WithContext(ctx).Where("resource_id = ? AND expires_at <= ?", g.ResourceID, now).Delete(&OCRResourceGrant{}).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&g).Error; err != nil {
		return err
	}
	var current OCRResourceGrant
	if err := s.db.WithContext(ctx).Where("resource_id = ? AND expires_at = ?", g.ResourceID, g.ExpiresAt).First(&current).Error; err != nil {
		return err
	}
	if current.JobID != g.JobID || current.Digest != g.Digest {
		return ErrOCRGrant
	}
	return nil
}
func (s *OCRBillingStore) ValidateOCRGrant(ctx context.Context, jobID, resourceID, digest string, now int64) error {
	var count int64
	err := s.db.WithContext(ctx).Model(&OCRResourceGrant{}).Where("job_id = ? AND resource_id = ? AND digest = ? AND expires_at > ?", jobID, resourceID, digest, now).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrOCRGrant
	}
	return nil
}
