package model

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProfileUpdateCannotRestoreStaleIdentityProjection(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&UniHubIdentityMapping{}))
	var stale User
	require.NoError(t, db.First(&stale, 42).Error)
	stale.IdentitySource = ProductIdentityPending
	stale.UniHubUserID = "stale-subject"
	stale.ProductRegistrationPending = true
	stale.ProductJITPending = true
	stale.DisplayName = "new profile display"
	_, err := BindUniHubIdentity(platformaccount.Identity{Issuer: "https://identity.test", User: platformaccount.User{ID: "current-subject"}}, 42)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return stale.UpdateWithTx(tx, false) }))
	var current User
	require.NoError(t, db.First(&current, 42).Error)
	require.Equal(t, "new profile display", current.DisplayName)
	require.Equal(t, UniHubIdentitySource, current.IdentitySource)
	require.Equal(t, "current-subject", current.UniHubUserID)
	require.False(t, current.ProductRegistrationPending)
	require.False(t, current.ProductJITPending)
	projected, err := CheckUniHubProjection(42)
	require.NoError(t, err)
	require.True(t, projected)
}
