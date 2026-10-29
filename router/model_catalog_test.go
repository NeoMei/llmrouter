package router

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogRoutesPreserveKeyScopeAndNeverCallBilling(t *testing.T) {
	enableUniHubBoundary(t)
	oldDB := model.DB
	oldLimit := common.GlobalApiRateLimitEnable
	t.Cleanup(func() { model.DB = oldDB; common.GlobalApiRateLimitEnable = oldLimit })
	common.GlobalApiRateLimitEnable = false
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/catalog.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB = db
	require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "supplier-secret", Status: common.ChannelStatusEnabled, Models: "allowed,hidden", Group: "platform"}).Error)
	for i, id := range []string{"allowed", "hidden"} {
		require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: id, ChannelId: 1, Enabled: true}).Error)
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: i + 1, PublicId: id, DisplayName: id, Client: "all", Enabled: true, Metadata: `{"operations":["chat.completions.create"],"supports_tools":true,"streaming":true}`}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: i + 1, RouteId: i + 1, ChannelId: 1, UpstreamModel: id, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	}
	verifies := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/model-credentials/verify" {
			t.Errorf("financial request from discovery %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		verifies++
		raw, _ := common.Marshal(platformbilling.Claims{ID: "key", CredentialKind: "api_key", InstanceID: "fixture", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", Models: []string{"allowed"}})
		w.Write(raw)
	}))
	defer hub.Close()
	adapter, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("f", 32), InstanceID: "fixture", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CNY/6": {"allowed": "rate"}}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"chat": {"allowed": {Policy: "provider_reported_v1"}, "hidden": {Policy: "provider_reported_v1"}}}})
	require.NoError(t, e)
	for k, v := range map[string]string{"UNIHUB_CATALOG_EXPORT_TOKEN": strings.Repeat("c", 32), "UNIHUB_CATALOG_PRODUCT_ID": "superwagie", "UNIHUB_CATALOG_INSTANCE_ID": "fixture"} {
		t.Setenv(k, v)
	}
	engine := gin.New()
	SetManagedKeyDispatch(engine, adapter)
	SetModelCatalogRouter(engine, adapter, platformbilling.CatalogConfigFromEnv(func(k string) string {
		switch k {
		case "UNIHUB_CATALOG_EXPORT_TOKEN":
			return strings.Repeat("c", 32)
		case "UNIHUB_CATALOG_PRODUCT_ID":
			return "superwagie"
		case "UNIHUB_CATALOG_INSTANCE_ID":
			return "fixture"
		}
		return ""
	}))
	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	w := request("/v1/models", "uh_key_test")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"object":"list"`)
	assert.Contains(t, w.Body.String(), `"capability_metadata"`)
	assert.Contains(t, w.Body.String(), `"supports_tools":true`)
	assert.NotContains(t, w.Body.String(), "hidden")
	assert.NotContains(t, w.Body.String(), "supplier-secret")
	assert.Equal(t, 404, request("/v1/models/hidden", "uh_key_test").Code)
	w = request("/internal/v1/model-catalog?product_id=superwagie&instance_id=fixture", strings.Repeat("c", 32))
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "hidden")
	assert.NotContains(t, w.Body.String(), "supplier-secret")
	assert.Equal(t, http.StatusUnauthorized, request("/api/unihub/chat-models", "").Code)
	assert.Equal(t, 2, verifies)
	var calls int64
	require.NoError(t, db.Model(&platformbilling.Call{}).Count(&calls).Error)
	assert.Zero(t, calls)
}

func TestCatalogDoesNotAdvertiseBlockedNativeProducer(t *testing.T) {
	old := model.DB
	t.Cleanup(func() { model.DB = old })
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/producer.db"), &gorm.Config{})
	require.NoError(t, e)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeAnthropic, Key: "secret", Status: common.ChannelStatusEnabled, Models: "chat", Group: "platform"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "chat", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "chat", DisplayName: "Chat", Client: "all", Enabled: true, Metadata: `{"operations":["chat.completions.create"]}`}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "chat", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	adapter, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: "http://127.0.0.1:1", ServiceToken: strings.Repeat("f", 32), InstanceID: "fixture", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CNY/6": {"chat": "rate"}}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"chat": {"chat": {Policy: "provider_reported_v1"}}}})
	require.NoError(t, e)
	result, e := readTechnicalCatalog(context.Background(), adapter, "superwagie")
	require.NoError(t, e)
	require.Len(t, result.Models, 1)
	assert.False(t, result.Models[0].Operations[0].Available)
	assert.Equal(t, "managed_producer_unavailable", result.Models[0].Operations[0].DisabledReason)
}

func TestCatalogVideoPolicyRequiresEverySelectableProducer(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		channels     []int
		available    bool
	}{
		{"sora_matching", "sora_completed_seconds_v1", []int{constant.ChannelTypeSora}, true},
		{"kling_matching", "kling_final_units_v1", []int{constant.ChannelTypeKling}, true},
		{"doubao_matching", "doubao_completion_tokens_v1", []int{constant.ChannelTypeDoubaoVideo}, true},
		{"wan_old_policy", "ali_wan26_t2v_billable_duration_v1", []int{constant.ChannelTypeAli}, false},
		{"wan_missing_certificate", "ali_wan26_t2v_cny6_v2", []int{constant.ChannelTypeAli}, false},
		{"wan_with_sora_receipt", "sora_completed_seconds_v1", []int{constant.ChannelTypeAli}, false},
		{"kling_with_sora_receipt", "sora_completed_seconds_v1", []int{constant.ChannelTypeKling}, false},
		{"gemini_unmeasured_veo", "sora_completed_seconds_v1", []int{constant.ChannelTypeGemini}, false},
		{"mixed_sora_kling", "sora_completed_seconds_v1", []int{constant.ChannelTypeSora, constant.ChannelTypeKling}, false},
		{"matching_candidates", "sora_completed_seconds_v1", []int{constant.ChannelTypeSora, constant.ChannelTypeSora}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := model.DB
			t.Cleanup(func() { model.DB = old })
			db, e := gorm.Open(sqlite.Open(t.TempDir()+"/video.db"), &gorm.Config{})
			require.NoError(t, e)
			model.DB = db
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "video", DisplayName: "Video", Client: "all", Enabled: true, Metadata: `{"operations":["videos.create"],"async":true}`}).Error)
			for i, kind := range tc.channels {
				require.NoError(t, db.Create(&model.Channel{Id: i + 1, Type: kind, Key: "synthetic", Status: common.ChannelStatusEnabled, Models: "video", Group: "platform"}).Error)
				require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "video", ChannelId: i + 1, Enabled: true}).Error)
				require.NoError(t, db.Create(&model.ClientModelTarget{Id: i + 1, RouteId: 1, ChannelId: i + 1, UpstreamModel: "video", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
			}
			adapter, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: "http://127.0.0.1:1", ServiceToken: strings.Repeat("f", 32), InstanceID: "fixture", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"video": {"video": {Policy: tc.policy, MaxQuantity: 30}}}})
			require.NoError(t, e)
			got, e := readTechnicalCatalog(context.Background(), adapter, "superwagie")
			require.NoError(t, e)
			require.Len(t, got.Models, 1)
			require.Len(t, got.Models[0].Operations, 1)
			op := got.Models[0].Operations[0]
			assert.Equal(t, tc.available, op.Available)
			assert.False(t, op.Executable)
			if !tc.available {
				assert.Equal(t, "managed_producer_policy_mismatch", op.DisabledReason)
			}
		})
	}
}

func TestCatalogNativeVisionUsesExactCertifiedProducer(t *testing.T) {
	oldDB := model.DB
	t.Cleanup(func() { model.DB = oldDB })
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/catalog.db"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	config := platformbilling.Config{Enabled: true, Origin: "http://127.0.0.1:1", ServiceToken: strings.Repeat("s", 32), PrincipalID: 1, Group: "platform", MaxInputTokens: 10000, MaxOutputTokens: 100, MaxBodyBytes: 32768, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"chat": {}, "responses": {}}, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CREDIT/6": {}}, "responses": {"CREDIT/6": {}}}, NativeInputPolicies: map[string]map[string]platformbilling.NativeInputPolicy{}}
	for i, tc := range []struct {
		id, wire, preferred, op string
		channel                 int
	}{{"chat", "chat.completions.create", "openai_chat", "chat", constant.ChannelTypeOpenAI}, {"responses", "responses.create", "responses", "responses", constant.ChannelTypeOpenAI}, {"claude", "claude.messages.create", "anthropic", "chat", constant.ChannelTypeAnthropic}, {"gemini", "gemini.generate", "gemini", "chat", constant.ChannelTypeGemini}} {
		require.NoError(t, db.Create(&model.Channel{Id: i + 1, Type: tc.channel, Key: "fixture", Status: common.ChannelStatusEnabled, Models: tc.id, Group: "platform"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: tc.id, ChannelId: i + 1, Enabled: true}).Error)
		metadata, err := common.Marshal(map[string]any{"operations": []string{tc.wire}, "input_modalities": []string{"text", "image", "document"}, "output_modalities": []string{"text"}, "streaming": true})
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: i + 1, PublicId: tc.id, DisplayName: tc.id, Client: "all", Enabled: true, Metadata: string(metadata)}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: i + 1, RouteId: i + 1, ChannelId: i + 1, UpstreamModel: tc.id, PreferredWires: `["` + tc.preferred + `"]`, Enabled: true, Weight: 1}).Error)
		config.OperationRateIDs[tc.op]["CREDIT/6"][tc.id] = "rate"
		config.OperationLimits[tc.op][tc.id] = platformbilling.OperationLimits{Policy: "sum"}
		config.NativeInputPolicies[tc.wire] = map[string]platformbilling.NativeInputPolicy{tc.id: {Version: "hard_context_v1", ProviderReference: "https://fixture.example/context", ChannelType: tc.channel, UpstreamModel: tc.id, HardInputTokens: 10000, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 2}}
	}
	a, err := platformbilling.New(db, config)
	require.NoError(t, err)
	catalog, err := readTechnicalCatalog(context.Background(), a, "superwagie")
	require.NoError(t, err)
	require.Len(t, catalog.Models, 4)
	for _, m := range catalog.Models {
		require.Len(t, m.Operations, 1)
		assert.True(t, m.Operations[0].Available, m.ID+":"+m.Operations[0].DisabledReason)
		assert.Equal(t, 100, *m.Capabilities.MaxOutputTokens)
		require.NotNil(t, m.Capabilities.MediaLimits)
		require.NotNil(t, m.Capabilities.MediaLimits.MaxImages)
		assert.Equal(t, 2, *m.Capabilities.MediaLimits.MaxImages)
		assert.Equal(t, []string{"image/png", "image/jpeg", "application/pdf", "text/plain"}, m.Capabilities.MediaLimits.Formats)
	}
	policy := config.NativeInputPolicies["claude.messages.create"]["claude"]
	policy.MaxDocuments = 0
	config.NativeInputPolicies["claude.messages.create"]["claude"] = policy
	delete(config.NativeInputPolicies, "gemini.generate")
	a, err = platformbilling.New(db, config)
	require.NoError(t, err)
	catalog, err = readTechnicalCatalog(context.Background(), a, "superwagie")
	require.NoError(t, err)
	for _, m := range catalog.Models {
		if m.ID == "gemini" || m.ID == "claude" {
			assert.False(t, m.Operations[0].Available)
			assert.Equal(t, "certified_media_reservation_unavailable", m.Operations[0].DisabledReason)
		}
	}
}

func TestChatCatalogProjectionRequiresAvailableChatAndAssetPins(t *testing.T) {
	catalog := platformbilling.TechnicalCatalog{ProductID: "llmrouter", InstanceID: "default", Models: []platformbilling.CatalogModel{
		{ID: "chat", Operations: []platformbilling.CatalogOperation{{ID: "chat.completions.create", Available: true, Policy: "provider_reported_v1", RateIDs: map[string]string{"CNY/6": "cash-rate"}}}},
		{ID: "disabled", Operations: []platformbilling.CatalogOperation{{ID: "chat.completions.create", Available: false, RateIDs: map[string]string{"CNY/6": "rate"}}}},
		{ID: "rerank", Operations: []platformbilling.CatalogOperation{{ID: "rerank.create", Available: true, RateIDs: map[string]string{"CNY/6": "rate"}}}},
		{ID: "unpinned", Operations: []platformbilling.CatalogOperation{{ID: "chat.completions.create", Available: true}}},
	}}
	result := projectChatCatalog(catalog, "metered_v2", map[string]map[string]string{"CNY/6": {"chat": "cash-rate"}})
	require.Len(t, result, 2)
	assert.Equal(t, "chat", result[0].ID)
	assert.Equal(t, "provider_reported_v1", result[0].Policy)
	assert.Equal(t, map[string]string{"CNY/6": "cash-rate"}, result[0].RateIDs)
}
