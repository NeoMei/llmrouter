package router

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProviderCatalogRequiresServiceAuthority(t *testing.T) {
	engine := gin.New()
	SetModelCatalogRouter(engine, nil, platformbilling.CatalogConfig{Token: strings.Repeat("c", 32), ProductID: "llmrouter", InstanceID: "fixture"})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-catalog", strings.NewReader(`{"origin_product_id":"superwagie","instance_id":"fixture","actor_id":"actor","account_id":"team"}`))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
}

func providerCatalogFixture(t *testing.T) (*platformbilling.Adapter, *gorm.DB) {
	t.Helper()
	previousDB := model.DB
	previousPrice := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrice))
		model.InvalidatePricingCache()
	})
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/provider.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Model{}, &model.Vendor{}))
	model.DB = db
	model.InvalidatePricingCache()
	adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: "https://billing.example.test", ServiceToken: strings.Repeat("s", 32), InstanceID: "fixture", PrincipalID: 1, Group: "legacy", NativeGroup: "native", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "private-supplier-key", Status: common.ChannelStatusEnabled, Group: "native", Models: "fixture"}).Error)
	return adapter, db
}

func TestProviderCatalogIncludesEveryAvailableModelAndNewModels(t *testing.T) {
	adapter, db := providerCatalogFixture(t)
	prices := map[string]float64{}
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("fixture-chat-%03d", i)
		prices[id] = 0.01
		require.NoError(t, db.Create(&model.Ability{Group: "native", Model: id, ChannelId: 1, Enabled: true}).Error)
	}
	prices["other-group"] = 1
	prices["disabled-channel"] = 1
	require.NoError(t, db.Create(&model.Ability{Group: "other", Model: "other-group", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "no-native-price", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 2, Type: constant.ChannelTypeOpenAI, Key: "another-secret", Status: common.ChannelStatusManuallyDisabled}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "disabled-channel", ChannelId: 2, Enabled: true}).Error)
	raw, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(raw)))
	grant := platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team", Models: []string{"one-old-model"}}
	first, err := readProviderCatalog(context.Background(), adapter, grant)
	require.NoError(t, err)
	require.Len(t, first.Models, 105)
	require.Equal(t, "team", first.AccountID)
	require.Equal(t, "llmrouter_available", first.ModelScope)
	for _, m := range first.Models {
		require.Equal(t, []string{"chat"}, m.Operations)
		require.True(t, m.CapabilityMetadata.Operations[0].Executable)
	}
	repeated, err := readProviderCatalog(context.Background(), adapter, grant)
	require.NoError(t, err)
	require.Equal(t, first.CatalogVersion, repeated.CatalogVersion)
	prices["new-chat"] = 0
	raw, err = common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(raw)))
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "new-chat", ChannelId: 1, Enabled: true}).Error)
	second, err := readProviderCatalog(context.Background(), adapter, grant)
	require.NoError(t, err)
	require.Len(t, second.Models, 106)
	require.NotEqual(t, first.CatalogVersion, second.CatalogVersion)
	encoded, err := common.Marshal(second)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-supplier-key")
	require.NotContains(t, string(encoded), "billing.example.test")
	require.NotContains(t, string(encoded), "other-group")
	require.NotContains(t, string(encoded), "disabled-channel")
}

func TestProviderCatalogRetainsExplicitOCRCapabilities(t *testing.T) {
	adapter, db := providerCatalogFixture(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"PaddleOCR-VL-1.6":0}`))
	require.NoError(t, db.Create(&model.Channel{Id: 3, Type: constant.ChannelTypePaddleOCR, Key: "private-ocr-key", Status: common.ChannelStatusEnabled, Group: "native"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "PaddleOCR-VL-1.6", ChannelId: 3, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "PaddleOCR-VL-1.6", DisplayName: "Document OCR", Client: "all", Enabled: true, Metadata: `{"operations":["ocr.jobs.create","ocr.jobs.retrieve","ocr.resources.retrieve"],"input_modalities":["document","image"],"output_modalities":["text"],"async":true}`}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 3, UpstreamModel: "PaddleOCR-VL-1.6", BillingModel: "PaddleOCR-VL-1.6", PreferredWires: `["paddleocr_jobs"]`, Enabled: true, Weight: 1}).Error)
	catalog, err := readProviderCatalog(context.Background(), adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
	require.NoError(t, err)
	require.Len(t, catalog.Models, 1)
	m := catalog.Models[0]
	require.Equal(t, []string{"ocr"}, m.Operations)
	require.Equal(t, []string{"document", "image"}, m.InputModalities)
	require.Len(t, m.CapabilityMetadata.Operations, 1)
	require.Equal(t, "/v1/ocr", m.CapabilityMetadata.Operations[0].Endpoint)
	require.True(t, m.CapabilityMetadata.Operations[0].Executable)
}

func TestProviderCatalogRejectsRoutingSelectorsAndScopeMismatch(t *testing.T) {
	adapter, _ := providerCatalogFixture(t)
	config := platformbilling.CatalogConfig{Token: strings.Repeat("c", 32), ProductID: "llmrouter", InstanceID: "fixture"}
	engine := gin.New()
	SetProviderCatalogRouter(engine, adapter, config)
	for _, body := range []string{
		`{"origin_product_id":"unrelated","instance_id":"fixture","actor_id":"actor","account_id":"team"}`,
		`{"origin_product_id":"superwagie","instance_id":"other","actor_id":"actor","account_id":"team"}`,
		`{"origin_product_id":"superwagie","instance_id":"fixture","actor_id":"","account_id":"team"}`,
		`{"origin_product_id":"superwagie","instance_id":"fixture","actor_id":"actor","account_id":"team","group":"admin"}`,
		`{"origin_product_id":"superwagie","instance_id":"fixture","actor_id":"actor","account_id":"team","account_id":"other"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-catalog", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+config.Token)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code, body)
	}
}

func TestProviderCatalogBindsDiscoveryToAccountWithoutBilling(t *testing.T) {
	adapter, _ := providerCatalogFixture(t)
	engine := gin.New()
	config := platformbilling.CatalogConfig{Token: strings.Repeat("c", 32), ProductID: "llmrouter", InstanceID: "fixture"}
	SetProviderCatalogRouter(engine, adapter, config)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-catalog", strings.NewReader(`{"origin_product_id":"superwagie","instance_id":"fixture","actor_id":"actor","account_id":"team"}`))
	request.Header.Set("Authorization", "Bearer "+config.Token)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var catalog ProviderCatalog
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &catalog))
	require.Equal(t, "team", catalog.AccountID)
	require.Equal(t, "actor", catalog.ActorID)
	require.Equal(t, "superwagie", catalog.OriginProductID)
	require.Equal(t, "fixture", catalog.InstanceID)
	require.Equal(t, "llmrouter_native", catalog.PricingSource)
	require.Empty(t, catalog.Models)
	// The configured billing origin is deliberately unreachable. Discovery only
	// reads the router's own catalog and must not attempt a reserve or settlement.
}

func TestProviderCatalogDoesNotAdvertiseUnimplementedNativeOperations(t *testing.T) {
	projected := projectProviderModel(platformbilling.CatalogModel{ID: "future-model", Name: "Future model", Capabilities: dto.ModelCapabilities{Operations: []string{"audio.translations.create"}}})
	require.False(t, projected.Probe)
	require.Equal(t, "capabilities_unconfigured", projected.CapabilityMetadata.DisabledReason)
}

func TestProviderCatalogUsesCodexAdapterContractWithoutModelNameGuess(t *testing.T) {
	adapter, db := providerCatalogFixture(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ordinary-name":0.01}`))
	require.NoError(t, db.Create(&model.Channel{Id: 4, Type: constant.ChannelTypeCodex, Key: "private-codex-token", Status: common.ChannelStatusEnabled, Group: "native"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "ordinary-name", ChannelId: 4, Enabled: true}).Error)
	catalog, err := readProviderCatalog(context.Background(), adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
	require.NoError(t, err)
	require.Len(t, catalog.Models, 1)
	m := catalog.Models[0]
	require.Equal(t, []string{"responses"}, m.Operations)
	require.True(t, slices.ContainsFunc(m.CapabilityMetadata.Operations, func(op platformbilling.CatalogOperation) bool { return op.ID == "responses.create" && op.Executable }))
	require.NotContains(t, m.CapabilityMetadata.Capabilities.Operations, "chat.completions.create")
}

func TestProviderCatalogSupportsNativeOnlyPricesPerOperation(t *testing.T) {
	adapter, db := providerCatalogFixture(t)
	before := billing_setting.GetModelNativePricingCopy()
	t.Cleanup(func() { require.NoError(t, billing_setting.SetModelNativePricing(before)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	prices := map[string]map[string]metering.Price{
		"qwen-image-2.0": {"image_generation": {Version: "metered_v2", Operation: "image_generation", Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "images", Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "10"}}}}},
		"native-ocr":     {"ocr": {Version: "metered_v2", Operation: "ocr", Policy: "success_request", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "0", Denominator: "1"}}}}},
	}
	require.NoError(t, billing_setting.SetModelNativePricing(prices))
	for _, row := range []struct {
		id              int
		name            string
		kind            int
		metadata, wires string
	}{
		{3, "qwen-image-2.0", constant.ChannelTypeAli, `{"operations":["dashscope.images.generate","chat.completions.create"],"input_modalities":["text"],"output_modalities":["image"]}`, `["openai_chat"]`},
		{4, "native-ocr", constant.ChannelTypePaddleOCR, `{"operations":["ocr.jobs.create"],"input_modalities":["document"],"output_modalities":["text"]}`, `["paddleocr_jobs"]`},
	} {
		require.NoError(t, db.Create(&model.Channel{Id: row.id, Type: row.kind, Key: "supplier-secret", Status: common.ChannelStatusEnabled, Group: "native"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "native", Model: row.name, ChannelId: row.id, Enabled: true}).Error)
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: row.id, PublicId: row.name, DisplayName: row.name, Client: "all", Enabled: true, Metadata: row.metadata}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: row.id, RouteId: row.id, ChannelId: row.id, UpstreamModel: row.name, BillingModel: row.name, PreferredWires: row.wires, Enabled: true, Weight: 1}).Error)
	}
	catalog, err := readProviderCatalog(context.Background(), adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
	require.NoError(t, err)
	require.Len(t, catalog.Models, 2)
	for _, m := range catalog.Models {
		for _, op := range m.CapabilityMetadata.Operations {
			if op.ID == "chat.completions.create" {
				require.False(t, op.Executable)
				require.Equal(t, "native_price_unavailable", op.DisabledReason)
			} else {
				require.True(t, op.Executable, op.ID)
			}
		}
	}
	selected, _, err := managedQwenImageTarget(context.Background(), adapter, "qwen-image-2.0")
	require.NoError(t, err)
	_, quota, err := managedQwenImagePrice(selected, 1, 1024, 1024)
	require.NoError(t, err)
	require.Positive(t, quota)
	for _, change := range []map[string]any{
		{"channel_info": model.ChannelInfo{IsMultiKey: true}},
		{"channel_info": model.ChannelInfo{}, "key": ""},
	} {
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 3).Updates(change).Error)
		current, err := readProviderCatalog(context.Background(), adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
		require.NoError(t, err)
		image := current.Models[slices.IndexFunc(current.Models, func(m ProviderCatalogModel) bool { return m.ID == "qwen-image-2.0" })]
		require.False(t, image.Probe)
		for _, op := range image.CapabilityMetadata.Operations {
			if op.ID == "dashscope.images.generate" {
				require.False(t, op.Executable)
				require.Equal(t, "native_image_target_unavailable", op.DisabledReason)
			}
		}
	}
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 4).Update("channel_info", model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled}}).Error)
	pooled, err := readProviderCatalog(context.Background(), adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
	require.NoError(t, err)
	pooledOCR := pooled.Models[slices.IndexFunc(pooled.Models, func(m ProviderCatalogModel) bool { return m.ID == "native-ocr" })]
	require.True(t, pooledOCR.Probe)
	require.True(t, pooledOCR.CapabilityMetadata.Operations[0].Executable)
	for _, change := range []map[string]any{
		{"channel_info": model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusAutoDisabled}}},
		{"channel_info": model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusAutoDisabled}}, "key": "first-key\nsecond-key"},
		{"channel_info": model.ChannelInfo{}, "key": ""},
	} {
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 4).Updates(change).Error)
		current, err := readProviderCatalog(context.Background(), adapter, platformbilling.Claims{OriginProductID: "superwagie", InstanceID: "fixture", ActorID: "actor", AccountID: "team"})
		require.NoError(t, err)
		ocr := current.Models[slices.IndexFunc(current.Models, func(m ProviderCatalogModel) bool { return m.ID == "native-ocr" })]
		require.False(t, ocr.Probe)
		require.Equal(t, "native_ocr_target_unavailable", ocr.CapabilityMetadata.Operations[0].DisabledReason)
	}
}

func TestNativeCreditProtocolNegotiationRequiresConfiguredService(t *testing.T) {
	adapter, _ := providerCatalogFixture(t)
	config := platformbilling.CatalogConfig{Token: strings.Repeat("c", 32), ProductID: "llmrouter", InstanceID: "fixture"}
	engine := gin.New()
	SetProviderCatalogRouter(engine, adapter, config)
	for _, tc := range []struct {
		body, token string
		status      int
	}{
		{`{"origin_product_id":"superwagie","purpose":"native_credit_readiness"}`, config.Token, 200},
		{`{"origin_product_id":"superwagie","purpose":"native_credit_readiness"}`, "wrong", 401},
		{`{"origin_product_id":"superwagie","purpose":"native_credit_readiness","actor_id":"actor"}`, config.Token, 403},
		{`{"origin_product_id":"other","purpose":"native_credit_readiness"}`, config.Token, 403},
	} {
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-catalog", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		res := httptest.NewRecorder()
		engine.ServeHTTP(res, req)
		require.Equal(t, tc.status, res.Code, res.Body.String())
		if tc.status == 200 {
			var response map[string]any
			require.NoError(t, common.Unmarshal(res.Body.Bytes(), &response))
			require.Equal(t, "lr-native-credit-v1", response["native_credit_protocol"])
			require.Equal(t, "native_credit_readiness", response["purpose"])
		}
	}
	disabled := gin.New()
	SetProviderCatalogRouter(disabled, nil, config)
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-catalog", strings.NewReader(`{"origin_product_id":"superwagie","purpose":"native_credit_readiness"}`))
	req.Header.Set("Authorization", "Bearer "+config.Token)
	res := httptest.NewRecorder()
	disabled.ServeHTTP(res, req)
	require.Equal(t, 503, res.Code)
}
