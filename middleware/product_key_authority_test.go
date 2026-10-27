package middleware

import (
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSpecificChannelUsesFreshCentralAuthorityAndKeepsKeyRestrictions(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, i18n.Init())
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.Channel{}, &model.Log{}))
	t.Setenv("LOG_SQL_DSN", "")
	oldLog := model.LOG_DB
	require.NoError(t, model.InitLogDB())
	model.LOG_DB = model.DB
	t.Cleanup(func() { model.LOG_DB = oldLog })
	user := model.User{Username: "key-authority", AffCode: "key-authority", Role: 100, Status: 1, AuthVersion: 1, Group: "default"}
	require.NoError(t, model.DB.Create(&user).Error)
	_, e := model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central"}}, user.Id)
	require.NoError(t, e)
	token := model.Token{Key: "keyauthority", UserId: user.Id, Status: 1, ExpiredTime: -1, RemainQuota: 10, ModelLimitsEnabled: true, ModelLimits: "allowed", Group: "default"}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "specific", Type: 1, Status: 1, Key: "provider", Group: "default", Models: "allowed,forbidden"}
	require.NoError(t, model.DB.Create(&channel).Error)
	role := 1
	permissions := []string{"product:use"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: strconv.Itoa(user.Id), UserID: "central", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "central", User: userkit.User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default", ProductRole: role, Active: true, Permissions: permissions, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: role, AuthVersion: 1})
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	engine := gin.New()
	engine.POST("/v1/chat/completions", TokenAuth(), Distribute(), func(c *gin.Context) { c.Status(204) })
	send := func(requestModel string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://router.test/v1/chat/completions", strings.NewReader(`{"model":"`+requestModel+`","messages":[]}`))
		r.Header.Set("Authorization", "Bearer sk-keyauthority-"+strconv.Itoa(channel.Id))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	got := send("allowed")
	require.Equal(t, 403, got.Code, "stale SQL root must not authorize channel selection: "+got.Body.String())
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("role", 1).Error)
	role = 10
	permissions = []string{"product:use", "channel:operate"}
	got = send("allowed")
	require.Equal(t, 204, got.Code, "fresh central admin must allow original channel selection: "+got.Body.String())
	got = send("forbidden")
	require.Equal(t, 403, got.Code, "specific channel must not bypass key model limit: "+got.Body.String())
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("group", "other").Error)
	got = send("allowed")
	require.Equal(t, 403, got.Code, "specific channel must not bypass key group: "+got.Body.String())
	role = 1
	permissions = []string{"product:use"}
	require.Equal(t, 403, send("allowed").Code, "same key must lose privilege immediately")
}
