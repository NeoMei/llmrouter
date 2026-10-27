package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductCutoverFencesNativeAdmissionBeforeCreatingLocalJournal(t *testing.T) {
	n, old, a, _ := nativeFixture(t)
	require.NoError(t, old.db.AutoMigrate(&ProductFundingAccount{}, &ProductRuntimeCall{}))
	require.NoError(t, old.db.Create(&ProductFundingAccount{UserID: a.UserID, AccountID: "payer", State: "migrating"}).Error)
	before := int64(0)
	require.NoError(t, old.db.Model(&NativeBilling{}).Count(&before).Error)
	_, err := n.Begin(context.Background(), a)
	require.Error(t, err)
	after := int64(0)
	require.NoError(t, old.db.Model(&NativeBilling{}).Count(&after).Error)
	require.Equal(t, before, after)
}
