package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pairedFixture struct {
	product                                                                                                         string
	embeddingUsage                                                                                                  string
	accessIssuer, accessInstance, accessSubject                                                                     string
	t                                                                                                               *testing.T
	db                                                                                                              *gorm.DB
	adapter                                                                                                         *platformbilling.Adapter
	config                                                                                                          platformbilling.Config
	engine                                                                                                          *gin.Engine
	name, wire, body, path, upstream, origin                                                                        string
	channel                                                                                                         int
	binding                                                                                                         platformbilling.ManagedBinding
	sends, reserves, settles, verifies, reads                                                                       int
	revoked, notMember, noPermission, wrongProduct, expired, redirect, malformed, badContent, oversized, refusePaid bool
	lostReserveReply                                                                                                bool
	supplierTimeout                                                                                                 bool
	beforeReserve, beforeClaim, afterClaim                                                                          func()
	claimChecks                                                                                                     int
	remote                                                                                                          platformbilling.RemoteCall
	usage                                                                                                           platformbilling.Usage
	finalDigest                                                                                                     string
}

func newPairedFixture(t *testing.T, embedding bool) *pairedFixture {
	t.Helper()
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	f := &pairedFixture{t: t, product: "superwagie", name: "qwen3-rerank", wire: "rerank.create", upstream: "qwen3-rerank", channel: 103, path: "/platform/v1/rerank", body: `{"model":"qwen3-rerank","query":"q","documents":["a"],"top_n":1,"return_documents":false}`}
	if embedding {
		f.name = "unihub-text-embedding-v4"
		f.wire = "embeddings.create"
		f.upstream = "text-embedding-v4"
		f.channel = 102
		f.path = "/platform/v1/embeddings"
		f.body = `{"model":"unihub-text-embedding-v4","input":["a"],"dimensions":1024}`
	}
	var err error
	f.db, err = gorm.Open(sqlite.Open("file:"+t.TempDir()+"/paired.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := f.db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	require.NoError(t, f.db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.KnowledgeActiveBinding{}, &model.KnowledgeRoutePin{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.RelayMediaResource{}))
	oldDB, oldLog, oldCache, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
	model.DB = f.db
	model.LOG_DB = f.db
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	t.Cleanup(func() {
		perfmetrics.DrainRelaySamples()
		model.DB = oldDB
		model.LOG_DB = oldLog
		common.MemoryCacheEnabled = oldCache
		common.RedisEnabled = oldRedis
	})
	bill := "rerank"
	if embedding {
		bill = "embedding"
	}
	price := metering.Price{Version: "metered_v2", Operation: bill, Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}}}}
	rate := platformbilling.Rate{ID: "rate", Model: f.name, Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			f.reads++
			assert.Equal(t, "Bearer original-actor", r.Header.Get("Authorization"))
			assert.Empty(t, r.URL.RawQuery)
			assert.Empty(t, r.Header.Get("Cookie"))
			assert.Empty(t, r.Header.Get("X-UniHub-Actor-Authorization"))
			if f.redirect {
				http.Redirect(w, r, "http://127.0.0.1:1/forbidden", 302)
				return
			}
			if f.badContent {
				w.Header().Set("Content-Type", "text/plain")
			}
			if f.malformed {
				io.WriteString(w, `{"active":true,"active":false}`)
				return
			}
			if f.oversized {
				io.WriteString(w, strings.Repeat(" ", (1<<20)+1))
				return
			}
			if f.revoked {
				w.WriteHeader(401)
				return
			}
			if r.URL.Path == "/v1/accounts" {
				if f.notMember {
					out = []any{}
				} else {
					out = []any{map[string]any{"id": "account", "kind": "user", "name": "private", "role": "member"}}
				}
			} else {
				product := f.product
				if f.wrongProduct {
					product = "unknown"
				}
				permissions := []string{"product:use"}
				if f.noPermission {
					permissions = nil
				}
				expires := time.Now().Add(time.Hour).Unix()
				if f.expired {
					expires = time.Now().Unix() - 1
				}
				issuer, instance, subject := f.origin, "default", "actor"
				if f.accessIssuer != "" {
					issuer = f.accessIssuer
				}
				if f.accessInstance != "" {
					instance = f.accessInstance
				}
				if f.accessSubject != "" {
					subject = f.accessSubject
				}
				out = map[string]any{"issuer": issuer, "subject": subject, "user": map[string]any{"id": "actor", "name": "private"}, "product_id": product, "instance_id": instance, "permissions": permissions, "version": 1, "active": true, "expires_at": expires}
			}
		} else {
			assert.Equal(t, "Bearer service-service-service-service-token", r.Header.Get("Authorization"))
			switch r.URL.Path {
			case "/internal/v1/model-credentials/verify":
				f.verifies++
				var in map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &in))
				assert.Equal(t, "Bearer original-actor", r.Header.Get("X-UniHub-Actor-Authorization"))
				if f.revoked || (f.refusePaid && in["purpose"] == "knowledge_dispatch") {
					w.WriteHeader(403)
					return
				}
				assert.Contains(t, []string{"knowledge_dispatch", "result_read"}, in["purpose"])
				if in["purpose"] == "knowledge_dispatch" {
					f.claimChecks++
					if f.claimChecks == 2 && f.beforeClaim != nil {
						f.beforeClaim()
					}
				}
				id := "producer"
				if in["token"] == "uh_model_reader" {
					id = "reader"
				}
				out = struct {
					platformbilling.Claims
					ActorExpiresAt int64 `json:"actor_expires_at"`
				}{platformbilling.Claims{CredentialKind: "task", InstanceID: "default", ID: id, Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: f.product, TaskID: "task", Models: []string{f.name}, Operations: map[string][]string{f.name: {f.wire}}, ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second), Asset: "CREDIT/6", FundingMode: "credits"}, time.Now().Add(time.Hour).Unix()}
			case "/internal/v1/rates":
				out = []platformbilling.Rate{rate}
			case "/internal/v1/calls/reserve":
				f.reserves++
				if f.beforeReserve != nil {
					f.beforeReserve()
				}
				var in map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &in))
				f.remote = platformbilling.RemoteCall{InstanceID: "default", WireOperation: f.wire, CallID: "call", ActorID: "actor", AccountID: "account", OriginProductID: f.product, CredentialID: "producer", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
				if f.lostReserveReply {
					w.WriteHeader(503)
					return
				}
				out = f.remote
			case "/internal/v1/calls/call/settle":
				f.settles++
				var in struct {
					Usage platformbilling.Usage `json:"usage"`
				}
				require.NoError(t, common.DecodeJson(r.Body, &in))
				f.usage = in.Usage
				f.remote.Status = "SETTLED"
				f.remote.Charged = "2"
				raw, e := common.Marshal(struct {
					CallID       string                 `json:"call_id"`
					Usage        *platformbilling.Usage `json:"usage"`
					ProviderCost string                 `json:"provider_cost"`
				}{"call", &f.usage, ""})
				require.NoError(t, e)
				sum := sha256.Sum256(raw)
				f.finalDigest = hex.EncodeToString(sum[:])
				out = f.remote
			case "/internal/v1/calls/call":
				out = struct {
					platformbilling.RemoteCall
					ServiceID    string                 `json:"service_id"`
					TaskID       string                 `json:"task_id"`
					Usage        *platformbilling.Usage `json:"usage"`
					FinalDigest  string                 `json:"final_digest"`
					ProviderCost string                 `json:"provider_cost"`
				}{f.remote, "llmrouter", "task", &f.usage, f.finalDigest, ""}
			case "/internal/v1/calls/call/release":
				f.remote.Status = "RELEASED"
				out = f.remote
			default:
				t.Errorf("unexpected center path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
		}
		raw, e := common.Marshal(out)
		require.NoError(t, e)
		w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	f.origin = hub.URL
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.sends++
		var row platformbilling.Call
		require.NoError(t, f.db.First(&row, "call_id = ?", "call").Error)
		assert.Equal(t, "UNKNOWN", row.Status)
		assert.NotContains(t, row.MediaSnapshot, "original-actor")
		var n int64
		require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Where("call_id = ?", "call").Count(&n).Error)
		assert.EqualValues(t, 1, n)
		if f.afterClaim != nil {
			f.afterClaim()
		}
		assert.Empty(t, r.Header.Get("X-UniHub-Actor-Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if embedding {
			vector := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 1024), ",") + "]"
			usage := f.embeddingUsage
			if usage == "" {
				usage = `{"total_tokens":3}`
			}
			fmt.Fprintf(w, `{"object":"list","model":"text-embedding-v4","data":[{"index":0,"embedding":%s}],"usage":%s}`, vector, usage)
		} else {
			io.WriteString(w, `{"output":{"results":[{"index":0,"relevance_score":0.9}]},"usage":{"total_tokens":3}}`)
		}
	}))
	t.Cleanup(up.Close)
	require.NoError(t, f.db.Create(&model.User{Id: 1, Username: "platform", Status: common.UserStatusEnabled, Quota: 12345, Group: "platform"}).Error)
	base := "https://dashscope.aliyuncs.com"
	require.NoError(t, f.db.Create(&model.Channel{Id: f.channel, Type: 17, Key: "key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: f.upstream, Group: "platform"}).Error)
	require.NoError(t, f.db.Create(&model.Ability{Group: "platform", Model: f.upstream, ChannelId: f.channel, Enabled: true}).Error)
	var space *dto.EmbeddingSpace
	if embedding {
		space = &dto.EmbeddingSpace{Provider: "aliyun", Model: f.upstream, Version: "v4", Dimensions: 1024, InputSemantics: "text"}
	}
	meta, e := common.Marshal(dto.ModelCapabilities{Operations: []string{f.wire}, InputModalities: []string{"text"}, EmbeddingSpace: space})
	require.NoError(t, e)
	require.NoError(t, f.db.Create(&model.ClientModelRoute{Id: 2, PublicId: f.name, DisplayName: "Knowledge", Client: "all", Enabled: true, Metadata: string(meta)}).Error)
	require.NoError(t, f.db.Create(&model.ClientModelTarget{Id: 2, RouteId: 2, ChannelId: f.channel, UpstreamModel: f.upstream, BillingModel: f.upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	p := platformbilling.KnowledgePolicy{ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Version: "knowledge_v1", ProviderReference: "https://fixture.test/policy", PublicModel: f.name, WireOperation: f.wire, ChannelID: f.channel, ChannelType: 17, UpstreamModel: f.upstream, HardInputTokens: 4096, MaxItemTokens: 2048, MaxBatch: 1, RateIDs: map[string]string{"CREDIT/6": "rate"}, Space: space}
	if embedding {
		p.Version = "knowledge_ali_text_v1"
		p.Origin = base
		p.MaxItemTokens = 4096
	}
	f.config = platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "default", PrincipalID: 1, Group: "platform", MaxInputTokens: 4096, MaxOutputTokens: 100, MaxBodyBytes: 65536, AllowAliEmbeddingV4: embedding, AllowAliRerank: !embedding, EnableKnowledgeResultRead: true, AliEmbeddingResultKeys: map[string][]byte{"v1": bytes.Repeat([]byte{11}, 32)}, AliEmbeddingResultActiveKey: "v1", KnowledgePolicies: map[string]map[string]platformbilling.KnowledgePolicy{f.wire: {f.name: p}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{bill: {f.name: {Policy: "provider_reported_v1"}}}, OperationRateIDs: map[string]map[string]map[string]string{bill: {"CREDIT/6": {f.name: "rate"}}}}
	f.adapter, err = platformbilling.New(f.db, f.config)
	require.NoError(t, err)
	f.engine = gin.New()
	f.engine.Use(middlewareBodyCleanupForTest())
	SetPlatformRouter(f.engine, f.adapter)
	bindings, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"llmrouter", "superwagie"})
	require.NoError(t, err)
	f.binding = bindings[1]
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	client := service.GetHttpClient()
	oldTransport := client.Transport
	client.Transport = pairedTransport{fixture: f, next: aliEmbeddingFixtureTransport{target: up.URL, next: oldTransport}}
	t.Cleanup(func() { client.Transport = oldTransport })
	return f
}
func (f *pairedFixture) request(path, body string, read bool, edit func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if read {
		r.Header.Set("Authorization", "Bearer original-actor")
	} else {
		r.Header.Set("Authorization", "Bearer uh_model_fixture")
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer original-actor")
		r.Header.Set("X-UniHub-Task-ID", "task")
		if path == f.path || strings.Contains(path, "text-rerank") {
			r.Header.Set("Idempotency-Key", "call")
			r.Header.Set("X-UniHub-Knowledge-Policy", f.binding.PolicyDigest)
		}
	}
	if edit != nil {
		edit(r)
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}
func (f *pairedFixture) readBody() string {
	return fmt.Sprintf(`{"purpose":"index_read","account_id":"account","model":%q,"wire_operation":%q,"policy_digest":%q}`, f.name, f.wire, f.binding.PolicyDigest)
}
func TestPairedKnowledgeActualRouterAndReceipt(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		t.Run(fmt.Sprint(embedding), func(t *testing.T) {
			f := newPairedFixture(t, embedding)
			paid := f.request("/platform/v1/knowledge/route", fmt.Sprintf(`{"model":%q,"wire_operation":%q}`, f.name, f.wire), false, nil)
			require.Equal(t, 200, paid.Code, paid.Body.String())
			f.claimChecks = 0
			w := f.request(f.path, f.body, false, nil)
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.Equal(t, 1, f.reserves)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
			var original model.ClientModelRoute
			require.NoError(t, f.db.First(&original, 2).Error)
			_, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"llmrouter", "superwagie"})
			require.NoError(t, err)
			result := f.request("/platform/v1/knowledge/results/call", "", false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer uh_model_reader") })
			require.Equal(t, 200, result.Code, result.Body.String())
			header, err := base64.RawURLEncoding.DecodeString(result.Header().Get("X-UniHub-Knowledge-Receipt"))
			require.NoError(t, err)
			var receipt platformbilling.KnowledgeReceipt
			require.NoError(t, common.UnmarshalUnique(header, &receipt))
			sum := sha256.Sum256(result.Body.Bytes())
			assert.Equal(t, hex.EncodeToString(sum[:]), receipt.ContentSHA256)
			assert.Equal(t, f.binding.PolicyDigest, receipt.PolicyDigest)
			assert.Equal(t, "producer", receipt.ProducerCredentialID)
			assert.Equal(t, "reader", receipt.AuthorizedCredentialID)
			assert.Equal(t, original.KnowledgeGeneration, receipt.RouteGeneration)
			assert.LessOrEqual(t, receipt.ExpiresAt, time.Now().Unix()+30)
			changed := sha256.Sum256(append(result.Body.Bytes(), ' '))
			assert.NotEqual(t, hex.EncodeToString(changed[:]), receipt.ContentSHA256)
			replay := f.request(f.path, f.body, false, nil)
			assert.Equal(t, 409, replay.Code)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.reserves)
		})
	}
}
func TestPairedKnowledgeIndexReadZeroSpend(t *testing.T) {
	f := newPairedFixture(t, true)
	f.refusePaid = true
	for _, tc := range []struct {
		name      string
		edit      func(*http.Request)
		configure func()
		status    int
	}{
		{"valid", nil, nil, 200}, {"task-token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer uh_model_fixture") }, nil, 403}, {"api-token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer uh_key_fixture") }, nil, 403}, {"wrapped-product", func(r *http.Request) { r.Header.Set("Authorization", "Bearer upa_fixture") }, nil, 403}, {"wrapped-session", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ubp_fixture") }, nil, 403}, {"duplicate-auth", func(r *http.Request) { r.Header.Add("Authorization", "Bearer original-actor") }, nil, 400}, {"mixed-task", func(r *http.Request) { r.Header.Set("X-UniHub-Task-ID", "task") }, nil, 400}, {"cookie", func(r *http.Request) { r.Header.Set("Cookie", "session=x") }, nil, 400},
		{"membership", nil, func() { f.notMember = true }, 403}, {"permission", nil, func() { f.noPermission = true }, 403}, {"wrong-product", nil, func() { f.wrongProduct = true }, 403}, {"revoked", nil, func() { f.revoked = true }, 403}, {"expired", nil, func() { f.expired = true }, 403}, {"redirect", nil, func() { f.redirect = true }, 503}, {"malformed", nil, func() { f.malformed = true }, 503}, {"content-type", nil, func() { f.badContent = true }, 503}, {"oversized", nil, func() { f.oversized = true }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.notMember = false
			f.noPermission = false
			f.wrongProduct = false
			f.revoked = false
			f.expired = false
			f.redirect = false
			f.malformed = false
			f.badContent = false
			f.oversized = false
			if tc.configure != nil {
				tc.configure()
			}
			w := f.request("/platform/v1/knowledge/route", f.readBody(), true, tc.edit)
			assert.Equal(t, tc.status, w.Code, w.Body.String())
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
			assert.Zero(t, f.settles)
			assert.Zero(t, f.verifies)
		})
	}
}
func TestPairedKnowledgeSendClaimBoundary(t *testing.T) {
	for _, phase := range []string{"before-claim", "after-claim", "revoked-before-claim"} {
		t.Run(phase, func(t *testing.T) {
			f := newPairedFixture(t, true)
			mutate := func() {
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel).Update("key", "rotated").Error)
			}
			if phase == "before-claim" {
				f.beforeClaim = mutate
			}
			if phase == "after-claim" {
				f.afterClaim = mutate
			}
			if phase == "revoked-before-claim" {
				f.beforeReserve = func() { f.revoked = true }
			}
			w := f.request(f.path, f.body, false, nil)
			if phase == "after-claim" {
				assert.Equal(t, 200, w.Code, w.Body.String())
				assert.Equal(t, 1, f.sends)
			} else {
				assert.GreaterOrEqual(t, w.Code, 400)
				assert.Zero(t, f.sends)
			}
			assert.Equal(t, 1, f.reserves)
			var count int64
			require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Count(&count).Error)
			if phase == "after-claim" {
				assert.EqualValues(t, 1, count)
			} else {
				assert.Zero(t, count)
			}
		})
	}
}

func TestPairedKnowledgePinLifecycle(t *testing.T) {
	f := newPairedFixture(t, true)
	original := f.binding
	var count int64
	require.NoError(t, f.db.Model(&model.KnowledgeRoutePin{}).Count(&count).Error)
	assert.EqualValues(t, 2, count)
	cfg := f.config
	p := cfg.KnowledgePolicies[f.wire][f.name]
	p.HardInputTokens = 2048
	cfg.KnowledgePolicies = map[string]map[string]platformbilling.KnowledgePolicy{f.wire: {f.name: p}}
	nodeB, err := platformbilling.New(f.db, cfg)
	require.NoError(t, err)
	InstallKnowledgeBindings(nodeB)
	_, err = nodeB.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, original.PolicyDigest)
	assert.ErrorIs(t, err, platformbilling.ErrConflict)
	require.NoError(t, f.db.Model(&model.KnowledgeRoutePin{}).Count(&count).Error)
	assert.EqualValues(t, 2, count, "read may not mint")
	b, err := nodeB.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"llmrouter", "superwagie"})
	require.NoError(t, err)
	assert.NotEqual(t, original.PolicyDigest, b[1].PolicyDigest)
	assert.NotEqual(t, original.RouteGeneration, b[1].RouteGeneration)
	_, err = f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, original.PolicyDigest)
	assert.ErrorIs(t, err, platformbilling.ErrConflict)
	a, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"llmrouter", "superwagie"})
	require.NoError(t, err)
	assert.NotEqual(t, original.PolicyDigest, a[1].PolicyDigest)
	assert.NotEqual(t, original.RouteGeneration, a[1].RouteGeneration)
	// Ordinary traffic counters do not rotate or retire authorization.
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.channel).Update("used_quota", 7).Error)
	_, err = f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, a[1].PolicyDigest)
	require.NoError(t, err)
	// A target mutation retires the shared pointer; reverting it cannot revive.
	require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = ?", 2).Update("priority", 3).Error)
	_, err = f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, a[1].PolicyDigest)
	assert.ErrorIs(t, err, platformbilling.ErrConflict)
	require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = ?", 2).Update("priority", 0).Error)
	_, err = f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, a[1].PolicyDigest)
	assert.ErrorIs(t, err, platformbilling.ErrConflict)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Where("route_id = ?", 2).Delete(&model.ClientModelTarget{}).Error; e != nil {
			return e
		}
		return tx.Delete(&model.ClientModelRoute{}, 2).Error
	}))
	var active model.KnowledgeActiveBinding
	require.NoError(t, f.db.First(&active, "origin_product_id = ?", "superwagie").Error)
	assert.Empty(t, active.ActivePin)
	var route model.ClientModelRoute
	route.Id = 2
	route.PublicId = f.name
	route.DisplayName = "recreated"
	route.Client = "all"
	route.Enabled = true
	meta, e := common.Marshal(dto.ModelCapabilities{Operations: []string{f.wire}, EmbeddingSpace: original.EmbeddingSpace})
	require.NoError(t, e)
	route.Metadata = string(meta)
	require.NoError(t, f.db.Create(&route).Error)
	assert.NotEqual(t, original.RouteGeneration, route.KnowledgeGeneration)
	assert.Zero(t, f.reserves)
	assert.Zero(t, f.sends)
}
func TestPairedKnowledgeNativeRerankAndStrictHeaders(t *testing.T) {
	f := newPairedFixture(t, false)
	for _, edit := range []func(*http.Request){func(r *http.Request) { r.Header.Del("X-UniHub-Knowledge-Policy") }, func(r *http.Request) { r.Header.Add("X-UniHub-Actor-Authorization", "Bearer original-actor") }, func(r *http.Request) { r.Header.Set("X-UniHub-Knowledge-Policy", strings.Repeat("0", 64)) }} {
		w := f.request(f.path, f.body, false, edit)
		assert.GreaterOrEqual(t, w.Code, 400)
		assert.Zero(t, f.reserves)
		assert.Zero(t, f.sends)
	}
	native := "/platform/api/v1/services/rerank/text-rerank/text-rerank"
	body := `{"model":"qwen3-rerank","input":{"query":"q","documents":["a"]},"parameters":{"top_n":1,"return_documents":false}}`
	w := f.request(native, body, false, nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 1, f.sends)
	assert.Equal(t, 1, f.reserves)
}

func TestPairedKnowledgeCrashWindows(t *testing.T) {
	for _, phase := range []string{"prepare-commit", "reserve-reply", "reserved", "claim-commit", "claimed-no-transport"} {
		t.Run(phase, func(t *testing.T) {
			f := newPairedFixture(t, true)
			// Fault injection uses actual GORM commit callbacks and real HTTP admission;
			// no implementation-only validator bypass or external supplier is used.
			if phase == "prepare-commit" || phase == "claim-commit" {
				target := "RESERVING"
				if phase == "claim-commit" {
					target = "UNKNOWN"
				}
				name := "paired_commit_fault"
				fired := false
				require.NoError(t, f.db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register(name, func(tx *gorm.DB) {
					if fired || tx.Statement.Table != "platform_model_calls" && tx.Statement.Table != "platform_model_attempts" {
						return
					}
					if phase == "prepare-commit" && tx.Statement.Table == "platform_model_calls" {
						fired = true
						tx.AddError(fmt.Errorf("simulated prepare failure at %s", target))
					}
					if phase == "claim-commit" && tx.Statement.Table == "platform_model_attempts" {
						fired = true
						tx.AddError(fmt.Errorf("simulated ambiguous claim at %s", target))
					}
				}))
				t.Cleanup(func() { f.db.Callback().Create().Remove(name) })
				w := f.request(f.path, f.body, false, nil)
				assert.GreaterOrEqual(t, w.Code, 400)
				assert.True(t, fired)
				assert.Zero(t, f.sends)
				if phase == "prepare-commit" {
					assert.Zero(t, f.reserves)
				} else {
					assert.Equal(t, 1, f.reserves)
					var row platformbilling.Call
					require.NoError(t, f.db.First(&row, "call_id = ?", "call").Error)
					assert.Equal(t, "UNKNOWN", row.Status)
				}
			} else {
				session, err := f.adapter.AdmitPinnedKnowledge(context.Background(), "original-actor", "uh_model_fixture", "task", "call", f.binding.PolicyDigest, platformbilling.WireOperation{ID: f.wire}, "application/json", []byte(f.body))
				require.NoError(t, err)
				if phase == "claimed-no-transport" {
					req := httptest.NewRequest("POST", "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings", strings.NewReader(`{"model":"text-embedding-v4","input":["a"],"dimensions":1024}`))
					req.Header.Set("Authorization", "Bearer key")
					req.Host = req.URL.Host
					require.NoError(t, session.ClaimPinnedKnowledgeSend(context.Background(), f.channel, f.upstream, "key", req))
					require.Error(t, session.ClaimPinnedKnowledgeSend(context.Background(), f.channel, f.upstream, "key", req))
				}
				// Simulate persisted RESERVE_UNKNOWN using the same stable call/remote hold.
				if phase == "reserve-reply" {
					require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "call").Update("status", "RESERVE_UNKNOWN").Error)
				}
				restarted, e := platformbilling.New(f.db, f.config)
				require.NoError(t, e)
				InstallKnowledgeBindings(restarted)
				_, err = restarted.AdmitPinnedKnowledge(context.Background(), "original-actor", "uh_model_fixture", "task", "call", f.binding.PolicyDigest, platformbilling.WireOperation{ID: f.wire}, "application/json", []byte(f.body))
				assert.Error(t, err)
				assert.Equal(t, 1, f.reserves)
				assert.Zero(t, f.sends)
				assert.Zero(t, f.settles)
				if phase == "claimed-no-transport" {
					require.NoError(t, restarted.Recover(context.Background()))
					var row platformbilling.Call
					require.NoError(t, f.db.First(&row, "call_id = ?", "call").Error)
					assert.Equal(t, "UNKNOWN", row.Status)
				}
			}
		})
	}
}

type pairedTransport struct {
	fixture *pairedFixture
	next    http.RoundTripper
}

func (p pairedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if p.fixture.supplierTimeout {
		return nil, context.DeadlineExceeded
	}
	return p.next.RoundTrip(r)
}
func TestPairedKnowledgeLostReserveAndSupplierTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			f := newPairedFixture(t, true)
			f.lostReserveReply = !timeout
			f.supplierTimeout = timeout
			w := f.request(f.path, f.body, false, nil)
			assert.GreaterOrEqual(t, w.Code, 400)
			assert.Equal(t, 1, f.reserves)
			assert.Zero(t, f.settles)
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", "call").Error)
			if timeout {
				assert.Equal(t, "UNKNOWN", row.Status)
			} else {
				assert.Equal(t, "RESERVE_UNKNOWN", row.Status)
			}
			f.lostReserveReply = false
			f.supplierTimeout = false
			replay := f.request(f.path, f.body, false, nil)
			assert.Equal(t, 409, replay.Code)
			assert.Equal(t, 1, f.reserves)
			assert.Zero(t, f.sends)
			require.NoError(t, f.adapter.Recover(context.Background()))
			require.NoError(t, f.db.First(&row, "call_id = ?", "call").Error)
			if timeout {
				assert.Equal(t, "UNKNOWN", row.Status)
			} else {
				assert.Equal(t, "RELEASED", row.Status)
			}
		})
	}
}

// realCommitFault wraps the actual SQL transaction. It returns an error only
// after the real claim commit succeeded, proving no transport follows ambiguity.
type realCommitFault struct {
	gorm.ConnPool
	fail bool
}

func (f *realCommitFault) ExecContext(ctx context.Context, q string, args ...interface{}) (sql.Result, error) {
	if strings.Contains(q, "INSERT INTO `platform_model_attempts`") {
		f.fail = true
	}
	return f.ConnPool.ExecContext(ctx, q, args...)
}
func (f *realCommitFault) QueryContext(ctx context.Context, q string, args ...interface{}) (*sql.Rows, error) {
	if strings.Contains(q, "INSERT INTO `platform_model_attempts`") {
		f.fail = true
	}
	return f.ConnPool.QueryContext(ctx, q, args...)
}
func (f *realCommitFault) Commit() error {
	if err := f.ConnPool.(gorm.TxCommitter).Commit(); err != nil {
		return err
	}
	if f.fail {
		return fmt.Errorf("lost commit acknowledgment")
	}
	return nil
}
func (f *realCommitFault) Rollback() error { return f.ConnPool.(gorm.TxCommitter).Rollback() }

type realBeginFault struct{ gorm.ConnPool }

func (f realBeginFault) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := f.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &realCommitFault{ConnPool: tx}, nil
}
func TestPairedKnowledgeAmbiguousActualCommit(t *testing.T) {
	f := newPairedFixture(t, true)
	original := f.db.Statement.ConnPool
	f.db.Statement.ConnPool = realBeginFault{ConnPool: original}
	t.Cleanup(func() { f.db.Statement.ConnPool = original })
	w := f.request(f.path, f.body, false, nil)
	assert.GreaterOrEqual(t, w.Code, 400)
	assert.Zero(t, f.sends)
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", "call").Error)
	assert.Equal(t, "UNKNOWN", row.Status)
	var n int64
	require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	require.NoError(t, f.adapter.Recover(context.Background()))
	assert.Zero(t, f.sends)
	assert.Zero(t, f.settles)
}

func TestPairedKnowledgeManagedMutationAtomicity(t *testing.T) {
	for _, mutation := range []string{"channel", "ability", "route-save", "native-counter", "tag", "rebuild"} {
		t.Run(mutation, func(t *testing.T) {
			f := newPairedFixture(t, true)
			pin := f.binding.PolicyDigest
			switch mutation {
			case "channel":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 102).Updates(map[string]any{"models": "replacement", "group": "other"}).Error)
				var rows []model.Ability
				require.NoError(t, f.db.Where("channel_id = ?", 102).Find(&rows).Error)
				require.Len(t, rows, 1)
				assert.Equal(t, "replacement", rows[0].Model)
				assert.Equal(t, "other", rows[0].Group)
			case "ability":
				require.NoError(t, f.db.Model(&model.Ability{}).Where("channel_id = ?", 102).Update("enabled", false).Error)
			case "route-save":
				var route model.ClientModelRoute
				require.NoError(t, f.db.First(&route, 2).Error)
				old := route.KnowledgeGeneration
				route.DisplayName = "new"
				require.NoError(t, f.db.Save(&route).Error)
				require.NoError(t, f.db.First(&route, 2).Error)
				assert.NotEqual(t, old, route.KnowledgeGeneration)
			case "tag":
				tag := "managed"
				require.NoError(t, model.BatchSetChannelTag([]int{102}, &tag))
			case "rebuild":
				ok, failed, err := model.FixAbility()
				require.NoError(t, err)
				assert.Equal(t, 1, ok)
				assert.Zero(t, failed)
			case "native-counter":
				channel := model.Channel{Id: 102}
				channel.UpdateResponseTime(3)
				channel.UpdateBalance(1)
			}
			_, err := f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, pin)
			if mutation == "native-counter" {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, platformbilling.ErrConflict)
			}
		})
	}
}
func TestPairedKnowledgeBoundedHistoryFailsClosed(t *testing.T) {
	f := newPairedFixture(t, true)
	// Populate retained history as an operator fixture, preserving the live row.
	for i := 1; i < 64; i++ {
		require.NoError(t, f.db.Create(&model.KnowledgeRoutePin{Pin: fmt.Sprintf("%064x", i), InstanceID: "default", OriginProductID: "superwagie", Model: f.name, WireOperation: f.wire, RouteID: 2}).Error)
	}
	result, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"llmrouter", "superwagie"})
	assert.ErrorIs(t, err, platformbilling.ErrPending)
	assert.Nil(t, result)
	current, err := f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, f.binding.PolicyDigest)
	require.NoError(t, err)
	assert.Equal(t, f.binding.RouteGeneration, current.RouteGeneration)
}

func TestPairedKnowledgePreflightAndReceiptDenials(t *testing.T) {
	f := newPairedFixture(t, true)
	preflight := fmt.Sprintf(`{"model":%q,"wire_operation":%q}`, f.name, f.wire)
	for _, tc := range []struct {
		body   string
		edit   func(*http.Request)
		status int
	}{
		{preflight, func(r *http.Request) { r.Header.Set("X-UniHub-Task-ID", "other") }, 403},
		{preflight, func(r *http.Request) { r.Header.Set("Authorization", "Bearer uh_key_fixture") }, 403},
		{`{"model":"unihub-text-embedding-v4","model":"qwen3-rerank","wire_operation":"embeddings.create"}`, nil, 400},
		{`{"model":"qwen3-rerank","wire_operation":"rerank"}`, nil, 400},
		{f.readBody(), func(r *http.Request) { r.Header.Set("Authorization", "Bearer original-actor") }, 400},
	} {
		w := f.request("/platform/v1/knowledge/route", tc.body, false, tc.edit)
		assert.Equal(t, tc.status, w.Code, w.Body.String())
	}
	assert.Zero(t, f.reserves)
	assert.Zero(t, f.sends)
	f.claimChecks = 0
	w := f.request(f.path, f.body, false, nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	read := func() *httptest.ResponseRecorder {
		return f.request("/platform/v1/knowledge/results/call", "", false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer uh_model_reader") })
	}
	f.revoked = true
	assert.Equal(t, 404, read().Code)
	f.revoked = false
	f.remote.CredentialID = "swapped-producer"
	assert.Equal(t, 404, read().Code)
	f.remote.CredentialID = "producer"
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", "call").Error)
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "call").Update("media_snapshot", strings.Replace(row.MediaSnapshot, f.binding.PolicyDigest, strings.Repeat("c", 64), 1)).Error)
	assert.Equal(t, 404, read().Code, "a historical pin cannot be stitched into another authenticated result")
}

func TestPairedFixwaveProductSpecificDispatch(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		for _, product := range []string{"llmrouter", "superwagie"} {
			for _, client := range []string{product, "all"} {
				t.Run(fmt.Sprintf("%t/%s/%s", embedding, product, client), func(t *testing.T) {
					f := newPairedFixture(t, embedding)
					f.product = product
					require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 2).Update("client", client).Error)
					bindings, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{product})
					require.NoError(t, err)
					f.binding = bindings[0]
					preflight := fmt.Sprintf(`{"model":%q,"wire_operation":%q}`, f.name, f.wire)
					pre := f.request("/platform/v1/knowledge/route", preflight, false, nil)
					require.Equal(t, 200, pre.Code, pre.Body.String())
					f.claimChecks = 0
					w := f.request(f.path, f.body, false, func(r *http.Request) {
						if embedding {
							r.URL.RawQuery = "client=forged-product"
						} else {
							r.Header.Set("X-Client", "forged-product")
						}
					})
					require.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, 1, f.reserves)
					assert.Equal(t, 1, f.sends)
					assert.Equal(t, 1, f.settles)
				})
			}
		}
	}
}
func TestPairedFixwaveWrongClientBeforeHold(t *testing.T) {
	f := newPairedFixture(t, true)
	require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 2).Update("client", "llmrouter").Error)
	bindings, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"llmrouter"})
	require.NoError(t, err)
	f.binding = bindings[0]
	w := f.request(f.path, f.body, false, func(r *http.Request) { r.URL.RawQuery = "client=llmrouter" })
	assert.Equal(t, 409, w.Code, w.Body.String())
	assert.Zero(t, f.reserves)
	assert.Zero(t, f.sends)
}
func TestPairedFixwaveIncomingMutationRetires(t *testing.T) {
	for _, mode := range []string{"alias-map", "alias-save", "alias-struct", "public-name", "target-map", "target-save", "target-struct"} {
		t.Run(mode, func(t *testing.T) {
			f := newPairedFixture(t, true)
			unrelated := model.ClientModelRoute{Id: 9, PublicId: "native-nine", DisplayName: "native", Client: "all", Aliases: "[]", Enabled: true}
			require.NoError(t, f.db.Create(&unrelated).Error)
			extra := model.ClientModelTarget{Id: 9, RouteId: 9, ChannelId: 102, UpstreamModel: f.upstream, BillingModel: f.upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}
			if strings.HasPrefix(mode, "target") {
				require.NoError(t, f.db.Create(&extra).Error)
			}
			switch mode {
			case "alias-map":
				require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 9).Update("aliases", `["unihub-text-embedding-v4"]`).Error)
			case "alias-save":
				unrelated.Aliases = `["unihub-text-embedding-v4"]`
				require.NoError(t, f.db.Save(&unrelated).Error)
			case "alias-struct":
				require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 9).Updates(model.ClientModelRoute{Aliases: `["unihub-text-embedding-v4"]`}).Error)
			case "public-name":
				require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 9).Update("public_id", "UNIHUB-TEXT-EMBEDDING-V4").Error)
			case "target-map":
				require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = ?", 9).Update("route_id", 2).Error)
			case "target-save":
				extra.RouteId = 2
				require.NoError(t, f.db.Save(&extra).Error)
			case "target-struct":
				require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = ?", 9).Updates(model.ClientModelTarget{RouteId: 2}).Error)
			}
			var active model.KnowledgeActiveBinding
			require.NoError(t, f.db.First(&active, "origin_product_id = ?", "superwagie").Error)
			assert.Empty(t, active.ActivePin, "incoming scope retired immediately")
			_, err := f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, f.binding.PolicyDigest)
			assert.ErrorIs(t, err, platformbilling.ErrConflict)
			if strings.HasPrefix(mode, "target") {
				require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = ?", 9).Update("route_id", 9).Error)
			} else {
				require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 9).Updates(map[string]any{"aliases": "[]", "public_id": "native-nine"}).Error)
			}
			_, err = f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, f.binding.PolicyDigest)
			assert.ErrorIs(t, err, platformbilling.ErrConflict, "revert cannot revive old pin")
			bindings, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"superwagie"})
			require.NoError(t, err)
			assert.NotEqual(t, f.binding.PolicyDigest, bindings[0].PolicyDigest)
			_, err = f.adapter.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, bindings[0].PolicyDigest)
			require.NoError(t, err)
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
		})
	}
}

func TestPairedFixwavePartialStructMutations(t *testing.T) {
	t.Run("incoming-alias-retires", func(t *testing.T) {
		f := newPairedFixture(t, true)
		unrelated := model.ClientModelRoute{Id: 9, PublicId: "native-nine", DisplayName: "native", Client: "all", Aliases: "[]", Enabled: true}
		require.NoError(t, f.db.Create(&unrelated).Error)
		require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 9).Updates(struct{ Aliases string }{Aliases: `["unihub-text-embedding-v4"]`}).Error)
		var active model.KnowledgeActiveBinding
		require.NoError(t, f.db.First(&active, "origin_product_id = ?", "superwagie").Error)
		assert.Empty(t, active.ActivePin)
	})

	t.Run("target-reparent-retires", func(t *testing.T) {
		f := newPairedFixture(t, true)
		unrelated := model.ClientModelRoute{Id: 9, PublicId: "native-nine", DisplayName: "native", Client: "all", Aliases: "[]", Enabled: true}
		require.NoError(t, f.db.Create(&unrelated).Error)
		extra := model.ClientModelTarget{Id: 9, RouteId: 9, ChannelId: 102, UpstreamModel: f.upstream, BillingModel: f.upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}
		require.NoError(t, f.db.Create(&extra).Error)
		require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = ?", 9).Updates(struct{ RouteId int }{RouteId: 2}).Error)
		var active model.KnowledgeActiveBinding
		require.NoError(t, f.db.First(&active, "origin_product_id = ?", "superwagie").Error)
		assert.Empty(t, active.ActivePin)
	})

	t.Run("zero-valued-partial-update-is-no-op", func(t *testing.T) {
		f := newPairedFixture(t, true)
		unrelated := model.ClientModelRoute{Id: 9, PublicId: "native-nine", DisplayName: "native", Client: "all", Aliases: "[]", Enabled: true}
		require.NoError(t, f.db.Create(&unrelated).Error)
		require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 9).Updates(struct{ Aliases string }{}).Error)
		var active model.KnowledgeActiveBinding
		require.NoError(t, f.db.First(&active, "origin_product_id = ?", "superwagie").Error)
		assert.NotEmpty(t, active.ActivePin, "zero-valued struct field is omitted by GORM")
		var route model.ClientModelRoute
		require.NoError(t, f.db.First(&route, 9).Error)
		assert.Equal(t, "[]", route.Aliases)
	})

	t.Run("omitted-partial-field-is-no-op", func(t *testing.T) {
		f := newPairedFixture(t, true)
		unrelated := model.ClientModelRoute{Id: 9, PublicId: "native-nine", DisplayName: "native", Client: "all", Aliases: "[]", Enabled: true}
		require.NoError(t, f.db.Create(&unrelated).Error)
		require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 9).Omit("aliases").Updates(struct{ Aliases string }{Aliases: `["unihub-text-embedding-v4"]`}).Error)
		var active model.KnowledgeActiveBinding
		require.NoError(t, f.db.First(&active, "origin_product_id = ?", "superwagie").Error)
		assert.NotEmpty(t, active.ActivePin, "GORM omitted field must not participate in retirement")
		var route model.ClientModelRoute
		require.NoError(t, f.db.First(&route, 9).Error)
		assert.Equal(t, "[]", route.Aliases)
	})
}
func TestPairedFixwaveReadAuthorityMatrix(t *testing.T) {
	for _, name := range []string{"issuer", "instance", "subject", "service-token", "approved-other-product-pin"} {
		t.Run(name, func(t *testing.T) {
			f := newPairedFixture(t, true)
			status := 403
			var edit func(*http.Request)
			switch name {
			case "issuer":
				f.accessIssuer = "https://wrong-center.test"
			case "instance":
				f.accessInstance = "other"
			case "subject":
				f.accessSubject = "other"
			case "service-token":
				edit = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+f.config.ServiceToken) }
			case "approved-other-product-pin":
				f.product = "llmrouter"
				status = 409
			}
			w := f.request("/platform/v1/knowledge/route", f.readBody(), true, edit)
			assert.Equal(t, status, w.Code, w.Body.String())
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
			assert.Zero(t, f.settles)
			assert.Zero(t, f.verifies)
			if name == "service-token" {
				assert.Zero(t, f.reads)
			}
		})
	}
}

func TestPairedFixwaveVerifiedProductSendBoundary(t *testing.T) {
	for _, phase := range []string{"before-claim", "after-claim", "native-rerank"} {
		t.Run(phase, func(t *testing.T) {
			f := newPairedFixture(t, phase != "native-rerank")
			require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 2).Update("client", "superwagie").Error)
			bindings, err := f.adapter.ActivateManagedKnowledge(context.Background(), f.name, f.wire, []string{"superwagie"})
			require.NoError(t, err)
			f.binding = bindings[0]
			if phase == "before-claim" {
				f.beforeClaim = func() { f.product = "llmrouter" }
			}
			if phase == "after-claim" {
				f.afterClaim = func() {
					require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 2).Update("client", "llmrouter").Error)
				}
			}
			path, body := f.path, f.body
			if phase == "native-rerank" {
				path = "/platform/api/v1/services/rerank/text-rerank/text-rerank"
				body = `{"model":"qwen3-rerank","input":{"query":"q","documents":["a"]},"parameters":{"top_n":1,"return_documents":false}}`
			}
			w := f.request(path, body, false, nil)
			assert.Equal(t, 1, f.reserves)
			if phase == "before-claim" {
				assert.GreaterOrEqual(t, w.Code, 400)
				assert.Zero(t, f.sends)
			} else {
				assert.Equal(t, 200, w.Code, w.Body.String())
				assert.Equal(t, 1, f.sends)
			}
		})
	}
}

func TestPairedKnowledgePublicIssuerPreservesActivatedPins(t *testing.T) {
	for _, embedding := range []bool{false, true} {
		t.Run(fmt.Sprintf("embedding=%t", embedding), func(t *testing.T) {
			f := newPairedFixture(t, embedding)
			f.refusePaid = true
			var beforePins []model.KnowledgeRoutePin
			var beforeActive []model.KnowledgeActiveBinding
			require.NoError(t, f.db.Order("pin").Find(&beforePins).Error)
			require.NoError(t, f.db.Order("origin_product_id").Find(&beforeActive).Error)
			require.Len(t, beforePins, 2)
			cfg := f.config
			cfg.IdentityIssuer = "https://unihub.quukk.com"
			f.accessIssuer = cfg.IdentityIssuer
			node, err := platformbilling.New(f.db, cfg)
			require.NoError(t, err)
			InstallKnowledgeBindings(node)
			for _, active := range beforeActive {
				f.product = active.OriginProductID
				route, err := node.ReadCurrentKnowledgeRoute(context.Background(), "original-actor", "account", f.name, f.wire, active.ActivePin)
				require.NoError(t, err)
				assert.Equal(t, active.ActivePin, route.PolicyDigest)
				assert.Equal(t, active.RouteGeneration, route.RouteGeneration)
			}
			var afterPins []model.KnowledgeRoutePin
			var afterActive []model.KnowledgeActiveBinding
			require.NoError(t, f.db.Order("pin").Find(&afterPins).Error)
			require.NoError(t, f.db.Order("origin_product_id").Find(&afterActive).Error)
			assert.Equal(t, beforePins, afterPins)
			assert.Equal(t, beforeActive, afterActive)
			assert.Zero(t, f.sends)
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.settles)
			assert.Zero(t, f.verifies)
		})
	}
}

func TestPairedKnowledgeAliCompatibleEmbeddingUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		valid       bool
	}{
		{"total only", `{"total_tokens":3}`, true},
		{"input alias", `{"prompt_tokens":3,"total_tokens":3}`, true},
		{"zero completion", `{"prompt_tokens":3,"total_tokens":3,"completion_tokens":0}`, true},
		{"contradictory input", `{"prompt_tokens":2,"total_tokens":3}`, false},
		{"nonzero completion", `{"prompt_tokens":3,"total_tokens":3,"completion_tokens":1}`, false},
		{"unknown counter", `{"prompt_tokens":3,"total_tokens":3,"other_tokens":0}`, false},
		{"missing total", `{"prompt_tokens":3}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPairedFixture(t, true)
			f.embeddingUsage = tc.usage
			w := f.request(f.path, f.body, false, nil)
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call, "call_id = ?", "call").Error)
			require.Equal(t, 1, f.sends)
			require.Equal(t, 1, f.reserves)
			if tc.valid {
				require.Equal(t, 200, w.Code, w.Body.String())
				assert.Equal(t, 1, f.settles)
				assert.Equal(t, "SETTLED", call.Status)
				assert.Contains(t, call.Evidence, `"value":"3"`)
				read := f.request("/platform/v1/knowledge/results/call", "", false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer uh_model_reader") })
				assert.Equal(t, 200, read.Code, read.Body.String())
			} else {
				assert.Equal(t, 502, w.Code, w.Body.String())
				assert.Zero(t, f.settles)
				assert.Equal(t, "UNKNOWN", call.Status)
				assert.Empty(t, call.Evidence)
				assert.Empty(t, call.Intent)
				assert.Contains(t, w.Body.String(), "knowledge_result_embedding_usage_")
				assert.NotContains(t, w.Body.String(), tc.usage)
			}
			replay := f.request(f.path, f.body, false, nil)
			assert.Equal(t, 409, replay.Code)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.reserves)
		})
	}
}
