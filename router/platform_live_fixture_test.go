package router

import (
	"bytes"
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/dto"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Explicit opt-in local acceptance harness. UniHub and its ledger remain real;
// only the synthetic OpenAI provider is simulated. Never reads provider stores.
func TestPlatformLocalLiveFixture(t *testing.T) {
	if os.Getenv("UNIHUB_PLATFORM_E2E") != "1" {
		t.Skip("opt-in local UniHub acceptance fixture")
	}
	enableUniHubBoundary(t)
	envFile := os.Getenv("UNIHUB_PLATFORM_E2E_ENV_FILE")
	require.True(t, filepath.IsAbs(envFile), "UNIHUB_PLATFORM_E2E_ENV_FILE must explicitly name an absolute local env file")
	values, e := godotenv.Read(envFile)
	require.NoError(t, e)
	token := values["MODEL_SERVICE_TOKEN"]
	require.GreaterOrEqual(t, len(token), 32, "MODEL_SERVICE_TOKEN must exist in local UniHub env")
	gin.SetMode(gin.ReleaseMode)
	require.NoError(t, i18n.Init())
	constant.StreamingTimeout = 30
	dbPath := os.Getenv("UNIHUB_PLATFORM_E2E_DATABASE")
	if dbPath == "" {
		dbPath = filepath.Join(t.TempDir(), "gateway.db")
	}
	require.True(t, filepath.IsAbs(dbPath))
	db, e := gorm.Open(sqlite.Open("file:"+dbPath), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB = db
	model.LOG_DB = db
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	common.RetryTimes = 1
	nativeWire := os.Getenv("UNIHUB_PLATFORM_E2E_NATIVE_WIRE")
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if nativeWire != "" {
			var call platformbilling.Call
			require.NoError(t, db.Order("created_at DESC").First(&call).Error)
			require.Equal(t, "UNKNOWN", call.Status)
			var attempts int64
			require.NoError(t, db.Model(&platformbilling.Attempt{}).Where("call_id = ? AND status = ?", call.CallID, "DISPATCHED").Count(&attempts).Error)
			require.EqualValues(t, 1, attempts)
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			if nativeWire == "embeddings.create" || nativeWire == "rerank.create" || nativeWire == "multimodal.embeddings.create" {
				usage := `{"total_tokens":3}`
				if nativeWire == "multimodal.embeddings.create" {
					usage = `{"input_tokens":2,"image_tokens":3,"total_tokens":5}`
				}
				mode := os.Getenv("UNIHUB_PLATFORM_E2E_KNOWLEDGE_CASE")
				if mode == "zero" {
					usage = `{"total_tokens":0}`
					if nativeWire == "multimodal.embeddings.create" {
						usage = `{"input_tokens":0,"image_tokens":0,"total_tokens":0}`
					}
				}
				if mode == "missing" {
					usage = `{}`
				}
				switch mode {
				case "duplicate":
					usage = `{"total_tokens":3,"total_tokens":3}`
					if nativeWire == "multimodal.embeddings.create" {
						usage = `{"input_tokens":2,"input_tokens":2,"image_tokens":3,"total_tokens":5}`
					}
				case "partial":
					usage = `{"prompt_tokens":3}`
					if nativeWire == "multimodal.embeddings.create" {
						usage = `{"input_tokens":2,"total_tokens":5}`
					}
				case "conflict":
					usage = `{"total_tokens":3,"input_tokens":4}`
					if nativeWire == "multimodal.embeddings.create" {
						usage = `{"input_tokens":2,"image_tokens":3,"total_tokens":4}`
					}
				case "overcap":
					usage = `{"total_tokens":1001}`
					if nativeWire == "multimodal.embeddings.create" {
						usage = `{"input_tokens":1001,"image_tokens":3,"total_tokens":1004}`
					}
				}
				response := `{"output":{"results":[{"index":0,"relevance_score":0.9}]},"usage":` + usage + `}`
				if nativeWire == "multimodal.embeddings.create" {
					vector := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 256), ",") + "]"
					response = fmt.Sprintf(`{"output":{"embeddings":[{"index":0,"type":"vl","embedding":%s}]},"usage":%s}`, vector, usage)
					require.Equal(t, "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", r.URL.Path)
				} else if nativeWire == "embeddings.create" {
					require.Equal(t, "/v1/embeddings", r.URL.Path)
				} else {
					require.Equal(t, "/api/v1/services/rerank/text-rerank/text-rerank", r.URL.Path)
				}
				if nativeWire == "embeddings.create" {
					usage = `{"prompt_tokens":3,"total_tokens":3}`
					switch mode {
					case "zero":
						usage = `{"prompt_tokens":0,"total_tokens":0}`
					case "missing":
						usage = `{}`
					case "duplicate":
						usage = `{"prompt_tokens":3,"prompt_tokens":3,"total_tokens":3}`
					case "partial":
						usage = `{"total_tokens":3}`
					case "conflict":
						usage = `{"prompt_tokens":3,"total_tokens":4}`
					case "overcap":
						usage = `{"prompt_tokens":1001,"total_tokens":1001}`
					}
					response = `{"model":"upstream-embed","object":"list","data":[{"index":0,"embedding":[0.1,0.2,0.3]}],"usage":` + usage + `}`
				}
				if mode == "bad-result" {
					response = strings.Replace(response, `"index":0`, `"index":9`, 1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(response))
				return
			}
			require.Contains(t, string(raw), "JVBERi0")
			response := `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic local provider"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
			switch nativeWire {
			case "responses.compact":
				require.Equal(t, "/v1/responses/compact", r.URL.Path)
				require.NotContains(t, string(raw), "max_output_tokens")
				require.Contains(t, string(raw), "prior-opaque")
				response = `{"id":"cmp","created_at":1700000000,"object":"response.compaction","output":[{"type":"compaction","id":"ci","encrypted_content":"synthetic local provider"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}}`
			case "responses.create":
				response = `{"id":"r","object":"response","status":"completed","output":[{"id":"m","type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic local provider"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
			case "claude.messages.create":
				require.Equal(t, "/v1/messages", r.URL.Path)
				response = `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"synthetic local provider"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
			case "gemini.generate":
				require.Equal(t, "/v1beta/models/unihub-synthetic-model:generateContent", r.URL.Path)
				response = `{"candidates":[{"content":{"role":"model","parts":[{"text":"synthetic local provider"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(response))
			return
		}

		var body struct {
			Stream bool `json:"stream"`
		}
		if common.DecodeJson(r.Body, &body) != nil {
			w.WriteHeader(400)
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic local provider\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"unihub-synthetic-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic local provider"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`))
	}))
	defer upstream.Close()
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{Id: 1, Username: "isolated-non-financial-principal", AffCode: "fixture-principal", Status: common.UserStatusEnabled, Group: "platform", Quota: 0}).Error)
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{Id: 2, Username: "isolated-native-compatibility-marker", AffCode: "fixture-native", Status: common.UserStatusEnabled, Group: "default", Quota: 12345, UsedQuota: 7, RequestCount: 3}).Error)
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.Token{Id: 1, UserId: 2, Key: "fixtureNativePreserved", Status: common.TokenStatusEnabled, RemainQuota: 54321, UsedQuota: 11, ExpiredTime: -1}).Error)
	channelType := constant.ChannelTypeOpenAI
	if nativeWire == "claude.messages.create" {
		channelType = constant.ChannelTypeAnthropic
	}
	if nativeWire == "gemini.generate" {
		channelType = constant.ChannelTypeGemini
	}
	if nativeWire == "rerank.create" || nativeWire == "multimodal.embeddings.create" {
		channelType = constant.ChannelTypeAli
	}
	base := upstream.URL
	require.NoError(t, db.Save(&model.Channel{Id: 1, Type: channelType, Key: "synthetic-not-a-provider-key", Status: common.ChannelStatusEnabled, Name: "isolated synthetic channel", BaseURL: &base, Models: "unihub-synthetic-model", Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.Ability{Group: "platform", Model: "unihub-synthetic-model", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"unihub-synthetic-model":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	origin := os.Getenv("UNIHUB_PLATFORM_E2E_ORIGIN")
	if origin == "" {
		origin = "http://localhost:8088"
	}
	config := platformbilling.Config{Enabled: true, Origin: origin, ServiceToken: token, InstanceID: os.Getenv("UNIHUB_PRODUCT_INSTANCE_ID"), PrincipalID: 1, Group: "platform", MaxBodyBytes: 32768, MaxInputTokens: 65536, MaxOutputTokens: 8192, RateIDs: map[string]string{"unihub-synthetic-model": "local-e2e-chat-v1"}}
	if raw := os.Getenv("UNIHUB_PLATFORM_E2E_ASSET_RATES"); raw != "" {
		require.NoError(t, common.UnmarshalJsonStr(raw, &config.AssetRateIDs))
	}
	if nativeWire != "" {
		require.Contains(t, []string{"chat.completions.create", "responses.create", "responses.compact", "claude.messages.create", "gemini.generate", "embeddings.create", "rerank.create", "multimodal.embeddings.create"}, nativeWire)
		operation := "chat"
		if nativeWire == "responses.create" || nativeWire == "responses.compact" {
			operation = "responses"
		}
		config.OperationRateIDs = map[string]map[string]map[string]string{operation: {"CNY/6": {"unihub-synthetic-model": "native-rate"}}}
		config.NativeInputPolicies = map[string]map[string]platformbilling.NativeInputPolicy{nativeWire: {"unihub-synthetic-model": {Version: "hard_context_v1", ProviderReference: "https://fixture.example/hard-context", ChannelType: channelType, UpstreamModel: "unihub-synthetic-model", HardInputTokens: 65536, MaxBytes: 10000, MaxImages: 2, MaxPixels: 100, MaxDocuments: 1, MaxPages: 2}}}
	}
	if nativeWire == "responses.compact" {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
		config.OperationLimits = map[string]map[string]platformbilling.OperationLimits{"responses": {"unihub-synthetic-model": {Policy: "sum"}}}
		config.CompactPolicies = map[string]platformbilling.CompactPolicy{"unihub-synthetic-model": {Version: "hard_context_v1", ProviderReference: "https://fixture.example/compact", PublicModel: "unihub-synthetic-model", ChannelID: 1, ChannelType: channelType, UpstreamModel: "unihub-synthetic-model", WireOperation: nativeWire, EndpointPath: "/v1/responses/compact", HardInputTokens: 65536, HardOutputTokens: 8192, ServiceTier: "auto", RateIDs: map[string]string{"CNY/6": "native-rate"}}}
		metadata := `{"operations":["responses.compact"],"input_modalities":["text","image","document"],"output_modalities":["text"],"max_output_tokens":8192,"media_limits":{"max_pages":2,"max_pixels":100,"max_images":2,"max_request_bytes":10000,"formats":["png","application/pdf"]}}`
		require.NoError(t, db.Save(&model.ClientModelRoute{Id: 1, PublicId: "unihub-synthetic-model", DisplayName: "Synthetic compact", Client: "all", Enabled: true, Metadata: metadata}).Error)
		require.NoError(t, db.Save(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "unihub-synthetic-model", PreferredWires: `["responses"]`, Enabled: true, Weight: 1}).Error)
	}
	if nativeWire == "embeddings.create" || nativeWire == "rerank.create" || nativeWire == "multimodal.embeddings.create" {
		config.NativeInputPolicies = nil
		op, upstreamModel, wire := "rerank", "qwen3-rerank", "openai_chat"
		metadata := `{"operations":["rerank.create"],"input_modalities":["text"]}`
		if nativeWire == "embeddings.create" {
			op, upstreamModel = "embedding", "upstream-embed"
			metadata = `{"operations":["embeddings.create"],"input_modalities":["text"],"embedding_space":{"provider":"openai","model":"upstream-embed","version":"v1","dimensions":3,"input_semantics":"text"}}`
		}

		if nativeWire == "multimodal.embeddings.create" {
			op, upstreamModel, wire = "multimodal_embedding", "qwen3-vl-embedding", "qwen_multimodal_embedding"
			metadata = `{"operations":["multimodal.embeddings.create"],"input_modalities":["text","image","video"],"output_modalities":["embedding"],"embedding_space":{"provider":"aliyun","model":"qwen3-vl-embedding","version":"v1","dimensions":256,"input_semantics":"` + dto.QwenMultimodalInputSemantics + `"},"media_limits":{"max_images":5,"max_pixels":16000000,"max_request_bytes":65536,"max_video_seconds":30,"formats":["png","mp4"]}}`
		}
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update("models", upstreamModel).Error)
		require.NoError(t, db.Save(&model.Ability{Group: "platform", Model: upstreamModel, ChannelId: 1, Enabled: true}).Error)
		require.NoError(t, db.Save(&model.ClientModelRoute{Id: 1, PublicId: "unihub-synthetic-model", DisplayName: "Knowledge", Client: "all", Enabled: true, Metadata: metadata}).Error)
		require.NoError(t, db.Save(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: upstreamModel, PreferredWires: `["` + wire + `"]`, Enabled: true, Weight: 1}).Error)
		p := platformbilling.KnowledgePolicy{ChannelKeySHA256: "d03933be66561c29ba59bef3ca8dc6aac78b7bafeefcd9ef9744cb9913f8c75b", Version: "knowledge_v1", ProviderReference: "https://fixture.test/policy", PublicModel: "unihub-synthetic-model", WireOperation: nativeWire, ChannelID: 1, ChannelType: channelType, UpstreamModel: upstreamModel, HardInputTokens: 1000, MaxItemTokens: 100, MaxBatch: 10, RateIDs: map[string]string{"CNY/6": "native-rate", "CREDIT/6": "native-rate"}}
		if op == "embedding" {
			p.Space = &dto.EmbeddingSpace{Provider: "openai", Model: upstreamModel, Version: "v1", Dimensions: 3, InputSemantics: "text"}
		}
		if op == "multimodal_embedding" {
			p.MaxBatch = 5
			p.VisualTokens = 2000
			p.Space = &dto.EmbeddingSpace{Provider: "aliyun", Model: upstreamModel, Version: "v1", Dimensions: 256, InputSemantics: dto.QwenMultimodalInputSemantics}
		}
		config.KnowledgePolicies = map[string]map[string]platformbilling.KnowledgePolicy{nativeWire: {"unihub-synthetic-model": p}}
		config.OperationLimits = map[string]map[string]platformbilling.OperationLimits{op: {"unihub-synthetic-model": {Policy: "provider_reported_v1"}}}
		config.OperationRateIDs = map[string]map[string]map[string]string{op: {"CNY/6": {"unihub-synthetic-model": "native-rate"}, "CREDIT/6": {"unihub-synthetic-model": "native-rate"}}}
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
		model.InitChannelCache()
	}
	adapter, e := platformbilling.New(db, config)
	require.NoError(t, e)
	if nativeWire == "responses.compact" || nativeWire == "embeddings.create" || nativeWire == "rerank.create" || nativeWire == "multimodal.embeddings.create" {
		catalog, err := readTechnicalCatalog(context.Background(), adapter, "superwagie")
		require.NoError(t, err)
		require.Len(t, catalog.Models, 1)
		require.True(t, catalog.Models[0].Operations[0].Available, catalog.Models[0].Operations[0].DisabledReason)
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/platform/v1/chat/completions" {
			c.Next()
			return
		}
		reader := &platformFixtureCountReader{ReadCloser: c.Request.Body}
		c.Request.Body = reader
		writer := &platformFixtureResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = common.Unmarshal(writer.errorBody.Bytes(), &envelope)
		code := envelope.Error.Code
		switch code {
		case "invalid_model_credential", "request_too_large", "stable_call_and_task_required", "platform_billing_unavailable", "logical_call_already_exists", "logical_call_conflict":
		default:
			code = "none_or_unrecognized"
		}
		t.Logf("platform fixture request: declared_bytes=%d read_bytes=%d status=%d error_code=%s", c.Request.ContentLength, reader.count, writer.Status(), code)
	})
	engine.Use(gin.Recovery(), middlewareBodyCleanupForTest())
	SetManagedKeyDispatch(engine, adapter)
	SetPlatformRouter(engine, adapter)
	if clientID := os.Getenv("UNIHUB_PLATFORM_E2E_PRODUCT_CLIENT_ID"); clientID != "" {
		require.NoError(t, db.AutoMigrate(&model.UserSession{}, &model.UniHubIdentityMapping{}, &model.UniHubSessionBinding{}, &model.Log{}, &model.QuotaData{}))
		publicOrigin := os.Getenv("UNIHUB_PLATFORM_E2E_PUBLIC_ORIGIN")
		require.NotEmpty(t, publicOrigin, "canonical UniHub issuer required for product-session acceptance")
		redirectURI := os.Getenv("UNIHUB_PLATFORM_E2E_REDIRECT_URI")
		require.NotEmpty(t, redirectURI, "registered product redirect URI required for product-session acceptance")
		SetUniHubRouter(engine, platformaccount.Config{Enabled: true, APIOrigin: origin, PublicOrigin: publicOrigin, ClientID: clientID, ProductID: "llmrouter", InstanceID: config.InstanceID, RedirectURI: redirectURI})
		SetApiRouter(engine)
	}
	// Only the explicit opt-in fixture exposes synthetic acceptance counters.
	engine.GET("/__fixture/status", func(c *gin.Context) {
		var user model.User
		require.NoError(t, db.Select("quota", "used_quota").First(&user, 1).Error)
		var native model.User
		var token model.Token
		require.NoError(t, db.Select("quota", "used_quota", "request_count").First(&native, 2).Error)
		require.NoError(t, db.Select("remain_quota", "used_quota").First(&token, 1).Error)
		c.JSON(200, gin.H{"provider_calls": upstreamCalls.Load(), "quota": user.Quota, "used_quota": user.UsedQuota, "native_quota": native.Quota, "native_used_quota": native.UsedQuota, "native_requests": native.RequestCount, "native_token_quota": token.RemainQuota, "native_token_used_quota": token.UsedQuota})
	})
	bind := os.Getenv("UNIHUB_PLATFORM_E2E_LISTEN")
	if bind == "" {
		bind = "127.0.0.1:3001"
	}
	listener, e := net.Listen("tcp", bind)
	require.NoError(t, e)
	require.True(t, listener.Addr().(*net.TCPAddr).IP.IsLoopback())
	server := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if os.Getenv("UNIHUB_PLATFORM_E2E_RECOVERY") != "false" {
		stop := adapter.Start(ctx)
		defer stop()
	}
	if ready := os.Getenv("UNIHUB_PLATFORM_E2E_READY"); ready != "" {
		require.True(t, filepath.IsAbs(ready))
		require.NoError(t, os.WriteFile(ready, []byte("http://"+listener.Addr().String()), 0600))
	}
	t.Log("isolated synthetic gateway ready; actual UniHub HTTP; auto-stop 15 minutes")
	select {
	case <-ctx.Done():
	case <-time.After(15 * time.Minute):
	}
}

type platformFixtureCountReader struct {
	io.ReadCloser
	count int
}

func (r *platformFixtureCountReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.count += n
	return n, err
}

type platformFixtureResponseWriter struct {
	gin.ResponseWriter
	errorBody bytes.Buffer
}

func (w *platformFixtureResponseWriter) Write(p []byte) (int, error) {
	if w.Status() >= 400 && w.errorBody.Len()+len(p) <= 4096 {
		_, _ = w.errorBody.Write(p)
	}
	return w.ResponseWriter.Write(p)
}
