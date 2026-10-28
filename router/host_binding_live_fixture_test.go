package router

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Opt-in TCP fixture for the actual SW host. Only supplier responses are synthetic.
func TestHostBindingLiveFixture(t *testing.T) {
	if os.Getenv("HOST_BINDING_FIXTURE") != "1" {
		t.Skip("isolated host acceptance only")
	}
	enableUniHubBoundary(t)
	dir := os.Getenv("HOST_BINDING_DIR")
	require.True(t, filepath.IsAbs(dir))
	gin.SetMode(gin.ReleaseMode)
	require.NoError(t, i18n.Init())
	constant.StreamingTimeout = 30
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "gateway.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB = db
	model.LOG_DB = db
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	common.RetryTimes = 0
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "host-billing-principal", AffCode: "host-principal", Status: common.UserStatusEnabled, Group: "platform"}).Error)
	require.NoError(t, db.Create(&model.User{Id: 2, Username: "native-preserved", AffCode: "native-preserved", Status: common.UserStatusEnabled, Group: "default", Quota: 12345, UsedQuota: 7, RequestCount: 3}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 2, Key: "fixtureNativePreserved", Status: common.TokenStatusEnabled, RemainQuota: 54321, UsedQuota: 11, ExpiredTime: -1}).Error)
	var embeds, ranks, chats atomic.Int64
	var emittedHelper, emittedLibrary atomic.Bool
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string           `json:"model"`
			Input    any              `json:"input"`
			Stream   bool             `json:"stream"`
			Messages []map[string]any `json:"messages"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &body))
		var call platformbilling.Call
		require.NoError(t, db.Order("created_at DESC").First(&call).Error)
		require.Equal(t, "UNKNOWN", call.Status)
		require.Equal(t, "Bearer synthetic-not-a-provider-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if body.Model == "upstream-chat" {
			chats.Add(1)
			require.Equal(t, "/v1/chat/completions", r.URL.Path)
			if !body.Stream {
				_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","created":1,"model":"upstream-chat","choices":[{"index":0,"message":{"role":"assistant","content":"Verified helper image"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
				return
			}
			var tool string
			var args map[string]any
			raw, _ := common.Marshal(body.Messages)
			if bytes.Contains(raw, []byte("Run actual helper")) && emittedHelper.CompareAndSwap(false, true) {
				tool = "superwagie_managed_helper"
				args = map[string]any{"skill": "ai-image-to-ppt", "action": "inspect", "args": map[string]any{"workId": "inspect-fixture", "image": "fixture.png", "question": "Identify the fixture"}}
			}
			if bytes.Contains(raw, []byte("Search actual library")) && emittedLibrary.CompareAndSwap(false, true) {
				tool = "openchamber_library"
				args = map[string]any{"action": "library.search", "parameters": map[string]any{"query": "orange lighthouse"}}
			}
			delta := map[string]any{"role": "assistant", "content": "Host action complete"}
			finish := "stop"
			if tool != "" {
				arguments, _ := common.Marshal(args)
				delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "tool_host_fixture", "type": "function", "function": map[string]any{"name": tool, "arguments": string(arguments)}}}}
				finish = "tool_calls"
			}
			first, _ := common.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "chat", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
			last, _ := common.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "chat", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3}})
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", first, last)
			return
		}

		if body.Model == "upstream-embed" {
			require.Equal(t, "/v1/embeddings", r.URL.Path)
			embeds.Add(1)
			inputs, ok := body.Input.([]any)
			if !ok {
				inputs = []any{body.Input}
			}
			data := make([]any, len(inputs))
			for i := range inputs {
				data[i] = map[string]any{"index": i, "embedding": []float64{0.9, 0.1, 0.2}}
			}
			raw, e := common.Marshal(map[string]any{"model": body.Model, "object": "list", "data": data, "usage": map[string]int{"prompt_tokens": 3, "total_tokens": 3}})
			require.NoError(t, e)
			_, _ = w.Write(raw)
			return
		}
		require.Equal(t, "qwen3-rerank", body.Model)
		require.Equal(t, "/api/v1/services/rerank/text-rerank/text-rerank", r.URL.Path)
		ranks.Add(1)
		input, ok := body.Input.(map[string]any)
		require.True(t, ok)
		documents, ok := input["documents"].([]any)
		require.True(t, ok)
		results := make([]any, len(documents))
		for i := range documents {
			results[i] = map[string]any{"index": i, "relevance_score": 0.99}
		}
		raw, e := common.Marshal(map[string]any{"output": map[string]any{"results": results}, "usage": map[string]int{"total_tokens": 3}})
		require.NoError(t, e)
		_, _ = w.Write(raw)
	}))
	defer supplier.Close()
	config := platformbilling.Config{Enabled: true, Origin: os.Getenv("HOST_BINDING_UH"), ServiceToken: os.Getenv("HOST_BINDING_SERVICE_TOKEN"), InstanceID: "fixture", PrincipalID: 1, Group: "platform", MaxBodyBytes: 1048576, MaxInputTokens: 65536, MaxOutputTokens: 8192, KnowledgePolicies: map[string]map[string]platformbilling.KnowledgePolicy{}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{}, OperationRateIDs: map[string]map[string]map[string]string{}}
	for i, item := range []struct {
		public, upstream, wire, operation string
		channelType                       int
	}{{"embedding", "upstream-embed", "embeddings.create", "embedding", constant.ChannelTypeOpenAI}, {"rerank", "qwen3-rerank", "rerank.create", "rerank", constant.ChannelTypeAli}} {
		id := i + 1
		base := supplier.URL
		require.NoError(t, db.Create(&model.Channel{Id: id, Type: item.channelType, Key: "synthetic-not-a-provider-key", Status: common.ChannelStatusEnabled, Name: item.public, BaseURL: &base, Models: item.upstream, Group: "platform"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: item.upstream, ChannelId: id, Enabled: true}).Error)
		metadata := fmt.Sprintf(`{"operations":[%q],"input_modalities":["text"]}`, item.wire)
		var space *dto.EmbeddingSpace
		if item.public == "embedding" {
			space = &dto.EmbeddingSpace{Provider: "openai", Model: item.upstream, Version: "v1", Dimensions: 3, InputSemantics: "text"}
			metadata = `{"operations":["embeddings.create"],"input_modalities":["text"],"embedding_space":{"provider":"openai","model":"upstream-embed","version":"v1","dimensions":3,"input_semantics":"text"}}`
		}
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: id, PublicId: item.public, DisplayName: item.public, Client: "all", Enabled: true, Metadata: metadata}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: id, RouteId: id, ChannelId: id, UpstreamModel: item.upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
		rates := map[string]string{"CNY/6": item.public + "-rate"}
		config.KnowledgePolicies[item.wire] = map[string]platformbilling.KnowledgePolicy{item.public: {ChannelKeySHA256: "d03933be66561c29ba59bef3ca8dc6aac78b7bafeefcd9ef9744cb9913f8c75b", Version: "knowledge_v1", ProviderReference: "https://fixture.test/policy", PublicModel: item.public, WireOperation: item.wire, ChannelID: id, ChannelType: item.channelType, UpstreamModel: item.upstream, HardInputTokens: 10000, MaxItemTokens: 2000, MaxBatch: 32, RateIDs: rates, Space: space}}
		config.OperationLimits[item.operation] = map[string]platformbilling.OperationLimits{item.public: {Policy: "provider_reported_v1"}}
		config.OperationRateIDs[item.operation] = map[string]map[string]string{"CNY/6": {item.public: item.public + "-rate"}}
	}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	base := supplier.URL
	require.NoError(t, db.Create(&model.Channel{Id: 3, Type: constant.ChannelTypeOpenAI, Key: "synthetic-not-a-provider-key", Status: common.ChannelStatusEnabled, Name: "chat", BaseURL: &base, Models: "upstream-chat", Group: "platform"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "upstream-chat", ChannelId: 3, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 3, PublicId: "chat", DisplayName: "Chat", Client: "all", Enabled: true, Metadata: `{"operations":["chat.completions.create"],"supports_tools":true,"context_window":32000,"input_modalities":["text","image"],"output_modalities":["text"],"max_output_tokens":1000,"streaming":true}`}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 3, RouteId: 3, ChannelId: 3, UpstreamModel: "upstream-chat", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	config.OperationLimits["chat"] = map[string]platformbilling.OperationLimits{"chat": {Policy: "provider_reported_v1", MaxBodyBytes: 1048576}}
	config.OperationRateIDs["chat"] = map[string]map[string]string{"CNY/6": {"chat": "chat-rate"}}
	config.NativeInputPolicies = map[string]map[string]platformbilling.NativeInputPolicy{"chat.completions.create": {"chat": {Version: "hard_context_v1", ProviderReference: "https://fixture.test/chat", ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "upstream-chat", HardInputTokens: 32000, MaxBytes: 1048576, MaxImages: 2, MaxPixels: 1000000, MaxDocuments: 1, MaxPages: 1}}}
	model.InitChannelCache()
	adapter, err := platformbilling.New(db, config)
	require.NoError(t, err)
	catalog, err := readTechnicalCatalog(context.Background(), adapter, "superwagie")
	require.NoError(t, err)
	require.Len(t, catalog.Models, 3)
	for _, model := range catalog.Models {
		require.Len(t, model.Operations, 1)
		require.True(t, model.Operations[0].Available, model.ID+":"+model.Operations[0].DisabledReason)
		require.Positive(t, model.Operations[0].Limits.MaxBodyBytes)
	}
	engine := gin.New()
	engine.Use(gin.Recovery(), middlewareBodyCleanupForTest())
	engine.Use(func(c *gin.Context) {
		recorder := &hostFixtureResponse{ResponseWriter: c.Writer}
		c.Writer = recorder
		c.Next()
		if c.Writer.Status() >= 400 {
			t.Logf("gateway rejection %s %s %d %s", c.Request.Method, c.Request.URL.Path, c.Writer.Status(), recorder.body.String())
		}
	})
	SetManagedKeyDispatch(engine, adapter)
	SetPlatformRouter(engine, adapter)
	SetModelCatalogRouter(engine, adapter, platformbilling.CatalogConfig{Token: os.Getenv("HOST_BINDING_CATALOG_TOKEN"), ProductID: "superwagie", InstanceID: "fixture"})
	engine.GET("/__fixture/status", func(c *gin.Context) {
		var native model.User
		var token model.Token
		require.NoError(t, db.First(&native, 2).Error)
		require.NoError(t, db.First(&token, 1).Error)
		var calls []platformbilling.Call
		require.NoError(t, db.Find(&calls).Error)
		c.JSON(200, gin.H{"chat_calls": chats.Load(), "embedding_calls": embeds.Load(), "rerank_calls": ranks.Load(), "native_quota": native.Quota, "native_used_quota": native.UsedQuota, "native_requests": native.RequestCount, "native_token_quota": token.RemainQuota, "native_token_used_quota": token.UsedQuota, "calls": calls})
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready"), []byte("http://"+listener.Addr().String()), 0600))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Minute):
	}
}

type hostFixtureResponse struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *hostFixtureResponse) Write(b []byte) (int, error) {
	if w.body.Len() < 4096 {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}
