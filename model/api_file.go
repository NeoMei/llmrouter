package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	APIFileStatusPending = "pending"
	APIFileStatusReady   = "ready"
	APIFileStatusDeleted = "deleted"
	APIFileStatusFailed  = "failed"
)

type APIFile struct {
	ID                 string `json:"id" gorm:"primaryKey;type:varchar(64)"`
	TenantID           string `json:"-" gorm:"type:varchar(128);index:idx_api_files_owner,priority:1"`
	UserID             int    `json:"-" gorm:"index:idx_api_files_owner,priority:2"`
	Purpose            string `json:"purpose" gorm:"type:varchar(64);index"`
	Filename           string `json:"filename" gorm:"type:varchar(512)"`
	Bytes              int64  `json:"bytes"`
	ChecksumSHA256     string `json:"checksum_sha256" gorm:"type:char(64)"`
	MediaType          string `json:"media_type" gorm:"type:varchar(255)"`
	StorageKey         string `json:"-" gorm:"type:varchar(1024);uniqueIndex"`
	ChannelID          int    `json:"channel_id,omitempty" gorm:"index"`
	ProviderType       int    `json:"provider_type,omitempty" gorm:"index"`
	UpstreamFileID     string `json:"upstream_file_id,omitempty" gorm:"type:varchar(255);index"`
	Status             string `json:"status" gorm:"type:varchar(32);index"`
	CreatedAt          int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt          int64  `json:"updated_at" gorm:"autoUpdateTime"`
	ExpiresAt          int64  `json:"expires_at,omitempty" gorm:"index"`
	DeletedAtTimestamp int64  `json:"-" gorm:"column:deleted_at_timestamp;index"`
}

func CreateAPIFile(file *APIFile) error {
	if file.Status == "" {
		file.Status = APIFileStatusPending
	}
	return DB.Create(file).Error
}

func GetOwnedAPIFile(tenantID string, userID int, id string) (*APIFile, error) {
	var file APIFile
	err := DB.Where("id = ? AND tenant_id = ? AND user_id = ? AND status <> ?", id, tenantID, userID, APIFileStatusDeleted).
		First(&file).Error
	if err != nil {
		return nil, err
	}
	return &file, nil
}

func ListAPIFiles(tenantID string, userID int, after string, limit int) ([]APIFile, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := DB.Where("tenant_id = ? AND user_id = ? AND status <> ?", tenantID, userID, APIFileStatusDeleted)
	if after != "" {
		query = query.Where("id < ?", after)
	}
	var files []APIFile
	if err := query.Order("id DESC").Limit(limit).Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

func MarkAPIFileDeleted(tenantID string, userID int, id string) (bool, error) {
	result := DB.Model(&APIFile{}).
		Where("id = ? AND tenant_id = ? AND user_id = ? AND status <> ?", id, tenantID, userID, APIFileStatusDeleted).
		Updates(map[string]any{"status": APIFileStatusDeleted, "deleted_at_timestamp": time.Now().Unix()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func DeleteAPIFileRecord(id string) error {
	return DB.Unscoped().Delete(&APIFile{}, "id = ?", id).Error
}

func IsAPIFileNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
