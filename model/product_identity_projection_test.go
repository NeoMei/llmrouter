package model

import (
	"context"
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCentralIdentityProjectionReenablesWithoutReplacingCredentials(t *testing.T) {
	previous, redis := DB, common.RedisEnabled
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/projection.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&User{}, &UniHubIdentityMapping{}))
	DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { DB = previous; common.RedisEnabled = redis })
	u := User{Username: "native", Password: "native-hash", AffCode: "native-aff", Role: 100, Status: common.UserStatusDisabled, AuthVersion: 7, Quota: 77}
	require.NoError(t, db.Create(&u).Error)
	_, e = BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central"}}, u.Id)
	require.NoError(t, e)
	u.IdentitySource = UniHubIdentitySource
	u.UniHubUserID = "central"
	active := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: strconv.Itoa(u.Id), UserID: "central", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "central", User: userkit.User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default", ProductRole: 1, Active: active, ExpiresAt: time.Now().Add(time.Minute).Unix(), Permissions: []string{"product:use"}}, ProductRole: 1, AuthVersion: 7})
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	require.NoError(t, ReconcileProductIdentity(context.Background(), &u))
	require.Equal(t, common.UserStatusEnabled, u.Status)
	require.Equal(t, 1, u.Role)
	require.EqualValues(t, 7, u.AuthVersion)
	require.Equal(t, "native-hash", u.Password)
	require.Equal(t, 77, u.Quota)
	active = false
	require.NoError(t, ReconcileProductIdentity(context.Background(), &u))
	require.Equal(t, common.UserStatusDisabled, u.Status)
}
