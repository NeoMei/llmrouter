package router

import (
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

func TestBrokerSourceProofBoundToSessionAndScope(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductBrokerSessionProof{}))
	cfg, err := platformaccount.Current()
	require.NoError(t, err)
	user := model.User{Username: "source-proof-member", AffCode: "source-proof-member", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, f.db.Create(&user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: "member"}}, user.Id)
	require.NoError(t, err)
	one, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	two, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	native, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	access := userkit.Access{Issuer: cfg.PublicOrigin, Subject: "member", User: userkit.User{ID: "member"}, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Active: true, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	secret := "ubsp_" + strings.Repeat("a", 64)
	require.NoError(t, model.BindBrokerSourceProof(one.Session.SID, user.Id, access, cfg, secret, time.Now().Add(time.Minute).Unix()))
	var stored model.ProductBrokerSessionProof
	require.NoError(t, f.db.First(&stored, "sid = ?", one.Session.SID).Error)
	assert.NotContains(t, stored.Ciphertext, secret)
	_, opened, err := model.OpenBrokerSourceProof(one.Session.SID, user.Id, "member", cfg)
	require.NoError(t, err)
	assert.Equal(t, secret, opened)
	require.Error(t, model.BindBrokerSourceProof(one.Session.SID, user.Id, access, cfg, secret, time.Now().Add(time.Minute).Unix()), "a proof cannot be overwritten")
	require.NoError(t, f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", one.Session.SID).Update("expires_at", stored.ExpiresAt+1).Error)
	_, _, err = model.OpenBrokerSourceProof(one.Session.SID, user.Id, "member", cfg)
	assert.Error(t, err, "authenticated metadata must not be altered")
	require.NoError(t, f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", one.Session.SID).Update("expires_at", stored.ExpiresAt).Error)
	assert.Error(t, model.BindBrokerSourceProof(native.Session.SID, user.Id, access, cfg, secret, time.Now().Add(time.Minute).Unix()))
	assert.Error(t, model.BindBrokerSourceProof(two.Session.SID, user.Id, access, cfg, "ubp_not_a_source_proof", time.Now().Add(time.Minute).Unix()))
	wrong := cfg
	wrong.ClientID = "other"
	_, _, err = model.OpenBrokerSourceProof(one.Session.SID, user.Id, "member", wrong)
	assert.Error(t, err)
	_, _, err = model.OpenBrokerSourceProof(one.Session.SID, user.Id, "another", cfg)
	assert.Error(t, err)
	copied := stored
	copied.SID = two.Session.SID
	require.NoError(t, f.db.Create(&copied).Error)
	_, _, err = model.OpenBrokerSourceProof(two.Session.SID, user.Id, "member", cfg)
	assert.Error(t, err)
	_, err = model.RevokeUserSession(user.Id, one.Session.SID, "test")
	require.NoError(t, err)
	_, _, err = model.OpenBrokerSourceProof(one.Session.SID, user.Id, "member", cfg)
	assert.Error(t, err)
	require.NoError(t, model.PurgeBrokerSourceProofs(time.Now().Unix()))
	var count int64
	require.NoError(t, f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", one.Session.SID).Count(&count).Error)
	assert.Zero(t, count)
}
