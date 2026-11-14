package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSeedProviderProfilesIsIdempotentAndPreservesEdits(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
	require.NoError(t, db.AutoMigrate(&ProviderProfile{}, &ProviderModel{}))

	require.NoError(t, SeedProviderProfiles())
	var profile ProviderProfile
	require.NoError(t, db.Where("name = ?", "deepseek").First(&profile).Error)
	require.NoError(t, db.Model(&profile).Update("base_url", "https://operator.example.test").Error)
	var modelRow ProviderModel
	require.NoError(t, db.Where("profile_id = ? AND model_id = ?", profile.Id, "deepseek-flash").First(&modelRow).Error)
	require.NoError(t, db.Model(&modelRow).Update("display_name", "Operator-owned name").Error)

	require.NoError(t, SeedProviderProfiles())
	require.NoError(t, db.Where("name = ?", "deepseek").First(&profile).Error)
	require.Equal(t, "https://operator.example.test", profile.BaseURL)
	require.NoError(t, db.Where("profile_id = ? AND model_id = ?", profile.Id, "deepseek-flash").First(&modelRow).Error)
	require.Equal(t, "Operator-owned name", modelRow.DisplayName)
	var count int64
	require.NoError(t, db.Model(&ProviderModel{}).Where("profile_id = ? AND model_id = ?", profile.Id, "deepseek-flash").Count(&count).Error)
	require.Equal(t, int64(1), count)
}
