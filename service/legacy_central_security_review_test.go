package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMixedLegacyPasswordChangePreservesCentralAuthority(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.ProductAuthLifecycle{}, &model.UniHubIdentityMapping{}))
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("password", hash).Error)
	var hub *httptest.Server
	var lifecycleCalls atomic.Int32
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/lifecycle") {
			lifecycleCalls.Add(1)
			_, _ = w.Write([]byte(`{"auth_version":2}`))
			return
		}
		access := userkit.Access{Issuer: hub.URL, Subject: "subject", User: userkit.User{ID: "subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(user.Id), UserID: "subject", ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 7, ProductRole: 1}
		raw, e := common.Marshal(identity)
		if e != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "subject"}}, user.Id)
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, AccessVersion: 1, Active: true, BindingID: "binding", Issuer: hub.URL, Subject: "subject", ProductID: "llmrouter", InstanceID: "default"}).Error)
	now := time.Now().Unix()
	legacy := model.UserSession{SID: "legacy", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "a", LoginMethod: "password", ExpiresAt: now + 600}
	central := model.UserSession{SID: "central", UserID: user.Id, Version: 1, UserAuthVersion: 7, AuthRealm: model.CentralAuthRealm, Status: model.UserSessionStatusActive, RefreshHash: "b", LoginMethod: "product_broker", ExpiresAt: now + 600}
	require.NoError(t, model.DB.Create(&legacy).Error)
	require.NoError(t, model.DB.Create(&central).Error)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", hub.URL)
	t.Setenv("UNIHUB_PRODUCT_PUBLIC_ORIGIN", hub.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	centralID := AuthIdentity{UserID: user.Id, SessionID: central.SID, SessionVersion: 1, UserAuthVersion: 7}
	require.NoError(t, VerifyCentralRealmAuthority(context.Background(), centralID))
	_, err = ChangePasswordForCurrentSession(AuthIdentity{UserID: user.Id, SessionID: legacy.SID, SessionVersion: 1, UserAuthVersion: 1}, "", &model.User{Id: user.Id, Password: "new-password"}, "old-password")
	require.ErrorIs(t, err, model.ErrLegacyCentralSecurityConflict)
	_, _, err = ValidateLoginSession(centralID)
	require.NoError(t, err)
	require.NoError(t, VerifyCentralRealmAuthority(context.Background(), centralID))
	var count int64
	require.NoError(t, model.DB.Model(&model.ProductAuthLifecycle{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
	assert.Zero(t, lifecycleCalls.Load())
}

func TestCentralStateWaitsForPendingLegacyLifecycle(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	var calls atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"auth_version":2}`))
	}))
	t.Cleanup(hub.Close)
	_, err := model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "subject"}}, user.Id)
	require.NoError(t, err)
	access := userkit.Access{Issuer: hub.URL, Subject: "subject", User: userkit.User{ID: "subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
	identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(user.Id), UserID: "subject", ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 2, ProductRole: 1}
	event := model.ProductAuthLifecycle{ID: "auth-version:1:2", UserID: user.Id, PreviousVersion: 1, NextVersion: 2}
	require.NoError(t, model.DB.Create(&event).Error)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", hub.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	require.Error(t, model.RecordCentralAuthState(context.Background(), user.Id, identity, access))
	var count int64
	require.NoError(t, model.DB.Model(&model.CentralAuthState{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
	assert.Zero(t, calls.Load())
	require.NoError(t, model.SyncProductAuthVersions(context.Background(), user.Id))
	assert.EqualValues(t, 1, calls.Load())
	require.NoError(t, model.DB.First(&event, "id = ?", event.ID).Error)
	require.True(t, event.Delivered)
	stale := identity
	stale.AuthVersion = 1 // the central response was obtained before the delivered event
	require.Error(t, model.RecordCentralAuthState(context.Background(), user.Id, stale, access))
	require.NoError(t, model.DB.Model(&model.CentralAuthState{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count, "a pre-delivery response cannot create stale central authority")
	require.NoError(t, model.RecordCentralAuthState(context.Background(), user.Id, identity, access))
}
