package middleware

import (
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCentralProductRoleIsCurrentAndNeverInferredAsRoot(t *testing.T) {
	require.Equal(t, common.RoleCommonUser, RoleFromAccess(userkit.Access{ProductRole: 1, Permissions: []string{"product:use"}}))
	require.Equal(t, common.RoleAdminUser, RoleFromAccess(userkit.Access{Permissions: []string{"channel:read"}}))
	require.Equal(t, common.RoleRootUser, RoleFromAccess(userkit.Access{ProductRole: 100}))
	require.NotEqual(t, common.RoleRootUser, RoleFromAccess(userkit.Access{Permissions: []string{"settings:write"}}))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/token/", nil)
	authz.SetProductPrincipal(c, userkit.Principal{LocalUser: userkit.LocalUser{ID: "17"}, Access: userkit.Access{Active: true, ExpiresAt: time.Now().Add(time.Minute).Unix(), ProductRole: 1, Permissions: []string{"product:use"}}})
	require.True(t, allowedUniHubPath(c), "ordinary original token and profile routes must not be source-blocked")
}
