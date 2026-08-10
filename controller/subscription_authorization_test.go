package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequireManageableSubscriptionUserEnforcesRoleHierarchy(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.UserSubscription{}))
	root := model.User{Username: "subscription-root", AffCode: "sub-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	admin := model.User{Username: "subscription-admin", AffCode: "sub-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}
	member := model.User{Username: "subscription-member", AffCode: "sub-member", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, db.Create(&member).Error)

	gin.SetMode(gin.TestMode)
	adminContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	adminContext.Set("role", common.RoleAdminUser)
	require.False(t, requireManageableSubscriptionUser(adminContext, root.Id))
	require.False(t, requireManageableSubscriptionUser(adminContext, admin.Id))

	memberContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	memberContext.Set("role", common.RoleAdminUser)
	require.True(t, requireManageableSubscriptionUser(memberContext, member.Id))
	require.False(t, requireRootSubscriptionAdmin(memberContext))
	rootContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	rootContext.Set("role", common.RoleRootUser)
	require.True(t, requireRootSubscriptionAdmin(rootContext))
}

func TestRequireManageableUserSubscriptionResolvesOwner(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.UserSubscription{}))
	root := model.User{Username: "subscription-owner-root", AffCode: "sub-owner-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&root).Error)
	subscription := model.UserSubscription{UserId: root.Id, Status: "active"}
	require.NoError(t, db.Create(&subscription).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set("role", common.RoleAdminUser)
	require.False(t, requireManageableUserSubscription(context, subscription.Id))
	require.False(t, requireManageableUserSubscription(context, subscription.Id+1000))
}
