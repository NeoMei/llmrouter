package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type providerSeedFixture struct {
	profile       string
	modelID       string
	contextWindow int
	maxOutput     int
}

func openProviderSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
	require.NoError(t, db.AutoMigrate(&ProviderProfile{}, &ProviderModel{}))
	return db
}

func insertProviderSeedFixture(t *testing.T, db *gorm.DB, fixture providerSeedFixture) {
	t.Helper()
	var profile ProviderProfile
	lookup := db.Where("name = ?", fixture.profile).Limit(1).Find(&profile)
	require.NoError(t, lookup.Error)
	if lookup.RowsAffected == 0 {
		profile = ProviderProfile{
			Name:        fixture.profile,
			DisplayName: fixture.profile,
			BaseURL:     "https://" + fixture.profile + ".example.test",
			AuthMode:    "api_key",
			Status:      "active",
		}
		require.NoError(t, db.Create(&profile).Error)
	}
	require.NoError(t, db.Create(&ProviderModel{
		ProfileId:     profile.Id,
		ModelId:       fixture.modelID,
		DisplayName:   fixture.modelID,
		ContextWindow: fixture.contextWindow,
		MaxOutput:     fixture.maxOutput,
		Status:        "active",
	}).Error)
}

func requireProviderSeedLimits(t *testing.T, db *gorm.DB, fixture providerSeedFixture) {
	t.Helper()
	var profile ProviderProfile
	require.NoError(t, db.Where("name = ?", fixture.profile).First(&profile).Error)
	var modelRow ProviderModel
	require.NoError(t, db.Where("profile_id = ? AND model_id = ?", profile.Id, fixture.modelID).First(&modelRow).Error)
	require.Equal(t, fixture.contextWindow, modelRow.ContextWindow, "context window for %s/%s", fixture.profile, fixture.modelID)
	require.Equal(t, fixture.maxOutput, modelRow.MaxOutput, "max output for %s/%s", fixture.profile, fixture.modelID)
}

func TestSeedProviderProfilesIsIdempotentAndPreservesEdits(t *testing.T) {
	db := openProviderSeedTestDB(t)

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

func TestUpgradeDeepSeekProviderProfile(t *testing.T) {
	tests := []struct {
		name         string
		freshInstall bool
		runTwice     bool
		rows         []providerSeedFixture
		want         []providerSeedFixture
	}{
		{
			name: "old seed upgrade",
			rows: []providerSeedFixture{{"deepseek", "deepseek-v4-pro", 131072, 0}},
			want: []providerSeedFixture{{"deepseek", "deepseek-v4-pro", 1048576, 393216}},
		},
		{
			name: "both Flash IDs upgrade",
			rows: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 131072, 0},
				{"deepseek", "deepseek-v4-flash", 131072, 0},
			},
			want: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 1048576, 393216},
				{"deepseek", "deepseek-v4-flash", 1048576, 393216},
			},
		},
		{
			name: "context edited preserved while output fixed",
			rows: []providerSeedFixture{{"deepseek", "deepseek-flash", 200000, 0}},
			want: []providerSeedFixture{{"deepseek", "deepseek-flash", 200000, 393216}},
		},
		{
			name: "output edited preserved while context fixed",
			rows: []providerSeedFixture{{"deepseek", "deepseek-v4-pro", 131072, 8192}},
			want: []providerSeedFixture{{"deepseek", "deepseek-v4-pro", 1048576, 8192}},
		},
		{
			name: "both edited preserved",
			rows: []providerSeedFixture{{"deepseek", "deepseek-v4-flash", 200000, 8192}},
			want: []providerSeedFixture{{"deepseek", "deepseek-v4-flash", 200000, 8192}},
		},
		{
			name: "unrelated profile untouched",
			rows: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 131072, 0},
				{"other", "deepseek-flash", 131072, 0},
			},
			want: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 1048576, 393216},
				{"other", "deepseek-flash", 131072, 0},
			},
		},
		{
			name: "current values unchanged",
			rows: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 1048576, 393216},
				{"deepseek", "deepseek-v4-pro", 1048576, 393216},
			},
			want: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 1048576, 393216},
				{"deepseek", "deepseek-v4-pro", 1048576, 393216},
			},
		},
		{
			name:     "second run idempotent",
			runTwice: true,
			rows: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 131072, 0},
				{"deepseek", "deepseek-v4-flash", 131072, 0},
				{"deepseek", "deepseek-v4-pro", 131072, 0},
			},
			want: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 1048576, 393216},
				{"deepseek", "deepseek-v4-flash", 1048576, 393216},
				{"deepseek", "deepseek-v4-pro", 1048576, 393216},
			},
		},
		{
			name:         "fresh install values",
			freshInstall: true,
			want: []providerSeedFixture{
				{"deepseek", "deepseek-flash", 1048576, 393216},
				{"deepseek", "deepseek-v4-pro", 1048576, 393216},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openProviderSeedTestDB(t)
			if tc.freshInstall {
				require.NoError(t, SeedProviderProfiles())
			} else {
				for _, row := range tc.rows {
					insertProviderSeedFixture(t, db, row)
				}
			}

			require.NoError(t, UpgradeDeepSeekProviderProfile())
			if tc.runTwice {
				require.NoError(t, UpgradeDeepSeekProviderProfile())
			}

			for _, want := range tc.want {
				requireProviderSeedLimits(t, db, want)
			}
			var count int64
			if tc.freshInstall {
				var profile ProviderProfile
				require.NoError(t, db.Where("name = ?", "deepseek").First(&profile).Error)
				require.NoError(t, db.Model(&ProviderModel{}).Where("profile_id = ?", profile.Id).Count(&count).Error)
				require.Equal(t, int64(len(tc.want)), count)
			} else {
				require.NoError(t, db.Model(&ProviderModel{}).Count(&count).Error)
				require.Equal(t, int64(len(tc.rows)), count)
			}
		})
	}
}
