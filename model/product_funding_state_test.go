package model

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestPendingFundingMetadataFailureCannotFallBackToLocalWallet(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("product_registration_pending", true).Error)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("fail_pending_metadata", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("metadata unavailable"))
		}
	}))
	state, e := ProductFundingState(context.Background(), 42)
	require.ErrorContains(t, e, "metadata unavailable")
	require.NotEqual(t, "local", state)
}
