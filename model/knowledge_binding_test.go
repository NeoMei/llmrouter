package model

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestKnowledgePortableMigrationPreservesNative(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/migration.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	// Actual upgrade of the prior schema and native row, not a fresh-only schema.
	require.NoError(t, db.AutoMigrate(&priorKnowledgeRoute{}))
	require.NoError(t, db.Exec(`INSERT INTO client_model_routes VALUES (1,'legacy','Original','all','[]','{"embedding_space":{"input_semantics":"query-document-v1"}}',1,10,20)`).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ClientModelRoute{}, &ClientModelTarget{}, &KnowledgeActiveBinding{}, &KnowledgeRoutePin{}))
	}
	var native ClientModelRoute
	require.NoError(t, db.First(&native, 1).Error)
	assert.Equal(t, "legacy", native.PublicId)
	assert.Empty(t, native.KnowledgeGeneration)
	assert.EqualValues(t, 10, native.CreatedTime)
	assert.EqualValues(t, 20, native.UpdatedTime)
	assert.Contains(t, native.Metadata, "query-document-v1")
	// Composite scope prevents a second active pointer for the same binding.
	row := KnowledgeActiveBinding{InstanceID: "default", OriginProductID: "superwagie", Model: "unihub-text-embedding-v4", WireOperation: "embeddings.create", ActivePin: "one"}
	require.NoError(t, db.Create(&row).Error)
	assert.Error(t, db.Create(&row).Error)
}

type priorKnowledgeRoute struct {
	Id          int    `gorm:"primaryKey"`
	PublicId    string `gorm:"size:191;uniqueIndex;not null"`
	DisplayName string `gorm:"size:191;not null"`
	Client      string `gorm:"size:32;index;not null;default:all"`
	Aliases     string `gorm:"type:text"`
	Metadata    string `gorm:"type:text"`
	Enabled     bool   `gorm:"index;not null;default:true"`
	CreatedTime int64
	UpdatedTime int64
}

func (priorKnowledgeRoute) TableName() string { return "client_model_routes" }
