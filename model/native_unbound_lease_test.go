package model

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestUnboundNativeLeaseAndIdentityBindingSerialize(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var db *gorm.DB
			var err error
			if dialect == "postgres" {
				raw := os.Getenv("NATIVE_COMPAT_TEST_POSTGRES")
				if raw == "" {
					t.Skip("isolated PostgreSQL not configured")
				}
				admin, e := gorm.Open(postgres.Open(raw), &gorm.Config{})
				require.NoError(t, e)
				schema := fmt.Sprintf("native_lease_%d", time.Now().UnixNano())
				require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
				t.Cleanup(func() {
					require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
					sql, e := admin.DB()
					if e == nil {
						_ = sql.Close()
					}
				})
				parsed, e := url.Parse(raw)
				require.NoError(t, e)
				q := parsed.Query()
				q.Set("search_path", schema)
				parsed.RawQuery = q.Encode()
				db, err = gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
			} else {
				db, err = gorm.Open(sqlite.Open(t.TempDir()+"/lease.db?_pragma=busy_timeout(5000)"), &gorm.Config{})
			}
			require.NoError(t, err)
			sql, e := db.DB()
			require.NoError(t, e)
			sql.SetMaxOpenConns(4)
			t.Cleanup(func() { _ = sql.Close() })
			require.NoError(t, db.AutoMigrate(&User{}, &UniHubIdentityMapping{}, &ProductAuthLifecycle{}, &ProductFundingAccount{}, &ProductLegacyFundingLease{}))
			previous, previousType := DB, common.MainDatabaseType()
			DB = db
			if dialect == "postgres" {
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			} else {
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			}
			t.Cleanup(func() { DB = previous; common.SetMainDatabaseType(previousType) })
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
			require.NoError(t, db.Create(&User{Id: 91, Username: "fixture", AffCode: "fixture", Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 500}).Error)
			start := make(chan struct{})
			var wg sync.WaitGroup
			var leaseErr, bindErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				leaseErr = BeginUnboundNativeFundingLease(context.Background(), "fixture-call", 91)
			}()
			go func() {
				defer wg.Done()
				<-start
				_, bindErr = BindUniHubIdentity(platformaccount.Identity{Issuer: "https://fixture.test", User: platformaccount.User{ID: "fixture-actor"}}, 91)
			}()
			close(start)
			wg.Wait()
			assert.NotEqual(t, leaseErr == nil, bindErr == nil, "only one of lease admission and identity binding may commit")
			var user User
			require.NoError(t, db.First(&user, 91).Error)
			var active int64
			require.NoError(t, db.Model(&ProductLegacyFundingLease{}).Where("user_id = ? AND state = ?", 91, "active").Count(&active).Error)
			if leaseErr == nil {
				assert.EqualValues(t, 1, active)
				assert.Empty(t, user.IdentitySource)
			} else {
				assert.Zero(t, active)
				assert.Equal(t, UniHubIdentitySource, user.IdentitySource)
			}

			require.NoError(t, db.Create(&User{Id: 92, Username: "cutover", AffCode: "cutover", Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 500}).Error)
			start = make(chan struct{})
			var cutoverErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				leaseErr = BeginUnboundNativeFundingLease(context.Background(), "cutover-call", 92)
			}()
			go func() {
				defer wg.Done()
				<-start
				cutoverErr = db.Transaction(func(tx *gorm.DB) error {
					var locked User
					if e := lockForUpdate(tx).First(&locked, 92).Error; e != nil {
						return e
					}
					if e := productLegacyDrained(tx, 92); e != nil {
						return e
					}
					return tx.Create(&ProductFundingAccount{UserID: 92, State: "migrating"}).Error
				})
			}()
			close(start)
			wg.Wait()
			assert.NotEqual(t, leaseErr == nil, cutoverErr == nil, "only one of lease admission and funding cutover may commit")
			require.NoError(t, db.Model(&ProductLegacyFundingLease{}).Where("user_id = ? AND state = ?", 92, "active").Count(&active).Error)
			var accounts int64
			require.NoError(t, db.Model(&ProductFundingAccount{}).Where("user_id = ?", 92).Count(&accounts).Error)
			if leaseErr == nil {
				assert.EqualValues(t, 1, active)
				assert.Zero(t, accounts)
			} else {
				assert.Zero(t, active)
				assert.EqualValues(t, 1, accounts)
			}
		})
	}
}
