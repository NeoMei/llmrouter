package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeFinanceActorRequiresCurrentBoundSession(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductAuthLifecycle{}))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("r", 32))
	cfg, err := platformaccount.Current()
	require.NoError(t, err)
	user := &model.User{Username: "finance-member", AffCode: "finance-member", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleRootUser}
	require.NoError(t, f.db.Create(user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: "central-member"}}, user.Id)
	require.NoError(t, err)
	bundle, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	identity, internal, err := service.ParseDashboardAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	require.True(t, internal)
	access := userkit.Access{Issuer: cfg.PublicOrigin, Subject: "central-member", User: userkit.User{ID: "central-member"}, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Active: true, Permissions: []string{"product:use"}, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	principal := userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(user.Id)}, Access: access}
	actorSubject := "central-member"
	actorToken := "upa_" + strings.Repeat("a", 64)
	actorExpires := time.Now().Add(time.Minute).Unix()
	calls := 0
	revokeDuringAuth := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/v1/product-runtime/identity/authenticate" {
			w.WriteHeader(404)
			return
		}
		out := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(user.Id), UserID: actorSubject, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID}, Access: access, ProductRole: 1, AuthVersion: 1, ActorToken: actorToken, ActorExpiresAt: actorExpires}
		out.Access.Subject = actorSubject
		out.Access.User.ID = actorSubject
		if revokeDuringAuth {
			_, revokeErr := model.RevokeUserSession(user.Id, identity.SessionID, "test")
			if revokeErr != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		raw, e := common.Marshal(out)
		require.NoError(t, e)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("UNIHUB_ORIGIN", upstream.URL)
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", upstream.URL)
	proof, err := runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.NoError(t, err)
	assert.Equal(t, "upa_"+strings.Repeat("a", 64), proof.ActorToken)
	actorSubject = "other"
	_, err = runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.Error(t, err)
	actorSubject = "central-member"
	actorExpires = time.Now().Add(-time.Second).Unix()
	_, err = runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.Error(t, err)
	actorExpires = time.Now().Add(time.Minute).Unix()
	actorToken = ""
	_, err = runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.Error(t, err)
	actorToken = "upa_" + strings.Repeat("a", 64)
	actorExpires = time.Now().Add(3 * time.Minute).Unix()
	_, err = runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.Error(t, err, "excessively long actor lifetime must fail closed")
	actorExpires = time.Now().Add(time.Minute).Unix()
	access.Permissions = nil
	_, err = runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.Error(t, err, "central permission revocation must be checked freshly")
	access.Permissions = []string{"product:use"}
	bad := principal
	bad.Access.Subject = "other"
	_, err = runtimeFinanceActor(context.Background(), identity, bad, cfg)
	require.Error(t, err)
	revokeDuringAuth = true
	_, err = runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.Error(t, err, "revoked source SID cannot mint usable financial authority")
	revokeDuringAuth = false
	prior := calls
	_, err = runtimeFinanceActor(context.Background(), identity, principal, cfg)
	require.Error(t, err)
	assert.Equal(t, prior, calls)
}
