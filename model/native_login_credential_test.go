package model

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestNativeLoginCredentialRequiresExplicitBindingAndCannotSurviveRebind(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&NativeLoginCredential{}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	bind := func(value, realm string) error {
		return db.Transaction(func(tx *gorm.DB) error { return SetNativeLoginCredentialTx(tx, 42, "oidc_id", value, realm) })
	}
	require.Error(t, bind("verified-subject", ""))
	require.NoError(t, bind("verified-subject", NativeFundingLocalOnly))
	native, err := IsNativeLoginCredential(context.Background(), 42, "oidc_id", "verified-subject")
	require.NoError(t, err)
	assert.True(t, native)
	native, err = IsNativeLoginCredential(context.Background(), 42, "oidc_id", "different")
	require.Error(t, err)
	assert.False(t, native)
	require.NoError(t, bind("new-subject", NativeFundingLocalOnly))
	_, err = IsNativeLoginCredential(context.Background(), 42, "oidc_id", "verified-subject")
	require.Error(t, err)
	require.NoError(t, bind("", NativeFundingLocalOnly))
	_, err = IsNativeLoginCredential(context.Background(), 42, "oidc_id", "new-subject")
	require.Error(t, err)
}

func TestNativeOAuthAndPasskeyBindingLifecycle(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&NativeLoginCredential{}, &UserOAuthBinding{}, &PasskeyCredential{}, &ProductAuthLifecycle{}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true, "auth_version": 1}).Error)
	require.Error(t, UpdateUserBindColumn(42, "oidc_id", "central-attempt", ""))
	require.NoError(t, UpdateUserBindColumn(42, "oidc_id", "native-subject", NativeFundingLocalOnly))
	native, err := IsNativeLoginCredential(context.Background(), 42, "oidc_id", "native-subject")
	require.NoError(t, err)
	assert.True(t, native)
	require.Error(t, UpdateUserOAuthBinding(42, 17, "central-attempt", ""))
	require.NoError(t, UpdateUserOAuthBinding(42, 17, "custom-subject", NativeFundingLocalOnly))
	native, err = IsNativeLoginCredential(context.Background(), 42, "oauth:17", "custom-subject")
	require.NoError(t, err)
	assert.True(t, native)
	require.Error(t, DeleteUserOAuthBinding(42, 17, ""))
	require.NoError(t, DeleteUserOAuthBinding(42, 17, NativeFundingLocalOnly))
	_, err = IsNativeLoginCredential(context.Background(), 42, "oauth:17", "custom-subject")
	require.Error(t, err)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	require.Error(t, user.ClearBinding("oidc", ""))
	require.NoError(t, user.ClearBinding("oidc", NativeFundingLocalOnly))
	_, err = IsNativeLoginCredential(context.Background(), 42, "oidc_id", "native-subject")
	require.Error(t, err)
	credential := &PasskeyCredential{UserID: 42, CredentialID: "synthetic-credential", PublicKey: "synthetic-public"}
	require.Error(t, UpsertPasskeyCredentialWithAuthVersion(credential, ""))
	require.NoError(t, UpsertPasskeyCredentialWithAuthVersion(credential, NativeFundingLocalOnly))
	native, err = IsNativeLoginCredential(context.Background(), 42, "passkey", credential.CredentialID)
	require.NoError(t, err)
	assert.True(t, native)
	require.Error(t, DeletePasskeyByUserIDWithAuthVersion(42, ""))
	require.NoError(t, DeletePasskeyByUserIDWithAuthVersion(42, NativeFundingLocalOnly))
	_, err = IsNativeLoginCredential(context.Background(), 42, "passkey", credential.CredentialID)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&PasskeyCredential{}).Where("user_id = ?", 42).Count(&count).Error)
	assert.Zero(t, count)
}

func TestNativeRealmBulkSubscriptionResetPartitionsMixedOwners(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&SubscriptionPlan{}, &UserSubscription{}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("funding_realm", NativeFundingLocalOnly).Error)
	require.NoError(t, db.Create(&User{Id: 43, Username: "central", AffCode: "central", Status: 1}).Error)
	plan := SubscriptionPlan{Title: "shared-plan", QuotaResetPeriod: SubscriptionResetDaily}
	require.NoError(t, db.Create(&plan).Error)
	now := GetDBTimestamp()
	for _, id := range []int{42, 43} {
		require.NoError(t, db.Create(&UserSubscription{UserId: id, PlanId: plan.Id, Status: "active", EndTime: now + 1000, AmountTotal: 100, AmountUsed: 40}).Error)
	}
	result, err := AdminResetPlanSubscriptions(plan.Id, false, NativeFundingLocalOnly)
	require.NoError(t, err)
	assert.Equal(t, 1, result.ResetCount)
	var native, central UserSubscription
	require.NoError(t, db.Where("user_id = ?", 42).First(&native).Error)
	require.NoError(t, db.Where("user_id = ?", 43).First(&central).Error)
	assert.Zero(t, native.AmountUsed)
	assert.EqualValues(t, 40, central.AmountUsed)
	result, err = AdminResetPlanSubscriptions(plan.Id, false, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.ResetCount)
	require.NoError(t, db.First(&central, central.Id).Error)
	assert.Zero(t, central.AmountUsed)
}
