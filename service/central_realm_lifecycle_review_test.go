package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCentralSessionUsesIndependentDurableState(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.Exec(`CREATE TABLE central_auth_states (user_id integer primary key, auth_version bigint not null, active boolean not null)`).Error)
	require.NoError(t, model.DB.Exec(`INSERT INTO central_auth_states (user_id, auth_version, active) VALUES (?, ?, ?)`, user.Id, 7, true).Error)
	now := time.Now().Unix()
	session := model.UserSession{SID: "central-independent", UserID: user.Id, Version: 1, UserAuthVersion: 7,
		AuthRealm: "CENTRAL", Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600}
	require.NoError(t, model.DB.Create(&session).Error)
	identity := AuthIdentity{UserID: user.Id, SessionID: session.SID, SessionVersion: 1, UserAuthVersion: 7}
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"auth_version": 2, "status": common.UserStatusDisabled}).Error)
	_, _, err := ValidateLoginSession(identity)
	require.NoError(t, err, "native status and version cannot revoke a central session")
	require.NoError(t, model.DB.Exec(`UPDATE central_auth_states SET active = ? WHERE user_id = ?`, false, user.Id).Error)
	_, _, err = ValidateLoginSession(identity)
	require.ErrorIs(t, err, ErrLoginSessionRevoked, "central disable must reject the central session")
}

func TestNativeAccountMutationsPreserveCentralSession(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*model.User) error
	}{
		{"update", func(user *model.User) error { user.Password = "updated-password"; return user.Update(true) }},
		{"edit", func(user *model.User) error { user.Password = "updated-password"; return user.Edit(true) }},
		{"reset", func(user *model.User) error { return model.ResetUserPasswordByEmail(user.Email, "updated-password") }},
		{"authenticator", func(user *model.User) error {
			return model.ApplyManagedCredentialChange(user.Id, func(*gorm.DB) error { return nil })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
			user := setupAuthSessionTestDB(t)
			require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}))
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true, "email": "owner@example.test"}).Error)
			require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "bound"}).Error)
			now := time.Now().Unix()
			sessions := []model.UserSession{
				{SID: "central", UserID: user.Id, Version: 1, UserAuthVersion: 7, AuthRealm: model.CentralAuthRealm, Status: model.UserSessionStatusActive, RefreshHash: "a", LoginMethod: "product_broker", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600},
				{SID: "native", UserID: user.Id, Version: 1, UserAuthVersion: 1, AuthRealm: model.NativeFundingLocalOnly, Status: model.UserSessionStatusActive, RefreshHash: "b", LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600},
			}
			require.NoError(t, model.DB.Create(&sessions).Error)
			require.NoError(t, model.DB.First(user, user.Id).Error)
			require.NoError(t, test.mutate(user))
			central, err := model.GetUserSessionBySID("central")
			require.NoError(t, err)
			require.Equal(t, model.UserSessionStatusActive, central.Status)
			native, err := model.GetUserSessionBySID("native")
			require.NoError(t, err)
			require.Equal(t, model.UserSessionStatusRevoked, native.Status)
			_, _, err = ValidateLoginSession(AuthIdentity{UserID: user.Id, SessionID: "central", SessionVersion: 1, UserAuthVersion: 7})
			require.NoError(t, err)
		})
	}
}

func TestCentralStateRequiresMatchingVerifiedBindingAndMonotonicVersion(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	digest := sha256.Sum256([]byte("https://identity.test\x00subject"))
	require.NoError(t, model.DB.Create(&model.UniHubIdentityMapping{UserID: user.Id, Issuer: "https://identity.test", Subject: "subject", IdentityKey: hex.EncodeToString(digest[:])}).Error)
	access := userkit.Access{Issuer: "https://identity.test", Subject: "subject", User: userkit.User{ID: "subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 3, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
	identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", UserID: "subject", LocalUserID: "1", ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 7}
	require.NoError(t, model.RecordCentralAuthState(context.Background(), user.Id, identity, access))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error)
	bundle, err := CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", access.ExpiresAt, "127.0.0.1", "test")
	require.NoError(t, err)
	session, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	require.Equal(t, model.CentralAuthRealm, session.AuthRealm)
	require.Equal(t, int64(7), session.UserAuthVersion)
	identity.AuthVersion = 6
	require.Error(t, model.RecordCentralAuthState(context.Background(), user.Id, identity, access))
	identity.AuthVersion = 8
	identity.Binding.UserID = "another-subject"
	require.Error(t, model.RecordCentralAuthState(context.Background(), user.Id, identity, access))
	state, err := model.ReadCentralAuthState(context.Background(), user.Id)
	require.NoError(t, err)
	require.Equal(t, int64(7), state.AuthVersion)
}

func TestCentralRefreshUnaffectedByNativeStatus(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.UniHubIdentityMapping{}))
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true}).Error)
	now := time.Now().Unix()
	sid := uuid.NewString()
	s := model.UserSession{SID: sid, UserID: user.Id, Version: 1, UserAuthVersion: 7, AuthRealm: model.CentralAuthRealm,
		Status: model.UserSessionStatusActive, RefreshHash: hashRefreshSecret("secret"), LoginMethod: "fixture",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600}
	require.NoError(t, model.DB.Create(&s).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"auth_version": 2, "status": common.UserStatusDisabled}).Error)
	bundle, _, err := RefreshLoginSession(sid+".secret", sid, "127.0.0.1", "test")
	require.NoError(t, err)
	require.NotEmpty(t, bundle.AccessToken)
}

func TestNativeSecurityMutationDoesNotRevokeCentralSession(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{
		"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true,
	}).Error)
	now := time.Now().Unix()
	rows := []model.UserSession{
		{SID: "central-kept", UserID: user.Id, Version: 1, UserAuthVersion: 1, AuthRealm: "CENTRAL", Status: model.UserSessionStatusActive, RefreshHash: "a", LoginMethod: "product_broker", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600},
		{SID: "native-revoked", UserID: user.Id, Version: 1, UserAuthVersion: 1, AuthRealm: model.NativeFundingLocalOnly, Status: model.UserSessionStatusActive, RefreshHash: "b", LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600},
	}
	require.NoError(t, model.DB.Create(&rows).Error)
	_, err := model.RevokeAllUserSessions(user.Id, "native_password_changed")
	require.NoError(t, err)
	central, err := model.GetUserSessionBySID("central-kept")
	require.NoError(t, err)
	require.Equal(t, model.UserSessionStatusActive, central.Status)
	native, err := model.GetUserSessionBySID("native-revoked")
	require.NoError(t, err)
	require.Equal(t, model.UserSessionStatusRevoked, native.Status)
}

func TestUnknownSessionRealmFailsClosed(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	now := time.Now().Unix()
	s := model.UserSession{SID: "unknown-realm", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
		AuthRealm: "UNKNOWN", Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "password",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600}
	require.NoError(t, model.DB.Create(&s).Error)
	_, _, err := ValidateLoginSession(AuthIdentity{UserID: user.Id, SessionID: s.SID, SessionVersion: 1, UserAuthVersion: user.AuthVersion})
	require.ErrorIs(t, err, ErrLoginSessionRevoked)
}

func TestCentralSessionListingUsesCentralVersion(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}))
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true}).Error)
	now := time.Now().Unix()
	s := model.UserSession{SID: "central-list", UserID: user.Id, Version: 1, UserAuthVersion: 7,
		AuthRealm: model.CentralAuthRealm, Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600}
	require.NoError(t, model.DB.Create(&s).Error)
	list, err := model.ListActiveUserSessions(user.Id, s.SID, now, model.CentralAuthRealm)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, s.SID, list[0].SID)
}

func TestCentralSessionRealmSurvivesRedisCache(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	useIndependentAuthSessionRedis(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}))
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "bound"}).Error)
	now := time.Now().Unix()
	s := model.UserSession{SID: "central-cache", UserID: user.Id, Version: 1, UserAuthVersion: 7, AuthRealm: model.CentralAuthRealm,
		Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600}
	require.NoError(t, model.CreateUserSession(&s))
	cached, err := model.GetUserSessionCached(s.SID)
	require.NoError(t, err)
	require.Equal(t, model.CentralAuthRealm, cached.AuthRealm)
}

func TestCentralVerifiedSourceRequiresRuntimeAndMatchingIdentity(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	digest := sha256.Sum256([]byte("https://identity.test\x00subject"))
	require.NoError(t, model.DB.Create(&model.UniHubIdentityMapping{UserID: user.Id, Issuer: "https://identity.test", Subject: "subject", IdentityKey: hex.EncodeToString(digest[:])}).Error)
	access := userkit.Access{Issuer: "https://identity.test", Subject: "subject", User: userkit.User{ID: "subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 3, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
	identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", UserID: "subject", LocalUserID: strconv.Itoa(user.Id), ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 7, ProductRole: 1}
	status := http.StatusServiceUnavailable
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/v1/product-runtime/identity/"+strconv.Itoa(user.Id), r.URL.Path)
		require.Equal(t, "Bearer "+strings.Repeat("s", 32), r.Header.Get("Authorization"))
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		data, err := common.Marshal(identity)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	for key, value := range map[string]string{"UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_API_ORIGIN": server.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": "https://identity.test", "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_PRODUCT_CLIENT_ID": "router", "UNIHUB_PRODUCT_REDIRECT_URI": "https://router.test/api/unihub/callback"} {
		t.Setenv(key, value)
	}
	_, err := CreateCentralSessionFromVerifiedSource(context.Background(), user.Id, access, "product_broker", access.ExpiresAt, "", "")
	require.ErrorIs(t, err, ErrBrokerSourceUnavailable)
	status = http.StatusOK
	identity.Access.Subject = "other-subject"
	_, err = CreateCentralSessionFromVerifiedSource(context.Background(), user.Id, access, "product_broker", access.ExpiresAt, "", "")
	require.Error(t, err)
	identity.Access = access
	bundle, err := CreateCentralSessionFromVerifiedSource(context.Background(), user.Id, access, "product_broker", access.ExpiresAt, "", "")
	require.NoError(t, err)
	session, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	require.Equal(t, model.CentralAuthRealm, session.AuthRealm)
	require.Equal(t, int64(7), session.UserAuthVersion)
}

func TestNativeAndCentralSessionSecurityChangesRemainIndependent(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{
		"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, AccessVersion: 4, Active: true, BindingID: "bound", Issuer: "issuer", Subject: "subject"}).Error)
	native, err := CreateNativeLoginSessionAtAuthVersion(user.Id, 1, "password", "", "")
	require.NoError(t, err)
	central, err := CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	nativeID, err := ParseAccessToken(native.AccessToken)
	require.NoError(t, err)
	centralID, err := ParseAccessToken(central.AccessToken)
	require.NoError(t, err)
	renewed, err := AdvanceCurrentSessionSecurity(nativeID, "native_security_changed")
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(centralID)
	require.NoError(t, err)
	renewedID, err := ParseAccessToken(renewed.AccessToken)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.CentralAuthState{}).Where("user_id = ?", user.Id).Update("active", false).Error)
	_, _, err = ValidateLoginSession(centralID)
	require.ErrorIs(t, err, ErrLoginSessionRevoked)
	_, _, err = ValidateLoginSession(renewedID)
	require.NoError(t, err)
}

func TestIndependentRealmRefreshCookiesCoexist(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "bound"}).Error)
	native, err := CreateNativeLoginSessionAtAuthVersion(user.Id, 1, "password", "", "")
	require.NoError(t, err)
	central, err := CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	WriteRefreshCookie(c, native.RefreshToken)
	WriteRefreshCookie(c, central.RefreshToken)
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 2)
	require.NotEqual(t, cookies[0].Name, cookies[1].Name, "central login must not overwrite native refresh cookie")
	require.Equal(t, native.RefreshToken, cookies[0].Value)
	require.Equal(t, central.RefreshToken, cookies[1].Value)
	request := httptest.NewRequest(http.MethodPost, "/api/user/auth/refresh", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	c.Request = request
	_, selected, err := SelectRefreshCookie(c, native.Session.SID)
	require.NoError(t, err)
	require.Equal(t, native.RefreshToken, selected)
	_, selected, err = SelectRefreshCookie(c, central.Session.SID)
	require.NoError(t, err)
	require.Equal(t, central.RefreshToken, selected)
	_, _, err = SelectRefreshCookie(c, "")
	require.ErrorIs(t, err, ErrLoginSessionMismatch)
	_, _, err = SelectRefreshCookie(c, "not-either-session")
	require.ErrorIs(t, err, ErrLoginSessionMismatch)
}

func TestCentralStateRejectsStaleDisableAndStaleReenable(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	digest := sha256.Sum256([]byte("https://identity.test\x00subject"))
	require.NoError(t, model.DB.Create(&model.UniHubIdentityMapping{UserID: user.Id, Issuer: "https://identity.test", Subject: "subject", IdentityKey: hex.EncodeToString(digest[:])}).Error)
	access := userkit.Access{Issuer: "https://identity.test", Subject: "subject", User: userkit.User{ID: "subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 8, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
	identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", UserID: "subject", LocalUserID: strconv.Itoa(user.Id), ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 7}
	require.NoError(t, model.RecordCentralAuthState(context.Background(), user.Id, identity, access))
	stale := identity
	stale.Access.Active = false
	stale.Access.Version = 7
	require.Error(t, model.RecordCentralAuthDenial(context.Background(), user.Id, stale))
	identity.Access.Active = false
	identity.Access.Version = 9
	require.NoError(t, model.RecordCentralAuthDenial(context.Background(), user.Id, identity))
	require.Error(t, model.RecordCentralAuthState(context.Background(), user.Id, userkit.ProductIdentity{Binding: identity.Binding, Access: access, AuthVersion: 7}, access))
	state, err := model.ReadCentralAuthState(context.Background(), user.Id)
	require.NoError(t, err)
	require.False(t, state.Active)
	require.Equal(t, int64(9), state.AccessVersion)
}

func TestCentralCredentialSessionRequiresCurrentCredentialProof(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.UniHubIdentityMapping{}))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("oidc_id", "subject").Error)
	sum := sha256.Sum256([]byte("subject"))
	digest := hex.EncodeToString(sum[:])
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "bound"}).Error)
	require.NoError(t, model.DB.Create(&model.CentralLoginCredential{UserID: user.Id, Kind: "oidc_id", Digest: digest, BindingID: "bound", AuthVersion: 7}).Error)
	now := time.Now().Unix()
	s := model.UserSession{SID: "central-credential-session", UserID: user.Id, Version: 1, UserAuthVersion: 7, AuthRealm: model.CentralAuthRealm, Status: model.UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "central_credential", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 600}
	require.NoError(t, model.DB.Create(&s).Error)
	id := AuthIdentity{UserID: user.Id, SessionID: s.SID, SessionVersion: 1, UserAuthVersion: 7}
	_, _, err := ValidateLoginSession(id)
	require.Error(t, err, "a CENTRAL credential SID must not work before the proof is bound")
	require.NoError(t, model.DB.Create(&model.CentralSessionCredential{SID: s.SID, UserID: user.Id, Kind: "oidc_id", Digest: digest}).Error)
	_, _, err = ValidateLoginSession(id)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("oidc_id", "changed").Error)
	_, _, err = ValidateLoginSession(id)
	require.Error(t, err, "credential replacement must immediately invalidate its SID")
}

func TestCentralCredentialLoginBindsRevocableAuthenticator(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.UniHubIdentityMapping{}))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"oidc_id": "external-subject", "status": common.UserStatusDisabled}).Error)
	digest := sha256.Sum256([]byte("external-subject"))
	state := model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "bound", Issuer: "https://identity.test", Subject: "central-subject", ProductID: "llmrouter", InstanceID: "default"}
	require.NoError(t, model.DB.Create(&state).Error)
	mappingDigest := sha256.Sum256([]byte(state.Issuer + "\x00" + state.Subject))
	require.NoError(t, model.DB.Create(&model.UniHubIdentityMapping{UserID: user.Id, Issuer: state.Issuer, Subject: state.Subject, IdentityKey: hex.EncodeToString(mappingDigest[:])}).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"identity_source": model.UniHubIdentitySource, "unihub_user_id": state.Subject}).Error)
	require.NoError(t, model.DB.Create(&model.CentralLoginCredential{UserID: user.Id, Kind: "oidc_id", Digest: hex.EncodeToString(digest[:]), BindingID: "bound", AuthVersion: 7}).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/v1/product-runtime/identity/authenticate", r.URL.Path)
		access := userkit.Access{Issuer: state.Issuer, Subject: state.Subject, User: userkit.User{ID: state.Subject}, ProductID: state.ProductID, InstanceID: state.InstanceID, Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: state.BindingID, LocalUserID: strconv.Itoa(user.Id), UserID: state.Subject, ProductID: state.ProductID, InstanceID: state.InstanceID}, Access: access, ProductRole: 1, AuthVersion: 7}
		data, err := common.Marshal(identity)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	bundle, _, err := CreateCentralCredentialLogin(context.Background(), user.Id, "oidc_id", "external-subject", "oauth:oidc", "", "")
	require.NoError(t, err)
	id, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(id)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("oidc_id", "replaced").Error)
	_, _, err = ValidateLoginSession(id)
	require.Error(t, err)
	_, _, err = RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "", "")
	require.ErrorIs(t, err, ErrLoginSessionRevoked)
}
