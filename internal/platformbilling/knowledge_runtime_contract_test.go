package platformbilling

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func knowledgeRuntimeEnvironment(t *testing.T, origin string) map[string]string {
	t.Helper()
	embedding := KnowledgePolicy{Version: "knowledge_ali_text_v1", ProviderReference: "https://help.aliyun.com/zh/model-studio/text-embedding-v4", PublicModel: "unihub-text-embedding-v4", WireOperation: "embeddings.create", ChannelID: 102, ChannelType: constant.ChannelTypeAli, ChannelKeySHA256: strings.Repeat("a", 64), Origin: "https://dashscope.aliyuncs.com", UpstreamModel: "text-embedding-v4", HardInputTokens: 4096, MaxItemTokens: 4096, MaxBatch: 1, Space: &dto.EmbeddingSpace{Provider: "aliyun", Model: "text-embedding-v4", Version: "v4", Dimensions: 1024, InputSemantics: "text"}, RateIDs: map[string]string{"CNY/6": "knowledge-text-embedding-v4-input-cny-v1"}}
	rerank := KnowledgePolicy{Version: "knowledge_v1", ProviderReference: "https://help.aliyun.com/zh/model-studio/text-rerank", PublicModel: "qwen3-rerank", WireOperation: "rerank.create", ChannelID: 103, ChannelType: constant.ChannelTypeAli, ChannelKeySHA256: strings.Repeat("b", 64), UpstreamModel: "qwen3-rerank", HardInputTokens: 4096, MaxItemTokens: 2048, MaxBatch: 1, RateIDs: map[string]string{"CNY/6": "knowledge-qwen3-rerank-input-cny-v1"}}
	policies, err := common.Marshal(map[string]map[string]KnowledgePolicy{"embeddings.create": {embedding.PublicModel: embedding}, "rerank.create": {rerank.PublicModel: rerank}})
	require.NoError(t, err)
	return map[string]string{
		"UNIHUB_PLATFORM_ENABLED": "true", "UNIHUB_ORIGIN": origin, "UNIHUB_MODEL_SERVICE_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_ROUTING_PRINCIPAL_ID": "14", "UNIHUB_ROUTING_GROUP": "unihub-platform", "UNIHUB_MAX_INPUT_TOKENS": "65536", "UNIHUB_MAX_OUTPUT_TOKENS": "4096", "UNIHUB_MAX_BODY_BYTES": "32768",
		"UNIHUB_ALI_EMBEDDING_ENABLED": "true", "UNIHUB_ALI_RERANK_ENABLED": "true", "UNIHUB_KNOWLEDGE_RESULT_READ_ENABLED": "true", "UNIHUB_KNOWLEDGE_RESULT_KEYS": `{"knowledge-v1":"` + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))) + `"}`, "UNIHUB_KNOWLEDGE_RESULT_ACTIVE_KEY": "knowledge-v1", "UNIHUB_KNOWLEDGE_POLICIES": string(policies),
	}
}

func TestKnowledgeRuntimeCatalogUsesCertifiedPolicyWithoutGenericLimits(t *testing.T) {
	env := knowledgeRuntimeEnvironment(t, "https://unihub.quukk.com:18488")
	cfg, err := ConfigFromEnv(func(name string) string { return env[name] })
	require.NoError(t, err)
	require.Empty(t, cfg.OperationLimits)
	sources := []CatalogSourceModel{
		{PublicID: "unihub-text-embedding-v4", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"embeddings.create"}, EmbeddingSpace: cfg.KnowledgePolicies["embeddings.create"]["unihub-text-embedding-v4"].Space}},
		{PublicID: "qwen3-rerank", ModelCapabilities: dto.ModelCapabilities{Operations: []string{"rerank.create"}}},
	}
	for _, scenario := range []string{"certified-only", "explicit-wrong-policy", "invalid-certificate"} {
		t.Run(scenario, func(t *testing.T) {
			current := cfg
			current.KnowledgePolicies = cloneKnowledgePolicies(cfg.KnowledgePolicies)
			if scenario == "explicit-wrong-policy" {
				current.OperationLimits = map[string]map[string]OperationLimits{"embedding": {"unihub-text-embedding-v4": {Policy: "request_count_v1"}}, "rerank": {"qwen3-rerank": {Policy: "request_count_v1"}}}
			}
			if scenario == "invalid-certificate" {
				for _, source := range sources {
					wire := source.Operations[0]
					policy := current.KnowledgePolicies[wire][source.PublicID]
					policy.ChannelKeySHA256 = "invalid"
					current.KnowledgePolicies[wire][source.PublicID] = policy
				}
			}
			a := &Adapter{config: current}
			handler := CatalogHandler(CatalogConfig{Token: strings.Repeat("c", 32), ProductID: "llmrouter", InstanceID: "default"}, func(context.Context) (TechnicalCatalog, error) { return a.ProjectCatalog("llmrouter", sources) })
			request := httptest.NewRequest(http.MethodGet, "/internal/v1/model-catalog?product_id=llmrouter&instance_id=default", nil)
			request.Header.Set("Authorization", "Bearer "+strings.Repeat("c", 32))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			var catalog TechnicalCatalog
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &catalog))
			require.Len(t, catalog.Models, 2)
			for _, model := range catalog.Models {
				require.Len(t, model.Operations, 1)
				op := model.Operations[0]
				if scenario != "certified-only" {
					assert.False(t, op.Available)
					if scenario == "explicit-wrong-policy" {
						assert.Equal(t, "managed_producer_policy_mismatch", op.DisabledReason)
					}
					continue
				}
				assert.True(t, op.Available)
				assert.False(t, op.Executable)
				assert.Equal(t, "account_authorization_required", op.DisabledReason)
				assert.Equal(t, "provider_reported_v1", op.Policy)
				assert.Equal(t, op.Policy, op.Limits.Policy)
				assert.Equal(t, 4096, op.Limits.MaxQuantity)
				assert.EqualValues(t, 32768, op.Limits.MaxBodyBytes)
				assert.Equal(t, current.KnowledgePolicies[op.ID][model.ID].RateIDs, op.RateIDs)
			}
		})
	}
}

func TestKnowledgeRuntimeIndexAuthorityUsesPublicIssuerWithPrivateTransport(t *testing.T) {
	for _, scenario := range []string{"public-issuer", "legacy-single-origin", "private-port-issuer", "foreign-issuer"} {
		t.Run(scenario, func(t *testing.T) {
			public := "https://unihub.quukk.com"
			issuer := public
			requests := []string{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				assert.Equal(t, "Bearer original-session", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				var payload any
				switch r.URL.Path {
				case "/v1/accounts":
					payload = []map[string]string{{"id": "account"}}
				case "/v1/me/product-access":
					payload = map[string]any{"issuer": issuer, "subject": "actor", "user": map[string]string{"id": "actor"}, "product_id": "llmrouter", "instance_id": "default", "active": true, "permissions": []string{"product:use"}, "version": 1, "expires_at": time.Now().Unix() + 600}
				default:
					t.Fatalf("unexpected endpoint %s", r.URL.Path)
				}
				raw, err := common.Marshal(payload)
				require.NoError(t, err)
				_, err = w.Write(raw)
				assert.NoError(t, err)
			}))
			defer server.Close()
			env := knowledgeRuntimeEnvironment(t, server.URL)
			env["UNIHUB_PRODUCT_PUBLIC_ORIGIN"] = public
			switch scenario {
			case "legacy-single-origin":
				delete(env, "UNIHUB_PRODUCT_PUBLIC_ORIGIN")
				issuer = server.URL
			case "private-port-issuer":
				issuer = public + ":18488"
			case "foreign-issuer":
				issuer = "https://foreign.invalid"
			}
			cfg, err := ConfigFromEnv(func(name string) string { return env[name] })
			require.NoError(t, err)
			client, err := newClient(cfg)
			require.NoError(t, err)
			client.http = server.Client()
			a := &Adapter{config: cfg, client: client}
			authority, err := a.ReadKnowledgeIndexAuthority(context.Background(), "original-session", "account")
			assert.Equal(t, server.URL, cfg.Origin)
			assert.Equal(t, server.URL, client.origin)
			if scenario == "public-issuer" || scenario == "legacy-single-origin" {
				require.NoError(t, err)
				assert.Equal(t, "actor", authority.ActorID)
				assert.Equal(t, "account", authority.AccountID)
			} else {
				require.Error(t, err)
				var remote *remoteError
				require.ErrorAs(t, err, &remote)
				assert.Equal(t, 403, remote.status)
			}
			assert.Equal(t, []string{"GET /v1/accounts", "GET /v1/me/product-access"}, requests)
		})
	}
}

func TestKnowledgeRuntimeRejectsUnsafeConfiguredPublicIssuer(t *testing.T) {
	for _, issuer := range []string{"http://unihub.quukk.com", "https://user:secret@unihub.quukk.com", "https://unihub.quukk.com/path", "https://unihub.quukk.com?", "https://unihub.quukk.com?issuer=foreign", "https://unihub.quukk.com#foreign"} {
		t.Run(issuer, func(t *testing.T) {
			env := knowledgeRuntimeEnvironment(t, "https://unihub.quukk.com:18488")
			env["UNIHUB_PRODUCT_PUBLIC_ORIGIN"] = issuer
			_, err := ConfigFromEnv(func(name string) string { return env[name] })
			require.Error(t, err)
		})
	}
}
