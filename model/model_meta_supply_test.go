package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundChannelsSeparateInstancesAndExcludeInactiveBindings(t *testing.T) {
	resetPricingEndpointTestTables(t)
	for _, id := range []int{8, 4, 6, 2} {
		insertPricingEndpointChannel(t, id, constant.ChannelTypeVolcEngine, dto.ChannelOtherSettings{})
		require.NoError(t, DB.Model(&Channel{}).Where("id = ?", id).Update("name", "same name").Error)
		insertPricingEndpointAbility(t, id, "bound-model")
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 6).Update("status", common.ChannelStatusManuallyDisabled).Error)
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", 2).Update("enabled", false).Error)
	require.NoError(t, DB.Create(&Ability{Group: "other", Model: "bound-model", ChannelId: 4, Enabled: true}).Error)

	channels, err := GetBoundChannelsByModelsMap([]string{"bound-model"})
	require.NoError(t, err)
	body, err := common.Marshal(channels["bound-model"])
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":4,"name":"same name","type":45,"status":1},{"id":8,"name":"same name","type":45,"status":1}]`, string(body))
}

func TestArkChannelConfigurationProvisionsOnlyUncoveredModels(t *testing.T) {
	resetPricingEndpointTestTables(t)
	vendor := &Vendor{Name: "字节跳动", Icon: "Existing.Icon", Description: "curated"}
	require.NoError(t, vendor.Insert())
	for _, item := range []Model{
		{ModelName: "doubao-seedream-curated", VendorID: vendor.Id, Icon: "Curated.Icon", Status: 0},
		{ModelName: "doubao-prefix-", VendorID: vendor.Id, NameRule: NameRulePrefix, Status: 1},
	} {
		require.NoError(t, item.Insert())
	}

	channel := Channel{
		Type: constant.ChannelTypeVolcEngine, Key: "local", Group: "default",
		Models: "doubao-seedream-new,doubao-seedream-curated,doubao-prefix-version",
	}
	require.NoError(t, BatchInsertChannels([]Channel{channel}))
	video := Channel{
		Type: constant.ChannelTypeDoubaoVideo, Key: "local", Group: "default",
		Models: "doubao-seedance-new",
	}
	require.NoError(t, video.Insert())
	ordinary := Channel{
		Type: constant.ChannelTypeOpenAI, Key: "local", Group: "default",
		Models: "ordinary-new",
	}
	require.NoError(t, ordinary.Insert())

	var rows []Model
	require.NoError(t, DB.Order("model_name").Find(&rows).Error)
	require.Len(t, rows, 4)
	assert.Equal(t, "doubao-prefix-", rows[0].ModelName)
	assert.Equal(t, "doubao-seedance-new", rows[1].ModelName)
	assert.Equal(t, vendor.Id, rows[1].VendorID)
	assert.Equal(t, "Existing.Icon", rows[1].Icon)
	assert.Equal(t, "doubao-seedream-curated", rows[2].ModelName)
	assert.Equal(t, "Curated.Icon", rows[2].Icon)
	assert.Zero(t, rows[2].Status)
	assert.Equal(t, "doubao-seedream-new", rows[3].ModelName)
	assert.Equal(t, vendor.Id, rows[3].VendorID)

	video.Models += ",doubao-seedance-added"
	require.NoError(t, video.Update())
	require.NoError(t, DB.Where("model_name = ?", "doubao-seedance-added").First(&Model{}).Error)
}

func TestArkChannelConfigurationRollsBackFailedProvisioning(t *testing.T) {
	resetPricingEndpointTestTables(t)
	require.NoError(t, DB.Exec(`CREATE TRIGGER test_reject_ark_metadata BEFORE INSERT ON models
		WHEN NEW.model_name = 'doubao-seedream-blocked' BEGIN SELECT RAISE(ABORT, 'metadata write failed'); END`).Error)
	t.Cleanup(func() { require.NoError(t, DB.Exec("DROP TRIGGER test_reject_ark_metadata").Error) })
	ordinary := Channel{Type: constant.ChannelTypeOpenAI, Key: "local", Group: "default", Models: "original"}
	require.NoError(t, ordinary.Insert())
	ordinary.Type = constant.ChannelTypeVolcEngine
	ordinary.Models = "doubao-seedream-blocked"
	require.ErrorContains(t, ordinary.Update(), "metadata write failed")

	stored, err := GetChannelById(ordinary.Id, true)
	require.NoError(t, err)
	assert.Equal(t, constant.ChannelTypeOpenAI, stored.Type)
	assert.Equal(t, "original", stored.Models)
	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", ordinary.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "original", abilities[0].Model)
	var vendors int64
	require.NoError(t, DB.Model(&Vendor{}).Count(&vendors).Error)
	assert.Zero(t, vendors)
}

func TestChannelAndAbilitiesRollBackTogether(t *testing.T) {
	resetPricingEndpointTestTables(t)
	require.NoError(t, DB.Exec(`CREATE TRIGGER test_reject_channel_ability BEFORE INSERT ON abilities
		WHEN NEW.model = 'blocked' BEGIN SELECT RAISE(ABORT, 'ability write failed'); END`).Error)
	t.Cleanup(func() { require.NoError(t, DB.Exec("DROP TRIGGER test_reject_channel_ability").Error) })
	bad := Channel{Type: constant.ChannelTypeOpenAI, Key: "local", Group: "default", Models: "blocked"}
	require.ErrorContains(t, bad.Insert(), "ability write failed")
	var n int64
	require.NoError(t, DB.Model(&Channel{}).Count(&n).Error)
	assert.Zero(t, n)

	tag := "test"
	valid := Channel{Type: constant.ChannelTypeOpenAI, Key: "local", Group: "default", Models: "original", Tag: &tag}
	require.NoError(t, valid.Insert())
	valid.Models = "blocked"
	require.ErrorContains(t, valid.Update(), "ability write failed")
	require.ErrorContains(t, EditChannelByTag(tag, nil, nil, &valid.Models, nil, nil, nil, nil, nil), "ability write failed")
	stored, err := GetChannelById(valid.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "original", stored.Models)
	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", valid.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "original", abilities[0].Model)
}

func TestVendorCannotBeDeletedWhileReferencedByModelMetadata(t *testing.T) {
	resetPricingEndpointTestTables(t)
	vendor := &Vendor{Name: "vendor", Status: 1}
	require.NoError(t, vendor.Insert())
	metadata := &Model{ModelName: "retained-model", VendorID: vendor.Id, Status: 0}
	require.NoError(t, metadata.Insert())

	require.Error(t, vendor.Delete())
	_, err := GetVendorByID(vendor.Id)
	require.NoError(t, err)

	metadata.VendorID = 0
	require.NoError(t, metadata.Update())
	require.NoError(t, vendor.Delete())

	require.Error(t, (&Model{ModelName: "invalid-vendor-ref", VendorID: vendor.Id, Status: 1}).Insert())
}

func TestExactModelMetadataCanBeDisabledWhileProtectingActiveBindings(t *testing.T) {
	resetPricingEndpointTestTables(t)
	ch := Channel{Type: constant.ChannelTypeOpenAI, Key: "local", Models: "active-model", Group: "default"}
	require.NoError(t, ch.Insert())
	item := &Model{ModelName: "active-model", Status: 1}
	require.NoError(t, item.Insert())

	item.Status = 0
	require.NoError(t, item.Update())
	assert.NotContains(t, pricingEndpointTypesFromPricing(GetPricing()), "active-model")
	assert.Contains(t, GetGroupEnabledModels("default"), "active-model")

	require.Error(t, item.Delete())
	item.ModelName = "renamed-model"
	require.Error(t, item.Update())

	require.True(t, UpdateChannelStatus(ch.Id, "", common.ChannelStatusManuallyDisabled, "test"))
	item.ModelName = "active-model"
	require.NoError(t, item.Delete())
}

func TestChannelStatusAndAbilityChangesCommitTogether(t *testing.T) {
	resetPricingEndpointTestTables(t)
	tag := "tagged"
	channel := Channel{Type: constant.ChannelTypeOpenAI, Key: "local", Group: "default", Models: "available", Tag: &tag}
	require.NoError(t, channel.Insert())

	require.NoError(t, DB.Exec(`CREATE TRIGGER test_reject_disable_ability BEFORE UPDATE ON abilities
		WHEN NEW.enabled = 0 BEGIN SELECT RAISE(ABORT, 'ability disable failed'); END`).Error)
	assert.False(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusManuallyDisabled, "test"))
	require.ErrorContains(t, DisableChannelByTag(tag), "ability disable failed")
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Contains(t, GetGroupEnabledModels("default"), "available")

	require.NoError(t, DB.Exec("DROP TRIGGER test_reject_disable_ability").Error)
	require.NoError(t, DisableChannelByTag(tag))
	stored, err = GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.NotContains(t, GetGroupEnabledModels("default"), "available")

	deleted, err := DeleteDisabledChannel()
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
	var count int64
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", channel.Id).Count(&count).Error)
	assert.Zero(t, count)
}

func TestExactModelMetadataProtectsExplicitRouteReferences(t *testing.T) {
	resetPricingEndpointTestTables(t)
	insertPricingEndpointChannel(t, 1, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	route := &ClientModelRoute{PublicId: "public", DisplayName: "Public", Enabled: true}
	require.NoError(t, DB.Create(route).Error)
	target := &ClientModelTarget{
		RouteId: route.Id, ChannelId: 1, UpstreamModel: "upstream", BillingModel: "billing",
		PreferredWires: `["openai_chat"]`, Enabled: true,
	}
	require.NoError(t, DB.Create(target).Error)

	for _, name := range []string{route.PublicId, target.UpstreamModel, target.BillingModel} {
		item := &Model{ModelName: name, Status: 1}
		require.NoError(t, item.Insert())
		require.ErrorContains(t, item.Delete(), "referenced by an enabled client route")
	}
	require.NoError(t, DB.Model(target).Update("enabled", false).Error)
	var billing Model
	require.NoError(t, DB.Where("model_name = ?", "billing").First(&billing).Error)
	require.NoError(t, billing.Delete())
}
