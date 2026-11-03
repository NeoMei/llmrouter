package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductSelfKeepsExactSharedCashSeparateFromProductQuota(t *testing.T) {
	user := &model.User{Id: 42, Username: "central", Role: common.RoleCommonUser, Quota: 123, UsedQuota: 17, Funding: &model.ProductMoneyBalance{Mode: "unified_cash", Asset: "CNY/6", UnitVersion: "lr-cny-v1", Available: "9007199254740993123", Held: "23", Consumed: "100"}}
	data := BuildSelfUserData(user)
	require.Equal(t, user.Funding, data["funding"])
	raw, err := common.Marshal(data)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"available":"9007199254740993123"`)
	assert.Equal(t, 123, data["quota"])
	assert.Equal(t, 17, data["used_quota"])
	user.Funding = nil
	assert.NotContains(t, BuildSelfUserData(user), "funding")
	user.Funding = &model.ProductMoneyBalance{Mode: "unified_cash", Available: "999"}
	user.FundingRealm = model.NativeFundingLocalOnly
	assert.NotContains(t, BuildSelfUserData(user), "funding")
}
