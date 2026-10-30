package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestNativeRealmSubscriptionTargetCannotBeManagedCentrally(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	user := model.User{Username: "protected-subscription", AffCode: "protected", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, db.Create(&user).Error)
	for _, native := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/api/subscription/admin/bind", nil)
		c.Set("role", common.RoleRootUser)
		c.Set("authenticated_native_realm", native)
		assert.Equal(t, native, requireManageableSubscriptionUser(c, user.Id))
	}
}
