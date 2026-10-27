package main

import (
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Run the real CLI entry point in a fresh process, including its default global
// Redis state. Calling run directly would hide the startup initialization bug.
func TestMigrationCLIProcess(t *testing.T) {
	if os.Getenv("MIGRATION_CLI_PROCESS") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("product-runtime-migrate", flag.ExitOnError)
	os.Args = []string{"product-runtime-migrate", "-manifest", os.Getenv("MIGRATION_TEST_MANIFEST"), "-sqlite-path", os.Getenv("MIGRATION_TEST_DATABASE"), "-apply"}
	main()
}

func TestMigrationCLIInitializesRedisAndPreservesCachedQuota(t *testing.T) {
	for _, mode := range []string{"disabled", "configured", "invalid", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "original.db")
			db, e := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
			require.NoError(t, e)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.ProductFundingAccount{}, &model.UniHubIdentityMapping{}))
			u := model.User{Id: 42, Username: "original", Role: 1, Status: 1, Quota: 500, AuthVersion: 1, AffCode: "original", Password: "unchanged-test-password-hash"}
			require.NoError(t, db.Create(&u).Error)
			manifest := migrationManifest{BatchID: "reviewed", Users: []migrationUser{{LocalUserID: 42, ExpectedUsername: "original", ExpectedRole: 1, ExpectedAuthVersion: 1, ExpectedQuota: 500}}}
			raw, e := common.Marshal(manifest)
			require.NoError(t, e)
			manifestPath := filepath.Join(dir, "reviewed.json")
			require.NoError(t, os.WriteFile(manifestPath, raw, 0o600))
			central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/internal/v1/product-runtime/identity/import", r.URL.Path)
				out := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: "42", UserID: "central-user", ProductID: "llmrouter", InstanceID: "default", PersonalAccountID: "payer"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "central-user", User: userkit.User{ID: "central-user"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ProductRole: 1, Permissions: []string{"product:use"}}, AuthVersion: 1, ProductRole: 1}
				body, e := common.Marshal(out)
				require.NoError(t, e)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer central.Close()
			redisURL := ""
			var cache *miniredis.Miniredis
			if mode == "configured" {
				cache = miniredis.RunT(t)
				cache.HSet("user:42", "Quota", "123")
				redisURL = "redis://" + cache.Addr() + "/0"
			}
			if mode == "invalid" {
				redisURL = "redis://migration-secret-marker@%zz"
			}
			if mode == "unavailable" {
				listener, e := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, e)
				redisURL = "redis://migration-secret-marker@" + listener.Addr().String() + "/0"
				require.NoError(t, listener.Close())
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestMigrationCLIProcess$")
			cmd.Env = append(os.Environ(), "MIGRATION_CLI_PROCESS=1", "MIGRATION_TEST_MANIFEST="+manifestPath, "MIGRATION_TEST_DATABASE="+dbPath, "SQL_DSN=", "LOG_SQL_DSN=", "DEBUG=false", "REDIS_CONN_STRING="+redisURL, "UNIHUB_PRODUCT_RUNTIME_ENABLED=true", "UNIHUB_ORIGIN=https://hub.test", "UNIHUB_PRODUCT_PUBLIC_ORIGIN=https://hub.test", "UNIHUB_PRODUCT_API_ORIGIN="+central.URL, "UNIHUB_PRODUCT_RUNTIME_TOKEN="+strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID=llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID=default")
			output, e := cmd.CombinedOutput()
			if mode == "invalid" || mode == "unavailable" {
				require.Error(t, e)
				require.NotContains(t, string(output), "migration-secret-marker")
			} else {
				require.NoError(t, e, string(output))
			}
			var updated model.User
			require.NoError(t, db.First(&updated, 42).Error)
			require.Equal(t, 500, updated.Quota)
			require.Equal(t, "unchanged-test-password-hash", updated.Password)
			if mode == "invalid" || mode == "unavailable" {
				require.Empty(t, updated.IdentitySource)
			} else {
				require.Equal(t, model.UniHubIdentitySource, updated.IdentitySource)
				require.Equal(t, "central-user", updated.UniHubUserID)
			}
			if cache != nil {
				require.Equal(t, "123", cache.HGet("user:42", "Quota"))
				require.Equal(t, "unihub", cache.HGet("user:42", "IdentitySource"))
				require.Equal(t, "central-user", cache.HGet("user:42", "UniHubUserID"))
			}
			sqlDB, e := db.DB()
			require.NoError(t, e)
			require.NoError(t, sqlDB.Close())
		})
	}
}
