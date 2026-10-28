package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeEmbeddingManagedGateway(t *testing.T)  { knowledgeManagedGateway(t, "embedding") }
func TestKnowledgeRerankManagedGateway(t *testing.T)     { knowledgeManagedGateway(t, "rerank") }
func TestKnowledgeMultimodalManagedGateway(t *testing.T) { knowledgeManagedGateway(t, "multimodal") }
func knowledgeManagedGateway(t *testing.T, kind string) {
	enableUniHubBoundary(t)
	variants := []string{"canonical", "native", "zero", "missing", "duplicate", "bad-index", "wrong-grant", "unknown-native", "wrong-policy", "wrong-channel", "text-overcap", "visual-overcap", "video", "remote-image", "multi-key", "key-drift", "space-drift", "crosswire", "changed-body"}
	if kind == "embedding" {
		variants = []string{"canonical", "token-flat", "token-batch", "zero", "missing", "duplicate", "bad-index", "wrong-grant", "wrong-policy", "wrong-channel", "multi-key", "key-drift", "space-drift", "changed-body"}
	}
	if kind == "multimodal" {
		variants = append(variants, "frozen-image-canonical-oversize", "frozen-image-native-oversize", "frozen-image-canonical-boundary", "frozen-image-native-boundary", "frozen-image-canonical-one-over", "frozen-image-native-one-over")
	}
	for _, variant := range variants {
		if kind == "rerank" && (strings.HasSuffix(variant, "overcap") || variant == "video" || variant == "remote-image" || variant == "space-drift") {
			continue
		}
		t.Run(variant, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			require.NoError(t, i18n.Init())
			db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
			require.NoError(t, e)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.RelayMediaResource{}))
			oldDB, oldLog, oldCache, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
			model.DB = db
			model.LOG_DB = db
			common.MemoryCacheEnabled = true
			common.RedisEnabled = false
			t.Cleanup(func() {
				perfmetrics.DrainRelaySamples()
				model.DB = oldDB
				model.LOG_DB = oldLog
				common.MemoryCacheEnabled = oldCache
				common.RedisEnabled = oldRedis
			})
			wire, bill, upstreamModel := "rerank.create", "rerank", "qwen3-rerank"
			frozenBoundary := strings.HasPrefix(variant, "frozen-image-")
			nativeEnvelope := variant == "native" || variant == "unknown-native" || strings.Contains(variant, "-native-")
			var imageBytes bytes.Buffer
			if frozenBoundary {
				encoder := png.Encoder{CompressionLevel: png.NoCompression}
				require.NoError(t, encoder.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 256, 256))))
				require.Greater(t, imageBytes.Len(), 65536)
			} else {
				require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
			}
			imageURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes.Bytes())
			if kind == "embedding" {
				wire, bill, upstreamModel = "embeddings.create", "embedding", "upstream-embed"
			}
			if kind == "multimodal" {
				wire, bill, upstreamModel = "multimodal.embeddings.create", "multimodal_embedding", "qwen3-vl-embedding"
			}
			bodyLimit := int64(65536)
			var frozenBodySize int
			if frozenBoundary {
				dimensions, fps := 256, float64(1)
				canonical := dto.MultimodalEmbeddingRequest{Model: "rank", Input: []dto.MultimodalEmbeddingContent{{Text: "a"}, {Image: imageURI}}, Dimensions: &dimensions, FPS: &fps}
				raw, err := common.Marshal(canonical)
				require.NoError(t, err)
				frozenBodySize = len(raw)
				native := dto.QwenMultimodalEmbeddingRequest{Model: upstreamModel}
				native.Input.Contents = canonical.Input
				native.Parameters.Dimension, native.Parameters.FPS = &dimensions, &fps
				raw, err = common.Marshal(native)
				require.NoError(t, err)
				frozenBodySize = max(frozenBodySize, len(raw))
				if strings.HasSuffix(variant, "-boundary") || strings.HasSuffix(variant, "-one-over") {
					bodyLimit = int64(frozenBodySize)
				}
				if strings.HasSuffix(variant, "-one-over") {
					bodyLimit--
				}
				t.Logf("decoded PNG=%d frozen supplier body=%d effective bound=%d", imageBytes.Len(), frozenBodySize, bodyLimit)
			}
			fetches := 0
			mediaBytes := imageBytes.Bytes()
			if variant == "video" {
				mediaBytes, e = os.ReadFile("../service/relaymedia/testdata/h264.mp4")
				require.NoError(t, e)
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fetches++
				assert.Empty(t, r.Header.Get("Authorization"))
				if fetches > 1 {
					_, _ = w.Write([]byte("mutated"))
					return
				}
				_, _ = w.Write(mediaBytes)
			}))
			defer source.Close()
			settings := system_setting.GetFetchSetting()
			oldFetch := *settings
			*settings = system_setting.FetchSetting{EnableSSRFProtection: true, AllowPrivateIp: true, AllowedPorts: []string{"1-65535"}, ApplyIPFilterForDomain: true}
			t.Cleanup(func() { *settings = oldFetch })
			oldRuntime := controller.RelayMediaRuntime
			controller.RelayMediaRuntime = nil
			t.Cleanup(func() { controller.RelayMediaRuntime = oldRuntime })
			sends, reserves, settles := 0, 0, 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				var row platformbilling.Call
				require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
				assert.Equal(t, "UNKNOWN", row.Status)
				var n int64
				require.NoError(t, db.Model(&platformbilling.Attempt{}).Where("call_id = ? AND status = ?", "call", "DISPATCHED").Count(&n).Error)
				assert.EqualValues(t, 1, n)
				raw, e := io.ReadAll(r.Body)
				require.NoError(t, e)
				if frozenBoundary {
					assert.Equal(t, frozenBodySize, len(raw))
					assert.EqualValues(t, bodyLimit, len(raw))
				}
				assert.Contains(t, string(raw), `"model":"`+upstreamModel+`"`)
				if kind == "rerank" {
					assert.Contains(t, string(raw), `"return_documents":false`)
					assert.Equal(t, "/api/v1/services/rerank/text-rerank/text-rerank", r.URL.Path)
				} else if kind == "embedding" {
					assert.Equal(t, "/v1/embeddings", r.URL.Path)
				} else {
					if variant == "video" {
						var req dto.QwenMultimodalEmbeddingRequest
						require.NoError(t, common.UnmarshalUnique(raw, &req))
						require.Len(t, req.Input.Contents, 2)
						url := req.Input.Contents[1].Video
						assert.NotEqual(t, source.URL, url)
						resp, e := http.Get(url)
						require.NoError(t, e)
						frozen, e := io.ReadAll(resp.Body)
						resp.Body.Close()
						require.NoError(t, e)
						assert.Equal(t, mediaBytes, frozen)
						assert.Contains(t, row.MediaSnapshot, "BodySHA256")
						assert.Contains(t, row.MediaSnapshot, "Digest")
					} else {
						assert.Contains(t, string(raw), imageURI)
					}
					assert.Equal(t, "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", r.URL.Path)
				}
				usage := `{"total_tokens":3}`
				if variant == "zero" {
					usage = `{"total_tokens":0}`
				}
				if variant == "missing" {
					usage = `{}`
				}
				if variant == "duplicate" {
					usage = `{"total_tokens":3,"total_tokens":3}`
				}
				results := `[{"index":1,"relevance_score":0.9}]`
				if variant == "bad-index" {
					results = `[{"index":2,"relevance_score":0.9}]`
				}
				if kind == "embedding" {
					usage = `{"prompt_tokens":3,"total_tokens":3}`
					if variant == "zero" {
						usage = `{"prompt_tokens":0,"total_tokens":0}`
					}
					if variant == "missing" {
						usage = `{}`
					}
					if variant == "duplicate" {
						usage = `{"prompt_tokens":3,"prompt_tokens":3,"total_tokens":3}`
					}
					data := `[{"index":0,"embedding":[0.1,0.2,0.3]},{"index":1,"embedding":[0.4,0.5,0.6]}]`
					if variant == "token-flat" {
						data = `[{"index":0,"embedding":[0.1,0.2,0.3]}]`
					}
					if variant == "bad-index" {
						data = `[{"index":0,"embedding":[0.1,0.2,0.3]},{"index":0,"embedding":[0.4,0.5,0.6]}]`
					}
					_, _ = w.Write([]byte(`{"object":"list","model":"upstream-embed","data":` + data + `,"usage":` + usage + `}`))
					return
				}
				if kind == "multimodal" {
					usage = `{"input_tokens":2,"image_tokens":3,"total_tokens":5}`
					switch variant {
					case "zero":
						usage = `{"input_tokens":0,"image_tokens":0,"total_tokens":0}`
					case "missing":
						usage = `{"input_tokens":2,"total_tokens":5}`
					case "duplicate":
						usage = `{"input_tokens":2,"input_tokens":2,"image_tokens":3,"total_tokens":5}`
					case "text-overcap":
						usage = `{"input_tokens":1001,"image_tokens":3,"total_tokens":1004}`
					case "visual-overcap":
						usage = `{"input_tokens":2,"image_tokens":2001,"total_tokens":2003}`
					}
					vector := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 256), ",") + "]"
					index := 1
					if variant == "bad-index" {
						index = 2
					}
					response := fmt.Sprintf(`{"output":{"embeddings":[{"index":0,"type":"vl","embedding":%s},{"index":%d,"type":"vl","embedding":%s}]},"usage":%s}`, vector, index, vector, usage)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(response))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"output":{"results":` + results + `},"usage":` + usage + `}`))
			}))
			defer up.Close()
			price := metering.Price{Version: "metered_v2", Operation: bill, Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}}}}
			if kind == "multimodal" {
				price.Terms = []metering.Term{{Key: "text", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "3"}}, {Key: "visual", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "3"}}}
			}
			rate := platformbilling.Rate{ID: "rate", Model: "rank", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
			var remote platformbilling.RemoteCall
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var out any
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					grantWire := wire
					if variant == "wrong-grant" {
						grantWire = "chat.completions.create"
					}
					out = platformbilling.Claims{Operations: map[string][]string{"rank": {grantWire}}, ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "origin", Models: []string{"rank"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
				case "/internal/v1/rates":
					out = []platformbilling.Rate{rate}
				case "/internal/v1/calls/reserve":
					reserves++
					var in map[string]string
					require.NoError(t, common.DecodeJson(r.Body, &in))
					remote = platformbilling.RemoteCall{WireOperation: in["wire_operation"], CallID: "call", ActorID: "actor", AccountID: "account", OriginProductID: "origin", CredentialID: "credential", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
					out = remote
				case "/internal/v1/calls/call/settle":
					settles++
					var in struct {
						Usage platformbilling.Usage `json:"usage"`
					}
					require.NoError(t, common.DecodeJson(r.Body, &in))
					require.NotNil(t, in.Usage.Metered)
					assert.Equal(t, "confirmed", in.Usage.Metered.State)
					want := "3"
					if kind == "multimodal" {
						want = "2"
					}
					if variant == "zero" {
						want = "0"
					}
					assert.Equal(t, want, in.Usage.Metered.Measurements[0].Quantity.Value)
					remote.Status = "SETTLED"
					out = remote
				case "/internal/v1/calls/call/release":
					remote.Status = "RELEASED"
					out = remote
				default:
					t.Errorf("unexpected hub path %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				raw, e := common.Marshal(out)
				require.NoError(t, e)
				_, _ = w.Write(raw)
			}))
			defer hub.Close()
			require.NoError(t, db.Create(&model.User{Id: 1, Username: "platform", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, RequestCount: 3, Group: "platform"}).Error)
			channelType := constant.ChannelTypeAli
			if kind == "embedding" {
				channelType = constant.ChannelTypeOpenAI
			}
			base := up.URL
			require.NoError(t, db.Create(&model.Channel{Id: 1, Type: channelType, Key: "key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: upstreamModel, Group: "platform"}).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: upstreamModel, ChannelId: 1, Enabled: true}).Error)
			meta := `{"operations":["rerank.create"],"input_modalities":["text"]}`
			if kind == "embedding" {
				meta = `{"operations":["embeddings.create"],"input_modalities":["text"],"embedding_space":{"provider":"openai","model":"upstream-embed","version":"v1","dimensions":3,"input_semantics":"text"}}`
			}
			if kind == "multimodal" {
				meta = `{"operations":["multimodal.embeddings.create"],"input_modalities":["text","image","video"],"output_modalities":["embedding"],"embedding_space":{"provider":"aliyun","model":"qwen3-vl-embedding","version":"v1","dimensions":256,"input_semantics":"` + dto.QwenMultimodalInputSemantics + `"},"media_limits":{"max_images":5,"max_pixels":16000000,"max_request_bytes":65536,"max_video_seconds":30,"formats":["png","mp4"]}}`
			}
			if frozenBoundary {
				meta = strings.Replace(meta, `"max_request_bytes":65536`, fmt.Sprintf(`"max_request_bytes":%d`, bodyLimit), 1)
			}
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "rank", DisplayName: "Rank", Client: "all", Enabled: true, Metadata: meta}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: upstreamModel, PreferredWires: func() string {
				if kind == "multimodal" {
					return `["qwen_multimodal_embedding"]`
				}
				return `["openai_chat"]`
			}(), Enabled: true, Weight: 1}).Error)
			oldRatio := ratio_setting.ModelRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio)) })
			model.InitChannelCache()
			service.InitHttpClient()
			service.InitTokenEncoders()
			p := platformbilling.KnowledgePolicy{ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Version: "knowledge_v1", ProviderReference: "https://fixture.test/policy", PublicModel: "rank", WireOperation: wire, ChannelID: 1, ChannelType: channelType, UpstreamModel: upstreamModel, HardInputTokens: 1000, MaxItemTokens: 100, MaxBatch: 10, RateIDs: map[string]string{"CREDIT/6": "rate"}}
			if kind == "embedding" {
				p.Space = &dto.EmbeddingSpace{Provider: "openai", Model: upstreamModel, Version: "v1", Dimensions: 3, InputSemantics: "text"}
			}
			if kind == "multimodal" {
				p.VisualTokens = 2000
				p.MaxBatch = 5
				p.Space = &dto.EmbeddingSpace{Provider: "aliyun", Model: upstreamModel, Version: "v1", Dimensions: 256, InputSemantics: dto.QwenMultimodalInputSemantics}
			}
			if variant == "space-drift" {
				p.Space.Version = "other-space"
			}
			if variant == "wrong-policy" {
				p.UpstreamModel = "uncertified"
			}
			if variant == "wrong-channel" {
				p.ChannelID = 2
			}
			if variant == "multi-key" {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update("channel_info", model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1}).Error)
				model.InitChannelCache()
			}
			cfg := platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: bodyLimit, KnowledgePolicies: map[string]map[string]platformbilling.KnowledgePolicy{wire: {"rank": p}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{bill: {"rank": {Policy: "provider_reported_v1"}}}, OperationRateIDs: map[string]map[string]map[string]string{bill: {"CREDIT/6": {"rank": "rate"}}}}
			a, e := platformbilling.New(db, cfg)
			require.NoError(t, e)
			catalog, e := readTechnicalCatalog(context.Background(), a, "superwagie")
			require.NoError(t, e)
			require.Len(t, catalog.Models, 1)
			if variant == "multi-key" || variant == "wrong-policy" || variant == "wrong-channel" || variant == "space-drift" {
				assert.False(t, catalog.Models[0].Operations[0].Available)
			} else {
				assert.True(t, catalog.Models[0].Operations[0].Available, catalog.Models[0].Operations[0].DisabledReason)
			}
			engine := gin.New()
			engine.Use(middlewareBodyCleanupForTest())
			SetPlatformRouter(engine, a)
			if variant == "video" {
				public := httptest.NewServer(engine)
				defer public.Close()
				objects, e := objectstore.NewLocal(t.TempDir())
				require.NoError(t, e)
				svc, e := relaymedia.NewService(db, relaymedia.Config{PublicOrigin: public.URL, GrantSecret: bytes.Repeat([]byte{9}, 32), Objects: objects, AllowLoopbackHTTP: true})
				require.NoError(t, e)
				controller.RelayMediaRuntime = &relaymedia.Runtime{Service: svc}
				SetRelayMediaRouter(engine, &controller.RelayMediaHandler{Service: svc})
			}

			path := "/platform/v1/rerank"
			body := `{"model":"rank","query":"q","documents":["a","b"],"top_n":1,"return_documents":false}`
			if nativeEnvelope {
				path = "/platform/api/v1/services/rerank/text-rerank/text-rerank"
				body = `{"model":"rank","input":{"query":"q","documents":["a","b"]},"parameters":{"top_n":1,"return_documents":false}}`
			}
			if kind == "multimodal" {
				path = "/platform/v1/multimodal/embeddings"
				body = `{"model":"rank","input":[{"text":"a"},{"image":"` + imageURI + `"}],"dimensions":256}`
				if nativeEnvelope {
					path = "/platform/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
					body = `{"model":"rank","input":{"contents":[{"text":"a"},{"image":"` + imageURI + `"}]},"parameters":{"dimension":256}}`
				}
			}
			if kind == "embedding" {
				path = "/platform/v1/embeddings"
				input := `["a","b"]`
				if variant == "token-flat" {
					input = `[0,1,2]`
				}
				if variant == "token-batch" {
					input = `[[0,1],[2,3]]`
				}
				body = `{"model":"rank","input":` + input + `,"dimensions":3}`
			}
			if variant == "video" {
				body = `{"model":"rank","input":[{"text":"a"},{"video":"` + source.URL + `"}],"dimensions":256}`
			}
			if variant == "remote-image" || frozenBoundary {
				body = strings.Replace(body, imageURI, source.URL, 1)
			}
			if variant == "unknown-native" {
				body = strings.Replace(body, `"parameters":{`, `"parameters":{"callback_url":"https://forbidden.test",`, 1)
			}
			send := func(path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
				r.Header.Set("Authorization", "Bearer uh_model_fixture")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-UniHub-Task-ID", "task")
				r.Header.Set("Idempotency-Key", "call")
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, r)
				return w
			}
			if variant == "key-drift" {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "rotated-key").Error)
				model.InitChannelCache()
			}
			if frozenBoundary {
				require.Less(t, len(body), 256)
			}
			w := send(path, body)
			denied := (frozenBoundary && !strings.HasSuffix(variant, "-boundary")) || variant == "multi-key" || variant == "wrong-grant" || variant == "unknown-native" || variant == "wrong-policy" || variant == "wrong-channel" || variant == "key-drift" || variant == "space-drift"
			if denied {
				assert.GreaterOrEqual(t, w.Code, 400, w.Body.String())
				if frozenBoundary {
					assert.Equal(t, 400, w.Code)
					var count int64
					require.NoError(t, db.Model(&platformbilling.Call{}).Count(&count).Error)
					assert.Zero(t, count)
				}
				assert.Equal(t, 0, sends)
				if variant != "wrong-channel" {
					assert.Equal(t, 0, reserves)
				}
			} else {
				assert.Equal(t, 1, sends)
				var row platformbilling.Call
				require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
				if variant == "missing" || variant == "duplicate" || variant == "bad-index" || strings.HasSuffix(variant, "overcap") {
					assert.Equal(t, "UNKNOWN", row.Status)
					assert.Equal(t, 0, settles)
				} else {
					assert.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, "SETTLED", row.Status)
					assert.Equal(t, 1, settles)
					var result map[string]any
					require.NoError(t, common.UnmarshalUnique(w.Body.Bytes(), &result))
					if nativeEnvelope {
						assert.Contains(t, result, "output")
					} else {
						if kind == "multimodal" || kind == "embedding" {
							assert.Contains(t, result, "data")
						} else {
							assert.Contains(t, result, "results")
						}
					}
				}
				if variant == "changed-body" {
					if kind == "embedding" {
						body = strings.Replace(body, `"a"`, `"changed"`, 1)
					} else if kind == "rerank" {
						body = strings.Replace(body, `"query":"q"`, `"query":"changed"`, 1)
					} else {
						body = strings.Replace(body, `"text":"a"`, `"text":"changed"`, 1)
					}
				}
				if variant == "crosswire" {
					if kind == "rerank" {
						path = "/platform/api/v1/services/rerank/text-rerank/text-rerank"
						body = `{"model":"rank","input":{"query":"q","documents":["a","b"]},"parameters":{"top_n":1,"return_documents":false}}`
					} else {
						path = "/platform/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
						body = `{"model":"rank","input":{"contents":[{"text":"a"},{"image":"` + imageURI + `"}]},"parameters":{"dimension":256}}`
					}
				}
				again := send(path, body)
				assert.Equal(t, 409, again.Code)
				assert.Equal(t, 1, sends)
				if frozenBoundary {
					assert.Equal(t, 1, reserves)
				}
			}
			if variant == "video" || variant == "remote-image" || frozenBoundary {
				assert.Equal(t, 1, fetches)
			}
			var user model.User
			require.NoError(t, db.First(&user, 1).Error)
			assert.Equal(t, 12345, user.Quota)
			assert.Equal(t, 7, user.UsedQuota)
			assert.Equal(t, 3, user.RequestCount)
		})
	}
}
