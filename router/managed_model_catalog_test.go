package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestManagedModelCatalogProjectsVerifiedOperationAuthority(t *testing.T) {
	enableUniHubBoundary(t)
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldDB, oldLimit := model.DB, common.GlobalApiRateLimitEnable
	t.Cleanup(func() { model.DB, common.GlobalApiRateLimitEnable = oldDB, oldLimit })
	common.GlobalApiRateLimitEnable = false
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/managed-catalog.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB = db
	for i, entry := range []struct{ id, metadata string }{
		{"dual", `{"operations":["chat.completions.create","responses.create"],"supports_tools":true}`},
		{"blocked", `{"operations":["chat.completions.create"]}`},
		{"unknown", `{}`},
		{"embedding", `{"operations":["embeddings.create"]}`},
		{"rerank", `{"operations":["rerank.create"]}`},
		{"multimodal", `{"operations":["multimodal.embeddings.create"]}`},
		{"hidden", `{"operations":["chat.completions.create"]}`},
		{"qwen3-rerank", `{"operations":["rerank.create"]}`},
	} {
		channelType := constant.ChannelTypeOpenAI
		upstream := entry.id
		if entry.id == "embedding" || entry.id == "rerank" || entry.id == "multimodal" || entry.id == "qwen3-rerank" {
			channelType = constant.ChannelTypeAli
		}
		if entry.id == "rerank" {
			upstream = "qwen3-rerank"
		}
		require.NoError(t, db.Create(&model.Channel{Id: i + 1, Type: channelType, Key: "fixture-supplier", Status: common.ChannelStatusEnabled, Models: upstream, Group: "platform"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: upstream, ChannelId: i + 1, Enabled: true}).Error)
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: i + 1, PublicId: entry.id, DisplayName: entry.id, Client: "all", Enabled: true, Metadata: entry.metadata}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: i + 1, RouteId: i + 1, ChannelId: i + 1, UpstreamModel: upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	}
	grants := map[string]map[string][]string{
		"uh_key_chat":      {"dual": {"chat.completions.create"}, "blocked": {"chat.completions.create"}, "unknown": {"chat.completions.create"}, "missing": {"chat.completions.create"}, "embedding": {"embeddings.create"}, "rerank": {"rerank.create"}, "multimodal": {"multimodal.embeddings.create"}, "qwen3-rerank": {"rerank.create"}},
		"uh_key_responses": {"dual": {"responses.create"}},
		"uh_key_unknown":   nil,
	}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/model-credentials/verify" {
			t.Errorf("model discovery must not call billing or a provider: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var input map[string]string
		if err := common.DecodeJson(r.Body, &input); !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Empty(t, input["model"])
		assert.Equal(t, "fixture", input["instance_id"])
		operations, ok := grants[input["token"]]
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body, err := common.Marshal(platformbilling.Claims{ID: "key", CredentialKind: "api_key", InstanceID: "fixture", Audience: "llmrouter", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", Models: []string{"dual", "blocked", "unknown", "missing", "embedding", "rerank", "multimodal", "qwen3-rerank"}, Operations: operations})
		if assert.NoError(t, err) {
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(hub.Close)
	keyHash := sha256.Sum256([]byte("fixture-supplier"))
	knowledgePolicies := map[string]platformbilling.KnowledgePolicy{}
	for name, channel := range map[string]int{"rerank": 5, "qwen3-rerank": 8} {
		knowledgePolicies[name] = platformbilling.KnowledgePolicy{Version: "knowledge_v1", ProviderReference: "https://help.aliyun.com/zh/model-studio/text-rerank", PublicModel: name, WireOperation: "rerank.create", ChannelID: channel, ChannelType: constant.ChannelTypeAli, ChannelKeySHA256: hex.EncodeToString(keyHash[:]), UpstreamModel: "qwen3-rerank", HardInputTokens: 1000, MaxItemTokens: 100, MaxBatch: 1, RateIDs: map[string]string{"CNY/6": "rerank-rate"}}
	}
	adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("f", 32), InstanceID: "fixture", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100,
		AllowAliRerank: true, AliEmbeddingResultKey: []byte(strings.Repeat("k", 32)), KnowledgePolicies: map[string]map[string]platformbilling.KnowledgePolicy{"rerank.create": knowledgePolicies},
		OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CNY/6": {"dual": "chat-rate"}}, "responses": {"CNY/6": {"dual": "responses-rate"}}},
		OperationLimits:  map[string]map[string]platformbilling.OperationLimits{"chat": {"dual": {Policy: "provider_reported_v1"}}, "responses": {"dual": {Policy: "provider_reported_v1"}}},
	})
	require.NoError(t, err)
	_, err = readTechnicalCatalog(context.Background(), adapter, "superwagie")
	require.NoError(t, err)
	for k, v := range map[string]string{"UNIHUB_CATALOG_EXPORT_TOKEN": strings.Repeat("c", 32), "UNIHUB_CATALOG_PRODUCT_ID": "superwagie", "UNIHUB_CATALOG_INSTANCE_ID": "fixture"} {
		t.Setenv(k, v)
	}
	engine := gin.New()
	SetManagedKeyDispatch(engine, adapter)
	type entry struct {
		ID             string                        `json:"id"`
		Version        string                        `json:"catalog_version"`
		Metadata       *platformbilling.CatalogModel `json:"capability_metadata"`
		DisabledReason string                        `json:"disabled_reason"`
	}
	read := func(path, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	versions := map[string]string{}
	for _, tc := range []struct {
		key, wire, reason string
		executable        bool
	}{
		{"uh_key_chat", "chat.completions.create", "", true},
		{"uh_key_chat", "responses.create", "operation_not_authorized", false},
		{"uh_key_responses", "responses.create", "", true},
		{"uh_key_unknown", "chat.completions.create", "operation_authorization_unknown", false},
	} {
		t.Run(tc.key+"/"+tc.wire, func(t *testing.T) {
			w := read("/v1/models/dual", tc.key)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var result entry
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
			require.NotNil(t, result.Metadata)
			versions[tc.key] = result.Version
			var operation *platformbilling.CatalogOperation
			for i := range result.Metadata.Operations {
				if result.Metadata.Operations[i].ID == tc.wire {
					operation = &result.Metadata.Operations[i]
				}
			}
			require.NotNil(t, operation)
			assert.True(t, operation.Available, "technical availability is independent of key authorization")
			assert.Equal(t, tc.executable, operation.Executable)
			assert.Equal(t, tc.reason, operation.DisabledReason)
		})
	}
	assert.NotEqual(t, versions["uh_key_chat"], versions["uh_key_responses"], "operation authority changes the credential-scoped catalog version")
	w := read("/v1/models", "uh_key_chat")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list struct {
		Data []entry `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Data, 8)
	for _, item := range list.Data {
		assert.Equal(t, versions["uh_key_chat"], item.Version)
		switch item.ID {
		case "missing":
			assert.Nil(t, item.Metadata)
			assert.Equal(t, "capability_metadata_unavailable", item.DisabledReason)
		case "unknown":
			require.NotNil(t, item.Metadata)
			assert.Empty(t, item.Metadata.Operations, "a model grant does not establish chat capability")
			assert.Empty(t, item.Metadata.Capabilities.Operations)
			assert.Equal(t, "capabilities_unconfigured", item.Metadata.DisabledReason)
		case "blocked":
			require.NotNil(t, item.Metadata)
			require.Len(t, item.Metadata.Operations, 1)
			assert.False(t, item.Metadata.Operations[0].Executable)
			assert.Equal(t, "usage_policy_unconfigured", item.Metadata.Operations[0].DisabledReason)
		case "embedding", "multimodal":
			require.NotNil(t, item.Metadata)
			require.Len(t, item.Metadata.Operations, 1)
			assert.False(t, item.Metadata.Operations[0].Executable)
			assert.Equal(t, "certified_knowledge_target_unavailable", item.Metadata.Operations[0].DisabledReason)
		case "rerank":
			require.NotNil(t, item.Metadata)
			require.Len(t, item.Metadata.Operations, 1)
			assert.True(t, item.Metadata.Operations[0].Available)
			assert.True(t, item.Metadata.Operations[0].Executable)
			assert.Empty(t, item.Metadata.Operations[0].DisabledReason)
		case "qwen3-rerank":
			require.NotNil(t, item.Metadata)
			require.Len(t, item.Metadata.Operations, 1)
			assert.True(t, item.Metadata.Operations[0].Available)
			assert.False(t, item.Metadata.Operations[0].Executable)
			assert.Equal(t, "knowledge_route_unavailable", item.Metadata.Operations[0].DisabledReason)
		}
	}
	assert.Equal(t, http.StatusNotFound, read("/v1/models/hidden", "uh_key_chat").Code)
	assert.Equal(t, http.StatusForbidden, read("/v1/models", "uh_key_revoked").Code)
	var calls int64
	require.NoError(t, db.Model(&platformbilling.Call{}).Count(&calls).Error)
	assert.Zero(t, calls)
}

func TestManagedModelCatalogRequiresCurrentOriginKnowledgeBinding(t *testing.T) {
	f := newPairedFixture(t, false)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/model-credentials/verify" {
			t.Errorf("model discovery must only verify authority: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var input map[string]string
		if !assert.NoError(t, common.DecodeJson(r.Body, &input)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, map[string]string{"token": input["token"], "model": "", "instance_id": "default"}, input)
		product := strings.TrimPrefix(input["token"], "uh_key_")
		body, err := common.Marshal(platformbilling.Claims{ID: "key", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter", ActorID: "actor", AccountID: "payer", OriginProductID: product, Models: []string{f.name}, Operations: map[string][]string{f.name: {f.wire}}})
		if assert.NoError(t, err) {
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(hub.Close)
	config := f.config
	config.Origin = hub.URL
	adapter, err := platformbilling.New(f.db, config)
	require.NoError(t, err)
	InstallKnowledgeBindings(adapter)
	for k, v := range map[string]string{"UNIHUB_CATALOG_EXPORT_TOKEN": strings.Repeat("c", 32), "UNIHUB_CATALOG_PRODUCT_ID": "superwagie", "UNIHUB_CATALOG_INSTANCE_ID": "default"} {
		t.Setenv(k, v)
	}
	engine := gin.New()
	SetManagedKeyDispatch(engine, adapter)
	type entry struct {
		ID             string                        `json:"id"`
		Version        string                        `json:"catalog_version"`
		Metadata       *platformbilling.CatalogModel `json:"capability_metadata"`
		DisabledReason string                        `json:"disabled_reason"`
	}
	read := func(product string) entry {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer uh_key_"+product)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var list struct {
			Data []entry `json:"data"`
		}
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &list))
		require.Len(t, list.Data, 1, "binding readiness must not change the verified model scope")
		assert.Equal(t, f.name, list.Data[0].ID)
		return list.Data[0]
	}
	check := func(product string, executable bool) string {
		t.Helper()
		item := read(product)
		require.NotNil(t, item.Metadata)
		require.Len(t, item.Metadata.Operations, 1)
		op := item.Metadata.Operations[0]
		assert.True(t, op.Available, "the certified policy remains technically available")
		assert.Equal(t, executable, op.Executable)
		if executable {
			assert.Empty(t, op.DisabledReason)
		} else {
			assert.Equal(t, "knowledge_route_unavailable", op.DisabledReason)
		}
		return item.Version
	}

	require.NoError(t, f.db.Where("origin_product_id = ?", "llmrouter").Delete(&model.KnowledgeActiveBinding{}).Error)
	unavailableVersion := check("llmrouter", false)
	check("superwagie", true)
	bindings, err := adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"llmrouter", "superwagie"})
	require.NoError(t, err)
	activeVersion := check("llmrouter", true)
	assert.NotEqual(t, unavailableVersion, activeVersion)
	assert.NotEqual(t, activeVersion, check("superwagie", true), "catalog versions retain the verified origin even with identical capabilities")
	t.Setenv("UNIHUB_CATALOG_PRODUCT_ID", "unrelated-export")
	t.Setenv("UNIHUB_CATALOG_INSTANCE_ID", "unrelated-export")
	assert.Equal(t, activeVersion, check("llmrouter", true), "export scope is only an enablement gate for credential discovery")
	require.NoError(t, f.db.Where("pin = ?", bindings[0].PolicyDigest).Delete(&model.KnowledgeRoutePin{}).Error)
	check("llmrouter", false)
	check("superwagie", true)

	require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 2).Update("client", "llmrouter").Error)
	check("llmrouter", false)
	otherOrigin := read("superwagie")
	assert.Nil(t, otherOrigin.Metadata, "metadata from another product must not cross the verified origin")
	assert.Equal(t, "capability_metadata_unavailable", otherOrigin.DisabledReason)
	assert.Zero(t, f.reserves)
	assert.Zero(t, f.sends)
	assert.Zero(t, f.settles)
	var calls int64
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Count(&calls).Error)
	assert.Zero(t, calls)
}
