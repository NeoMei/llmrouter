package controller

import (
	"testing"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCentralUserViewRowsMapAuthoritativeRolesAndStatus(t *testing.T) {
	config := platformaccount.Config{ProductID: "llmrouter", InstanceID: "default"}
	rows, err := centralUserViewRows([]userkit.RuntimeManagedUser{
		{User: userkit.User{ID: "root", Name: "Root", Email: "root@example.test"}, Assignment: userkit.RuntimeAssignment{ProductID: config.ProductID, InstanceID: config.InstanceID, Roles: []string{"product_root"}, Active: true, Version: 1}},
		{User: userkit.User{ID: "admin", Name: "Admin"}, Assignment: userkit.RuntimeAssignment{ProductID: config.ProductID, InstanceID: config.InstanceID, Roles: []string{"product_admin"}, Active: true, Version: 2}},
		{User: userkit.User{ID: "viewer", Name: "Viewer"}, Assignment: userkit.RuntimeAssignment{ProductID: config.ProductID, InstanceID: config.InstanceID, Roles: []string{"product_viewer"}, Active: true, Version: 3}},
		{User: userkit.User{ID: "member"}, Assignment: userkit.RuntimeAssignment{ProductID: config.ProductID, InstanceID: config.InstanceID, Roles: []string{"member"}, Active: false, Version: 4}},
	}, config)
	require.NoError(t, err)
	require.Len(t, rows, 4)
	assert.Equal(t, common.RoleRootUser, rows[0].Role)
	assert.Equal(t, common.RoleAdminUser, rows[1].Role)
	assert.Equal(t, common.RoleAdminUser, rows[2].Role)
	assert.Equal(t, common.RoleCommonUser, rows[3].Role)
	assert.Equal(t, common.UserStatusEnabled, rows[0].Status)
	assert.Equal(t, common.UserStatusDisabled, rows[3].Status)
	assert.Equal(t, "member", rows[3].DisplayName)
}

func TestCentralUserViewRowsRejectForeignAndIncompleteAssignments(t *testing.T) {
	config := platformaccount.Config{ProductID: "llmrouter", InstanceID: "default"}
	_, err := centralUserViewRows([]userkit.RuntimeManagedUser{{User: userkit.User{ID: "foreign"}, Assignment: userkit.RuntimeAssignment{ProductID: "other", InstanceID: config.InstanceID, Active: true}}}, config)
	require.Error(t, err)

	_, err = centralUserViewRows([]userkit.RuntimeManagedUser{{User: userkit.User{ID: "missing-scope"}, Assignment: userkit.RuntimeAssignment{ProductID: config.ProductID, Active: true}}}, config)
	require.Error(t, err)

	_, err = centralUserViewRows([]userkit.RuntimeManagedUser{{Assignment: userkit.RuntimeAssignment{ProductID: config.ProductID, InstanceID: config.InstanceID, Active: true}}}, config)
	require.Error(t, err)
}

func TestCentralUserViewRowsRejectPotentialDirectoryTruncation(t *testing.T) {
	config := platformaccount.Config{ProductID: "llmrouter", InstanceID: "default"}
	managed := make([]userkit.RuntimeManagedUser, centralUserDirectoryLimit)
	for i := range managed {
		managed[i] = userkit.RuntimeManagedUser{User: userkit.User{ID: "central-user"}, Assignment: userkit.RuntimeAssignment{ProductID: config.ProductID, InstanceID: config.InstanceID, Active: true}}
	}
	_, err := centralUserViewRows(managed, config)
	require.Error(t, err)
}
