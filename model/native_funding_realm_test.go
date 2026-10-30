package model

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeFundingRealmPinsWithoutMovingAssets(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old, redis, batch := DB, common.RedisEnabled, common.BatchUpdateEnabled
	DB, common.RedisEnabled, common.BatchUpdateEnabled = db, true, true
	t.Cleanup(func() { DB, common.RedisEnabled, common.BatchUpdateEnabled = old, redis, batch })
	ctx := context.Background()
	require.NoError(t, PinNativeFundingRealm(ctx, 42, "reviewed-cohort:42"))
	require.NoError(t, PinNativeFundingRealm(ctx, 42, "reviewed-cohort:42"))
	require.ErrorIs(t, PinNativeFundingRealm(ctx, 42, "different-evidence"), ErrProductFundingConflict)
	var u User
	require.NoError(t, db.First(&u, 42).Error)
	assert.Equal(t, "LOCAL_ONLY", u.FundingRealm)
	assert.Equal(t, "reviewed-cohort:42", u.FundingRealmEvidence)
	assert.Equal(t, 500, u.Quota)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	assert.Equal(t, "original-key", token.Key)
	assert.Equal(t, 200, token.RemainQuota)
	assert.True(t, common.RedisEnabled)
	assert.True(t, common.BatchUpdateEnabled)
	var accounts int64
	require.NoError(t, db.Model(&ProductFundingAccount{}).Count(&accounts).Error)
	assert.Zero(t, accounts)
}

func TestNativeFundingRealmRejectsPriorCentralOwnershipAndOnboarding(t *testing.T) {
	for _, state := range []string{"central", "migrating", "unknown", "registration", "jit"} {
		t.Run(state, func(t *testing.T) {
			_, db, _ := productFundingFixture(t)
			old := DB
			DB = db
			t.Cleanup(func() { DB = old })
			switch state {
			case "registration":
				require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("product_registration_pending", true).Error)
			case "jit":
				require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("product_jit_pending", true).Error)
			default:
				require.NoError(t, db.Create(&ProductFundingAccount{UserID: 42, State: state, AccountID: "payer"}).Error)
			}
			require.Error(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-cohort:42"))
			var u User
			require.NoError(t, db.First(&u, 42).Error)
			assert.Empty(t, u.FundingRealm)
			assert.Equal(t, 500, u.Quota)
		})
	}
}

func TestNativeFundingRealmBlocksImportAndZeroActivation(t *testing.T) {
	for _, action := range []string{"import", "zero"} {
		t.Run(action, func(t *testing.T) {
			store, db, remote := productFundingFixture(t)
			old, redis, batch := DB, common.RedisEnabled, common.BatchUpdateEnabled
			DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
			t.Cleanup(func() { DB, common.RedisEnabled, common.BatchUpdateEnabled = old, redis, batch })
			ctx := context.Background()
			require.NoError(t, PinNativeFundingRealm(ctx, 42, "reviewed-cohort:42"))
			if action == "zero" {
				require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("quota", 0).Error)
			}
			var err error
			if action == "import" {
				err = store.Import(ctx, 42, "payer", "batch")
			} else {
				err = store.ActivateZero(ctx, 42, "payer")
			}
			require.ErrorIs(t, err, ErrNativeFundingLocalOnly)
			assert.Zero(t, remote.imports)
			var accounts int64
			require.NoError(t, db.Model(&ProductFundingAccount{}).Count(&accounts).Error)
			assert.Zero(t, accounts)
		})
	}
}

func TestNativeFundingRealmPinAndActivationSerialize(t *testing.T) {
	for _, action := range []string{"import", "zero"} {
		t.Run(action, func(t *testing.T) {
			store, db, remote := productFundingFixture(t)
			old, redis, batch := DB, common.RedisEnabled, common.BatchUpdateEnabled
			DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
			t.Cleanup(func() { DB, common.RedisEnabled, common.BatchUpdateEnabled = old, redis, batch })
			conn, err := db.DB()
			require.NoError(t, err)
			conn.SetMaxOpenConns(1)
			if action == "zero" {
				require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("quota", 0).Error)
			}
			ctx := context.Background()
			start := make(chan struct{})
			var wg sync.WaitGroup
			var pinErr, activateErr error
			wg.Add(2)
			go func() { defer wg.Done(); <-start; pinErr = PinNativeFundingRealm(ctx, 42, "reviewed-cohort:42") }()
			go func() {
				defer wg.Done()
				<-start
				if action == "import" {
					activateErr = store.Import(ctx, 42, "payer", "batch")
				} else {
					activateErr = store.ActivateZero(ctx, 42, "payer")
				}
			}()
			close(start)
			wg.Wait()
			require.NotEqual(t, pinErr == nil, activateErr == nil)
			var u User
			require.NoError(t, db.First(&u, 42).Error)
			if pinErr == nil {
				assert.Equal(t, "LOCAL_ONLY", u.FundingRealm)
				assert.Zero(t, remote.imports)
			} else {
				assert.Empty(t, u.FundingRealm)
			}
		})
	}
}

func TestNativeFundingRealmAllowsIdentityMappingWithoutFundingTransfer(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	require.NoError(t, db.AutoMigrate(&UniHubIdentityMapping{}))
	old, redis := DB, common.RedisEnabled
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = old, redis })
	require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-cohort:42"))
	_, err := BindUniHubIdentity(platformaccount.Identity{Issuer: "https://fixture.test", User: platformaccount.User{ID: "central"}}, 42)
	require.NoError(t, err)
	var u User
	require.NoError(t, db.First(&u, 42).Error)
	assert.Equal(t, "LOCAL_ONLY", u.FundingRealm)
	assert.Equal(t, 500, u.Quota)
	var accounts int64
	require.NoError(t, db.Model(&ProductFundingAccount{}).Count(&accounts).Error)
	assert.Zero(t, accounts)
}

func TestNativeFundingRealmBlocksRegistrationAndJITFunding(t *testing.T) {
	for _, jit := range []bool{false, true} {
		name := "registration"
		if jit {
			name = "jit"
		}
		t.Run(name, func(t *testing.T) {
			_, db, _ := productFundingFixture(t)
			require.NoError(t, db.AutoMigrate(&ProductCreditAward{}))
			old, redis, batch := DB, common.RedisEnabled, common.BatchUpdateEnabled
			DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, true
			t.Cleanup(func() { DB, common.RedisEnabled, common.BatchUpdateEnabled = old, redis, batch })
			ctx := context.Background()
			require.NoError(t, PinNativeFundingRealm(ctx, 42, "reviewed-cohort:42"))
			// Simulate a stale onboarding writer resuming after the policy was pinned.
			require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"product_registration_pending": true, "product_jit_pending": jit, "identity_source": UniHubIdentitySource}).Error)
			var user User
			require.NoError(t, db.First(&user, 42).Error)
			var awards int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access") {
					raw, err := common.Marshal(userkit.RuntimeAccess{Binding: userkit.RuntimeBinding{LocalUserID: "42", ProductID: "llmrouter", InstanceID: "default", PersonalAccountID: "payer"}})
					if err != nil {
						w.WriteHeader(500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
					return
				}
				awards++
				w.WriteHeader(500)
			}))
			defer server.Close()
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
			t.Setenv("UNIHUB_ORIGIN", server.URL)
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
			t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
			t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
			var err error
			if jit {
				err = CompleteProductJITOnboarding(ctx, 42)
			} else {
				err = ActivateRegisteredProductFunding(ctx, &user)
			}
			require.ErrorIs(t, err, ErrNativeFundingLocalOnly)
			require.NoError(t, db.First(&user, 42).Error)
			assert.Equal(t, 500, user.Quota)
			assert.Equal(t, "LOCAL_ONLY", user.FundingRealm)
			assert.Zero(t, awards)
			var accounts, credits int64
			require.NoError(t, db.Model(&ProductFundingAccount{}).Count(&accounts).Error)
			require.NoError(t, db.Model(&ProductCreditAward{}).Count(&credits).Error)
			assert.Zero(t, accounts)
			assert.Zero(t, credits)
			assert.True(t, common.BatchUpdateEnabled)
		})
	}
}
