package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNativeRealmSessionCannotBecomeCentralHandoffActor(t *testing.T) {
	oldDB, oldRedis, oldSecret := model.DB, common.RedisEnabled, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/native-handoff.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.ProductFundingAccount{}, &model.AuthFlow{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	model.DB, common.RedisEnabled, common.SessionSecret = db, false, "native-handoff-test-secret"
	t.Cleanup(func() { model.DB, common.RedisEnabled, common.SessionSecret = oldDB, oldRedis, oldSecret })
	owner := model.User{Username: "native-handoff", AffCode: "native-handoff", AuthVersion: 1, Status: common.UserStatusEnabled, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, db.Create(&owner).Error)
	native, err := service.CreateNativeLoginSessionAtAuthVersion(owner.Id, 1, "password", "", "")
	require.NoError(t, err)
	authCalls := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { authCalls++; w.WriteHeader(http.StatusForbidden) }))
	t.Cleanup(hub.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", hub.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	r := gin.New()
	registerUniHubSSO(r, platformaccount.Config{Enabled: true, ClientID: "router", PublicOrigin: hub.URL, RedirectURI: "https://router.test/api/unihub/callback"})
	req := httptest.NewRequest("GET", "https://router.test/api/user/auth/unihub-handoff?return_to=%2Fprofile", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Zero(t, authCalls, "native authority must never be presented as central")
}
