package model

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedCredentialEventIsAtomicAndSurvivesUnavailableAuthority(t *testing.T) {
	old, redis := DB, common.RedisEnabled
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/lifecycle.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &ProductAuthLifecycle{}))
	DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { DB = old; common.RedisEnabled = redis })
	u := User{Username: "native", AffCode: "native", AuthVersion: 1, IdentitySource: UniHubIdentitySource, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&u).Error)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	unavailable := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"auth_version":2}`))
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	require.Error(t, ApplyManagedCredentialChange(u.Id, func(tx *gorm.DB) error { return errors.New("credential transaction failed") }))
	var count int64
	require.NoError(t, db.Model(&ProductAuthLifecycle{}).Count(&count).Error)
	require.Zero(t, count)
	require.Error(t, ApplyManagedCredentialChange(u.Id, func(tx *gorm.DB) error {
		return tx.Model(&User{}).Where("id = ?", u.Id).Update("password", "new-native-hash").Error
	}))
	var stored User
	require.NoError(t, db.First(&stored, u.Id).Error)
	require.EqualValues(t, 2, stored.AuthVersion)
	require.Equal(t, "new-native-hash", stored.Password)
	var event ProductAuthLifecycle
	require.NoError(t, db.First(&event).Error)
	require.False(t, event.Delivered)
	unavailable = false
	require.NoError(t, SyncProductAuthVersions(context.Background(), u.Id))
	require.NoError(t, db.First(&event).Error)
	require.True(t, event.Delivered)
	require.NoError(t, SyncProductAuthVersions(context.Background(), u.Id))
}
