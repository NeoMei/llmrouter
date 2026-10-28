package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativePresetCatalogChecksEverySelectableTarget(t *testing.T) {
	enableUniHubBoundary(t)
	old := model.DB
	t.Cleanup(func() { model.DB = old })
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/catalog.db"), &gorm.Config{})
	require.NoError(t, e)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	origin := "https://fixture.invalid"
	key := "synthetic-member"
	sum := sha256.Sum256([]byte(key))
	require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: key, BaseURL: &origin, Status: common.ChannelStatusEnabled, Models: "test", Group: "platform"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "test", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "test", DisplayName: "Test", Enabled: true, Metadata: `{"operations":["chat.completions.create"],"max_output_tokens":100}`}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "test", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	p := platformbilling.NativePresetPolicy{Version: "fixture-v1", PublicModel: "test", WireOperation: "chat.completions.create", ChannelID: 1, ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "test", ChannelKeySHA256: hex.EncodeToString(sum[:]), Origin: origin, APIVersion: "v1", Variants: []platformbilling.NativePreset{{ID: "deliberate", Label: "Deliberate", Parameters: json.RawMessage(`{"reasoning_effort":"high"}`)}}}
	c := platformbilling.Config{Enabled: true, Origin: "http://127.0.0.1:1", ServiceToken: strings.Repeat("x", 32), InstanceID: "fixture", Group: "platform", PrincipalID: 1, MaxBodyBytes: 8192, MaxInputTokens: 8192, MaxOutputTokens: 100, NativePresetPolicies: map[string]map[string]platformbilling.NativePresetPolicy{p.WireOperation: {"test": p}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"chat": {"test": {Policy: "provider_reported_v1"}}}, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CNY/6": {"test": "rate"}}}}
	a, e := platformbilling.New(db, c)
	require.NoError(t, e)
	engine := gin.New()
	cfg := platformbilling.CatalogConfig{Token: strings.Repeat("c", 32), ProductID: "superwagie", InstanceID: "fixture"}
	SetModelCatalogRouter(engine, a, cfg)
	read := func() platformbilling.TechnicalCatalog {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/internal/v1/model-catalog?product_id=superwagie&instance_id=fixture", nil)
		r.Header.Set("Authorization", "Bearer "+cfg.Token)
		engine.ServeHTTP(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		var doc platformbilling.TechnicalCatalog
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &doc))
		return doc
	}
	first := read()
	require.Len(t, first.Models[0].Operations[0].ParameterVariants, 1)
	// Constructor owns an immutable copy, despite operator fixture mutation.
	c.NativePresetPolicies[p.WireOperation]["test"] = platformbilling.NativePresetPolicy{}
	assert.Equal(t, first.Version, read().Version)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "rotated-member").Error)
	assert.Empty(t, read().Models[0].Operations[0].ParameterVariants)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", key).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 2, Type: constant.ChannelTypeOpenAI, Key: key, BaseURL: &origin, Status: common.ChannelStatusEnabled, Models: "test", Group: "platform"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "test", ChannelId: 2, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 2, RouteId: 1, ChannelId: 2, UpstreamModel: "test", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	assert.Empty(t, read().Models[0].Operations[0].ParameterVariants)
}
