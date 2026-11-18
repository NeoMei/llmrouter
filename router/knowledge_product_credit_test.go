package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *pairedFixture) productGrant() platformbilling.Claims {
	conversion := platformbilling.NativeConversion{Version: "product-fixture-1", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
	return platformbilling.Claims{CreditScope: platformbilling.ProductCreditScope, SourceVerified: true, FundingProductID: f.product,
		ID: "parent-key", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter", ActorID: "actor", AccountID: "account",
		OriginProductID: f.product, ModelScope: "llmrouter_available", PricingSource: platformbilling.NativeCreditPricingSource,
		CredentialVersion: 2, Asset: "CREDIT/6", FundingMode: "credits", NativeCreditConversion: &conversion,
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Minute)}
}

func newProductKnowledgeFixture(t *testing.T, embedding bool) *pairedFixture {
	t.Helper()
	f := newManagedKnowledgeFixture(t, embedding)
	f.productCredit = true
	f.wantProductReserve, f.wantProductCharge = "4300800", "3150"
	f.config.NativeGroup = "default"
	adapter, err := platformbilling.New(f.db, f.config)
	require.NoError(t, err)
	f.adapter = adapter
	f.engine = gin.New()
	SetManagedKeyDispatch(f.engine, f.adapter)
	ctx, err := platformbilling.WithProductAuthorization(t.Context(), "product-proof", "managed-knowledge")
	require.NoError(t, err)
	f.callID, err = f.productGrant().ProductCreditCallID(ctx)
	require.NoError(t, err)
	f.taskID = "task_" + f.callID
	operation := "rerank"
	if embedding {
		operation = "embedding"
	}
	oldTariff, oldFX := billing_setting.ProductCreditPricingJSONString(), operation_setting.USDExchangeRate
	t.Cleanup(func() {
		require.NoError(t, billing_setting.SetProductCreditPricing(oldTariff))
		operation_setting.USDExchangeRate = oldFX
	})
	operation_setting.USDExchangeRate = 7
	require.NoError(t, billing_setting.SetProductCreditPricing(fmt.Sprintf(`{
		"version":"knowledge-fixture","suppliers":{%q:{%q:{
		"vendor-usd":{"version":"u1","currency":"USD","tariff":{"version":"metered_v2","operation":%q,"policy":"provider_reported_v1","terms":[{"key":"input","unit":"token","price":{"numerator":"1","denominator":"1000000"}}]}},
		"vendor-cny":{"version":"c1","currency":"CNY","tariff":{"version":"metered_v2","operation":%q,"policy":"provider_reported_v1","terms":[{"key":"input","unit":"token","price":{"numerator":"1","denominator":"200000"}}]}}
		}}}}`, f.name, operation, operation, operation)))
	return f
}

func TestProductKnowledgeSettlesPinnedResponseAtFrozenCreditTariff(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		t.Run(fmt.Sprintf("embedding=%t", embedding), func(t *testing.T) {
			f := newProductKnowledgeFixture(t, embedding)
			withProof := func(r *http.Request) { r.Header.Set(platformbilling.ProductAuthorizationHeader, "product-proof") }
			f.beforeReserve = func() {
				require.NoError(t, billing_setting.SetProductCreditPricing(`{"version":"replaced-after-hold"}`))
			}
			result := managedKnowledgeRequest(f, f.body, withProof)
			require.Equal(t, http.StatusOK, result.Code, result.Body.String())
			assert.Equal(t, f.callID, result.Header().Get("X-UniHub-Call-ID"))
			assert.Equal(t, 1, f.reserves)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", f.callID).Error)
			assert.Equal(t, platformbilling.ProductCreditScope, row.CreditScope)
			assert.Equal(t, f.product, row.FundingProductID)
			assert.Equal(t, "SETTLED", row.Status)
			assert.Equal(t, "3150", row.Charged)
			assert.NotContains(t, row.MediaSnapshot, f.managed.credential)

			retry := managedKnowledgeRequest(f, f.body, withProof)
			require.Equal(t, http.StatusConflict, retry.Code, retry.Body.String())
			assert.Equal(t, 1, f.sends)

			read := func(proof string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, "/platform/v1/calls/managed-knowledge/result", nil)
				r.Header.Set("Authorization", "Bearer "+f.managed.credential)
				r.Header.Set(platformbilling.ProductAuthorizationHeader, proof)
				w := httptest.NewRecorder()
				f.engine.ServeHTTP(w, r)
				return w
			}
			recovered := read("product-proof")
			require.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
			assert.JSONEq(t, result.Body.String(), recovered.Body.String())
			assert.Equal(t, http.StatusNotFound, read("different-proof").Code)
			f.revoked = true
			assert.Equal(t, http.StatusNotFound, read("product-proof").Code)
			assert.Equal(t, 1, f.sends)
		})
	}
}

func TestProductKnowledgeBrokerPreservesAuthorizedKeyAndWorkReceipt(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		t.Run(fmt.Sprintf("embedding=%t", embedding), func(t *testing.T) {
			f := newProductKnowledgeFixture(t, embedding)
			call := "wk_" + strings.Repeat("a", 60)
			ctx, err := platformbilling.WithProductAuthorization(t.Context(), "product-proof", call)
			require.NoError(t, err)
			f.callID, err = f.productGrant().ProductCreditCallID(ctx)
			require.NoError(t, err)
			f.taskID = "original-work-task"
			send := func(path, body, actor, task, key, pin string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+f.managed.credential)
				r.Header.Set(platformbilling.ProductAuthorizationHeader, "product-proof")
				r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
				r.Header.Set("X-UniHub-Task-ID", task)
				if body != "" {
					r.Header.Set("Content-Type", "application/json")
				}
				if key != "" {
					r.Header.Set("Idempotency-Key", key)
				}
				if pin != "" {
					r.Header.Set("X-UniHub-Knowledge-Policy", pin)
				}
				w := httptest.NewRecorder()
				f.engine.ServeHTTP(w, r)
				return w
			}
			preflight := send("/platform/v1/knowledge/route", fmt.Sprintf(`{"model":%q,"wire_operation":%q}`, f.name, f.wire), "original-actor", f.taskID, "", "")
			require.Equal(t, http.StatusOK, preflight.Code, preflight.Body.String())
			assert.Equal(t, "no-store", preflight.Header().Get("Cache-Control"))
			var route platformbilling.KnowledgePaidRoute
			require.NoError(t, common.UnmarshalUnique(preflight.Body.Bytes(), &route))
			assert.Equal(t, "parent-key", route.AuthorizedCredentialID)
			assert.Equal(t, f.taskID, route.TaskID)
			assert.Equal(t, f.binding.PolicyDigest, route.PolicyDigest)

			path, body := "/platform/v1/embeddings", f.body
			if !embedding {
				path = "/platform/api/v1/services/rerank/text-rerank/text-rerank"
				body = `{"model":"qwen3-rerank","input":{"query":"test","documents":["test"]},"parameters":{"top_n":1,"return_documents":false}}`
			}
			wrong := send(path, body, "original-actor", f.taskID, call, strings.Repeat("c", 64))
			assert.NotEqual(t, http.StatusOK, wrong.Code)
			assert.Zero(t, f.reserves)
			sent := send(path, body, "original-actor", f.taskID, call, route.PolicyDigest)
			require.Equal(t, http.StatusOK, sent.Code, sent.Body.String())
			assert.Equal(t, "no-store", sent.Header().Get("Cache-Control"))
			assert.Equal(t, 1, f.sends)

			unguarded := httptest.NewRequest(http.MethodGet, "/platform/v1/calls/"+call+"/result", nil)
			unguarded.Header.Set("Authorization", "Bearer "+f.managed.credential)
			unguarded.Header.Set(platformbilling.ProductAuthorizationHeader, "product-proof")
			publicRead := httptest.NewRecorder()
			f.engine.ServeHTTP(publicRead, unguarded)
			assert.Equal(t, http.StatusNotFound, publicRead.Code, "broker work needs the current actor on every result read")

			resultPath := "/platform/v1/knowledge/results/" + call
			require.Equal(t, http.StatusNotFound, send(resultPath, "", "original-actor", "different-task", "", "").Code)
			result := send(resultPath, "", "original-actor", f.taskID, "", "")
			require.Equal(t, http.StatusOK, result.Code, result.Body.String())
			assert.JSONEq(t, sent.Body.String(), result.Body.String())
			raw, err := base64.RawURLEncoding.DecodeString(result.Header().Get("X-UniHub-Knowledge-Receipt"))
			require.NoError(t, err)
			var receipt platformbilling.KnowledgeReceipt
			require.NoError(t, common.UnmarshalUnique(raw, &receipt))
			assert.Equal(t, route.AuthorizedCredentialID, receipt.AuthorizedCredentialID)
			assert.Equal(t, route.AuthorizedCredentialID, receipt.ProducerCredentialID)
			assert.Equal(t, call, receipt.CallID)
			assert.Equal(t, f.taskID, receipt.TaskID)
			assert.Equal(t, route.PolicyDigest, receipt.PolicyDigest)
			assert.Equal(t, route.RouteGeneration, receipt.RouteGeneration)
			assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(result.Body.Bytes())), receipt.ContentSHA256)
			assert.Positive(t, receipt.AttemptID)
		})
	}
}

func TestProductKnowledgeCatalogFollowsOriginPinInCertifiedGroup(t *testing.T) {
	f := newProductKnowledgeFixture(t, true)
	projected, err := readProviderCatalog(t.Context(), f.adapter, f.productGrant())
	require.NoError(t, err)
	require.Len(t, projected.Models, 1)
	model := projected.Models[0]
	assert.Equal(t, f.name, model.ID)
	require.Len(t, model.CapabilityMetadata.Operations, 1)
	op := model.CapabilityMetadata.Operations[0]
	assert.True(t, op.Executable)
	assert.Equal(t, "provider_reported_v1", op.Policy)
	require.NotNil(t, op.PublicTariff)
	badGrant := f.productGrant()
	badGrant.OriginProductID, badGrant.FundingProductID = "other-product", "other-product"
	other, err := readProviderCatalog(t.Context(), f.adapter, badGrant)
	require.NoError(t, err)
	assert.Empty(t, other.Models)
}

func TestProductKnowledgeMultimodalCapturesSeparatedUsage(t *testing.T) {
	f := newProductKnowledgeFixture(t, true)
	f.name, f.upstream, f.wire = "mixed-embeddings", "qwen3-vl-embedding", "multimodal.embeddings.create"
	f.wantProductReserve, f.wantProductCharge = "750000", "1200"
	sends := 0
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		assert.Equal(t, "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", r.URL.Path)
		assert.Equal(t, "Bearer key", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get(platformbilling.ProductAuthorizationHeader))
		var request dto.QwenMultimodalEmbeddingRequest
		require.NoError(t, common.DecodeJson(r.Body, &request))
		assert.Equal(t, f.upstream, request.Model)
		require.Len(t, request.Input.Contents, 2)
		assert.Equal(t, "fixture-input-private", request.Input.Contents[0].Text)
		assert.True(t, strings.HasPrefix(request.Input.Contents[1].Image, "data:image/png;base64,"))
		vector := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 256), ",") + "]"
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":{"embeddings":[{"index":0,"embedding":%s,"type":"vl"},{"index":1,"embedding":%s,"type":"vl"}]},"usage":{"input_tokens":2,"image_tokens":3,"total_tokens":5}}`, vector, vector)
	}))
	t.Cleanup(vendor.Close)
	require.NoError(t, f.db.Create(&model.Channel{Id: 104, Type: constant.ChannelTypeAli, Key: "key", Status: common.ChannelStatusEnabled,
		BaseURL: &vendor.URL, Models: f.upstream, Group: "platform"}).Error)
	require.NoError(t, f.db.Create(&model.Ability{Group: "platform", Model: f.upstream, ChannelId: 104, Enabled: true}).Error)
	space := dto.EmbeddingSpace{Provider: "aliyun", Model: f.upstream, Version: "v1", Dimensions: 256, InputSemantics: dto.QwenMultimodalInputSemantics}
	metadata := `{"operations":["multimodal.embeddings.create"],"input_modalities":["text","image"],"output_modalities":["embedding"],"embedding_space":{"provider":"aliyun","model":"qwen3-vl-embedding","version":"v1","dimensions":256,"input_semantics":"` + space.InputSemantics + `"},"media_limits":{"max_images":5,"max_pixels":16000000,"max_request_bytes":65536,"formats":["png"]}}`
	require.NoError(t, f.db.Create(&model.ClientModelRoute{Id: 3, PublicId: f.name, DisplayName: "Mixed", Client: "all", Enabled: true, Metadata: metadata}).Error)
	require.NoError(t, f.db.Create(&model.ClientModelTarget{Id: 3, RouteId: 3, ChannelId: 104, UpstreamModel: f.upstream,
		BillingModel: f.upstream, PreferredWires: `["qwen_multimodal_embedding"]`, Enabled: true, Weight: 1}).Error)
	f.config.NativeGroup = "platform"
	f.config.KnowledgePolicies[f.wire] = map[string]platformbilling.KnowledgePolicy{f.name: {
		Version: "knowledge_v1", ProviderReference: "https://fixture.test/policy", PublicModel: f.name, WireOperation: f.wire,
		ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", ChannelID: 104, ChannelType: constant.ChannelTypeAli,
		UpstreamModel: f.upstream, HardInputTokens: 1000, VisualTokens: 2000, MaxBatch: 5, Space: &space, RateIDs: map[string]string{"CREDIT/6": "fixture"},
	}}
	model.InitChannelCache()
	require.NoError(t, billing_setting.SetProductCreditPricing(`{"version":"multimodal-fixture","suppliers":{"mixed-embeddings":{"multimodal_embedding":{"vendor":{"version":"v1","currency":"CNY","tariff":{"version":"metered_v2","operation":"multimodal_embedding","policy":"provider_reported_v1","terms":[{"key":"text","unit":"token","price":{"numerator":"1","denominator":"1000000"}},{"key":"visual","unit":"token","price":{"numerator":"1","denominator":"500000"}}]}}}}}}`))
	adapter, err := platformbilling.New(f.db, f.config)
	require.NoError(t, err)
	f.adapter = adapter
	f.engine = gin.New()
	SetManagedKeyDispatch(f.engine, f.adapter)
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	body := fmt.Sprintf(`{"model":%q,"input":[{"text":"fixture-input-private"},{"image":"data:image/png;base64,%s"}]}`, f.name, base64.StdEncoding.EncodeToString(imageBytes.Bytes()))
	f.path = "/platform/v1/multimodal/embeddings"
	sent := managedKnowledgeRequest(f, body, func(r *http.Request) { r.Header.Set(platformbilling.ProductAuthorizationHeader, "product-proof") })
	require.Equal(t, http.StatusOK, sent.Code, sent.Body.String())
	assert.Equal(t, 1, sends)
	assert.Equal(t, 1, f.reserves)
	assert.Equal(t, 1, f.settles)
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", f.callID).Error)
	assert.Equal(t, "1200", row.Charged)
	assert.Contains(t, row.Evidence, `"ali_multimodal_actual_v1"`)
	assert.NotContains(t, row.MediaSnapshot, "fixture-input-private")
	assert.NotContains(t, row.MediaSnapshot, "data:image/")
	read := httptest.NewRequest(http.MethodGet, "/platform/v1/calls/managed-knowledge/result", nil)
	read.Header.Set("Authorization", "Bearer "+f.managed.credential)
	read.Header.Set(platformbilling.ProductAuthorizationHeader, "product-proof")
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, read)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, sent.Body.String(), w.Body.String())
	assert.Equal(t, 1, sends)
}

func TestProductKnowledgeAliasCannotBypassCertifiedPin(t *testing.T) {
	f := newProductKnowledgeFixture(t, true)
	require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", f.binding.RouteID).Update("aliases", `["generic-alias"]`).Error)
	f.config.NativeGroup = "platform"
	adapter, err := platformbilling.New(f.db, f.config)
	require.NoError(t, err)
	f.engine = gin.New()
	SetManagedKeyDispatch(f.engine, adapter)
	body := strings.Replace(f.body, f.name, "generic-alias", 1)
	response := managedKnowledgeRequest(f, body, func(r *http.Request) { r.Header.Set(platformbilling.ProductAuthorizationHeader, "product-proof") })
	assert.GreaterOrEqual(t, response.Code, http.StatusBadRequest, response.Body.String())
	assert.Zero(t, f.reserves)
	assert.Zero(t, f.sends)
}
