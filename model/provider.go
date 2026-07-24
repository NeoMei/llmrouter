package model

import "time"

// ProviderProfile maps to the provider_profiles table.
type ProviderProfile struct {
	Id              int       `json:"id" gorm:"primaryKey;autoIncrement"`
	Name            string    `json:"name" gorm:"type:varchar(64);uniqueIndex"`
	DisplayName     string    `json:"display_name" gorm:"type:varchar(128)"`
	BaseURL         string    `json:"base_url" gorm:"type:varchar(512)"`
	AuthMode        string    `json:"auth_mode" gorm:"type:varchar(16)"`
	AdapterType     string    `json:"adapter_type" gorm:"type:varchar(32)"`
	DiscoveryURL    string    `json:"discovery_url" gorm:"type:varchar(512)"`
	DiscoveryParser string    `json:"discovery_parser" gorm:"type:varchar(32)"`
	Status          string    `json:"status" gorm:"type:varchar(16)"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (ProviderProfile) TableName() string { return "provider_profiles" }

// ProviderModel maps to the provider_models table.
// InputModes and QuirkRules are TEXT (JSON string) for cross-DB compatibility.
type ProviderModel struct {
	Id            int        `json:"id" gorm:"primaryKey;autoIncrement"`
	ProfileId     int        `json:"profile_id" gorm:"not null;uniqueIndex:idx_pm_profile_model"`
	ModelId       string     `json:"model_id" gorm:"type:varchar(128);not null;uniqueIndex:idx_pm_profile_model;index"`
	DisplayName   string     `json:"display_name" gorm:"type:varchar(128)"`
	ContextWindow int        `json:"context_window"`
	MaxOutput     int        `json:"max_output"`
	InputModes    string     `json:"input_modes" gorm:"type:text"`
	QuirkRules    string     `json:"quirk_rules" gorm:"type:text"`
	Status        string     `json:"status" gorm:"type:varchar(16)"`
	DiscoveredAt  *time.Time `json:"discovered_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (ProviderModel) TableName() string { return "provider_models" }

// UserProviderKey maps to the user_provider_keys table (BYOK).
// EncryptedKey is excluded from JSON output (json:"-").
type UserProviderKey struct {
	Id           int       `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId       int       `json:"user_id" gorm:"not null;index"`
	ProfileId    int       `json:"profile_id" gorm:"not null"`
	EncryptedKey []byte    `json:"-" gorm:"not null"`
	KeyLabel     string    `json:"key_label" gorm:"type:varchar(128)"`
	Status       string    `json:"status" gorm:"type:varchar(16)"`
	CreatedAt    time.Time `json:"created_at"`
}

func (UserProviderKey) TableName() string { return "user_provider_keys" }

func (k *UserProviderKey) Insert() error {
	return DB.Create(k).Error
}

func GetUserProviderKeys(userId int) ([]UserProviderKey, error) {
	var keys []UserProviderKey
	err := DB.Where("user_id = ?", userId).Order("id DESC").Find(&keys).Error
	return keys, err
}

// DeleteUserProviderKeyById deletes a BYOK key scoped to the owning user.
// Returns false if no row was affected (wrong user or not found).
func DeleteUserProviderKeyById(id, userId int) (bool, error) {
	result := DB.Where("id = ? AND user_id = ?", id, userId).Delete(&UserProviderKey{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ProviderKeyPool maps to the provider_key_pool table (platform keys).
// EncryptedKey is excluded from JSON output (json:"-").
type ProviderKeyPool struct {
	Id            int        `json:"id" gorm:"primaryKey;autoIncrement"`
	ProfileId     int        `json:"profile_id" gorm:"not null;index:idx_pkp_profile_status"`
	EncryptedKey  []byte     `json:"-" gorm:"not null"`
	QuotaWindows  string     `json:"quota_windows" gorm:"type:text"`
	Status        string     `json:"status" gorm:"type:varchar(16);index:idx_pkp_profile_status"`
	CooldownUntil *time.Time `json:"cooldown_until"`
	LastError     string     `json:"last_error" gorm:"type:text"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (ProviderKeyPool) TableName() string { return "provider_key_pool" }

func (k *ProviderKeyPool) Insert() error {
	return DB.Create(k).Error
}

func GetProviderKeyPools(profileId int) ([]ProviderKeyPool, error) {
	var keys []ProviderKeyPool
	q := DB.Order("id DESC")
	if profileId > 0 {
		q = q.Where("profile_id = ?", profileId)
	}
	err := q.Find(&keys).Error
	return keys, err
}
