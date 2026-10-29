package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeEmbeddingManagedGateway(t *testing.T)  { knowledgeManagedGateway(t, "embedding") }
func TestAliEmbeddingV4ManagedGateway(t *testing.T)      { knowledgeManagedGateway(t, "ali-embedding") }
func TestKnowledgeRerankManagedGateway(t *testing.T)     { knowledgeManagedGateway(t, "rerank") }
func TestKnowledgeMultimodalManagedGateway(t *testing.T) { knowledgeManagedGateway(t, "multimodal") }
func knowledgeManagedGateway(t *testing.T, kind string) {
	enableUniHubBoundary(t)
	aliEmbedding := kind == "ali-embedding"
	isEmbedding := kind == "embedding" || aliEmbedding
	variants := []string{"canonical", "native", "zero", "missing", "duplicate", "bad-index", "wrong-grant", "unknown-native", "wrong-policy", "wrong-channel", "text-overcap", "visual-overcap", "video", "remote-image", "multi-key", "key-drift", "space-drift", "crosswire", "changed-body"}
	if isEmbedding {
		variants = []string{"canonical", "token-flat", "token-batch", "zero", "missing", "duplicate", "bad-index", "wrong-grant", "wrong-policy", "wrong-channel", "multi-key", "key-drift", "space-drift", "changed-body"}
	}
	if aliEmbedding {
		variants = []string{"canonical", "disk-cache", "client-cancel", "rotate-key", "zero", "missing", "duplicate", "bad-index", "changed-body", "text-overcap", "settle-unavailable", "receipt-usage", "receipt-digest", "receipt-charged", "receipt-consistent-wrong-charge"}
	}
	if kind == "rerank" {
		variants = append(variants, "native-omitted", "disk-cache", "native-disk-cache", "gate-disabled", "client-cancel", "rotate-key", "settle-unavailable", "receipt-usage", "receipt-digest", "receipt-charged", "receipt-consistent-wrong-charge")
	}
	if kind == "multimodal" {
		variants = append(variants, "frozen-image-canonical-oversize", "frozen-image-native-oversize", "frozen-image-canonical-boundary", "frozen-image-native-boundary", "frozen-image-canonical-one-over", "frozen-image-native-one-over")
	}
	for _, variant := range variants {
		if kind == "rerank" && (strings.HasSuffix(variant, "overcap") || variant == "video" || variant == "remote-image" || variant == "space-drift") {
			continue
		}
		t.Run(variant, func(t *testing.T) {
			if strings.HasSuffix(variant, "disk-cache") {
				previous := common.GetDiskCacheConfig()
				common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: true, ThresholdMB: 0, MaxSizeMB: 100, Path: t.TempDir()})
				t.Cleanup(func() { common.SetDiskCacheConfig(previous) })
			}
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
			nativeEnvelope := variant == "native" || variant == "native-omitted" || variant == "native-disk-cache" || variant == "unknown-native" || strings.Contains(variant, "-native-")
			var imageBytes bytes.Buffer
			if frozenBoundary {
				encoder := png.Encoder{CompressionLevel: png.NoCompression}
				require.NoError(t, encoder.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 256, 256))))
				require.Greater(t, imageBytes.Len(), 65536)
			} else {
				require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
			}
			imageURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes.Bytes())
			if isEmbedding {
				wire, bill, upstreamModel = "embeddings.create", "embedding", "upstream-embed"
				if aliEmbedding {
					upstreamModel = "text-embedding-v4"
				}
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
			var cancelClient context.CancelFunc
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				if strings.HasSuffix(variant, "disk-cache") {
					files, err := os.ReadDir(common.GetDiskCacheDir())
					if !os.IsNotExist(err) {
						require.NoError(t, err)
						assert.Empty(t, files, "managed input must not create disk cache files")
					}
				}
				if variant == "client-cancel" && cancelClient != nil {
					cancelClient()
				}
				var row platformbilling.Call
				require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
				if aliEmbedding || kind == "rerank" {
					assert.NotContains(t, row.MediaSnapshot, `"input":["a","b"]`, "knowledge content must not be persisted in plaintext")
					assert.NotContains(t, row.MediaSnapshot, `"query":"q"`, "knowledge query must not be persisted in plaintext")
					assert.Equal(t, "platform-wire-hmac-v1", row.DigestVersion, "request digests for private input must be keyed")
				}
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
				} else if isEmbedding {
					if aliEmbedding {
						assert.Equal(t, "/compatible-mode/v1/embeddings", r.URL.Path)
					} else {
						assert.Equal(t, "/v1/embeddings", r.URL.Path)
					}
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
				if isEmbedding {
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
					if aliEmbedding {
						vector := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 1024), ",") + "]"
						second := 1
						if variant == "bad-index" {
							second = 0
						}
						data = fmt.Sprintf(`[{"index":0,"embedding":%s},{"index":%d,"embedding":%s}]`, vector, second, vector)
						usage = `{"total_tokens":3}`
						switch variant {
						case "zero":
							usage = `{"total_tokens":0}`
						case "missing":
							usage = `{}`
						case "duplicate":
							usage = `{"total_tokens":3,"total_tokens":3}`
						case "text-overcap":
							usage = `{"total_tokens":1001}`
						}
					}
					_, _ = w.Write([]byte(`{"object":"list","model":"` + upstreamModel + `","data":` + data + `,"usage":` + usage + `}`))
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
			var settledUsage platformbilling.Usage
			var finalDigest string
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
					if aliEmbedding || kind == "rerank" {
						var saved struct{ Ciphertext []byte }
						require.NoError(t, db.Table("platform_knowledge_results").Where("call_id = ?", "call").Take(&saved).Error)
						assert.NotEmpty(t, saved.Ciphertext)
						assert.NotContains(t, string(saved.Ciphertext), `"embedding"`)
						if variant == "settle-unavailable" {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
					}
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
					if aliEmbedding || kind == "rerank" {
						remote.Charged = "2"
						if variant == "zero" {
							remote.Charged = "0"
						}
						if variant == "receipt-consistent-wrong-charge" {
							remote.Charged = "1"
						}
						settledUsage = in.Usage
						digestInput, err := common.Marshal(struct {
							CallID       string                 `json:"call_id"`
							Usage        *platformbilling.Usage `json:"usage"`
							ProviderCost string                 `json:"provider_cost"`
						}{"call", &settledUsage, ""})
						require.NoError(t, err)
						digest := sha256.Sum256(digestInput)
						finalDigest = hex.EncodeToString(digest[:])
					}
					out = remote
				case "/internal/v1/calls/call":
					if !aliEmbedding && kind != "rerank" {
						t.Error("unexpected call receipt")
						w.WriteHeader(404)
						return
					}
					receiptUsage := settledUsage
					receiptDigest := finalDigest
					receiptCall := remote
					if variant == "receipt-usage" {
						receiptUsage = platformbilling.Usage{}
					}
					if variant == "receipt-digest" {
						receiptDigest = strings.Repeat("0", 64)
					}
					if variant == "receipt-charged" {
						receiptCall.Charged = "1"
					}
					out = struct {
						platformbilling.RemoteCall
						ServiceID    string                 `json:"service_id"`
						TaskID       string                 `json:"task_id"`
						Usage        *platformbilling.Usage `json:"usage"`
						FinalDigest  string                 `json:"final_digest"`
						ProviderCost string                 `json:"provider_cost"`
					}{receiptCall, "llmrouter", "task", &receiptUsage, receiptDigest, ""}
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
			if aliEmbedding || kind == "rerank" {
				base = "https://dashscope.aliyuncs.com"
			}
			require.NoError(t, db.Create(&model.Channel{Id: 1, Type: channelType, Key: "key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: upstreamModel, Group: "platform"}).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: upstreamModel, ChannelId: 1, Enabled: true}).Error)
			meta := `{"operations":["rerank.create"],"input_modalities":["text"]}`
			if isEmbedding {
				meta = `{"operations":["embeddings.create"],"input_modalities":["text"],"embedding_space":{"provider":"openai","model":"upstream-embed","version":"v1","dimensions":3,"input_semantics":"text"}}`
			}
			if aliEmbedding {
				meta = `{"operations":["embeddings.create"],"input_modalities":["text"],"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text"}}`
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
			if aliEmbedding || kind == "rerank" {
				client := service.GetHttpClient()
				baseTransport := client.Transport
				client.Transport = aliEmbeddingFixtureTransport{target: up.URL, next: baseTransport}
				t.Cleanup(func() { client.Transport = baseTransport })
			}
			service.InitTokenEncoders()
			p := platformbilling.KnowledgePolicy{ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Version: "knowledge_v1", ProviderReference: "https://fixture.test/policy", PublicModel: "rank", WireOperation: wire, ChannelID: 1, ChannelType: channelType, UpstreamModel: upstreamModel, HardInputTokens: 1000, MaxItemTokens: 100, MaxBatch: 10, RateIDs: map[string]string{"CREDIT/6": "rate"}}
			if isEmbedding {
				p.Space = &dto.EmbeddingSpace{Provider: "openai", Model: upstreamModel, Version: "v1", Dimensions: 3, InputSemantics: "text"}
			}
			if aliEmbedding {
				p.Version, p.Origin = "knowledge_ali_text_v1", base
				p.Space = &dto.EmbeddingSpace{Provider: "aliyun", Model: upstreamModel, Version: "v4", Dimensions: 1024, InputSemantics: "text"}
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
			cfg.AllowAliEmbeddingV4 = aliEmbedding
			cfg.AllowAliRerank = kind == "rerank" && variant != "gate-disabled"
			if aliEmbedding || kind == "rerank" {
				cfg.AliEmbeddingResultKeys = map[string][]byte{"v1": bytes.Repeat([]byte{11}, 32), "v2": bytes.Repeat([]byte{12}, 32)}
				cfg.AliEmbeddingResultActiveKey = "v1"
			}
			a, e := platformbilling.New(db, cfg)
			require.NoError(t, e)
			catalog, e := readTechnicalCatalog(context.Background(), a, "superwagie")
			require.NoError(t, e)
			require.Len(t, catalog.Models, 1)
			if variant == "multi-key" || variant == "wrong-policy" || variant == "wrong-channel" || variant == "space-drift" || variant == "gate-disabled" {
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
				if variant == "native-omitted" {
					body = `{"model":"rank","input":{"query":"q","documents":["a","b"]},"parameters":{"top_n":1}}`
				}
			}
			if kind == "multimodal" {
				path = "/platform/v1/multimodal/embeddings"
				body = `{"model":"rank","input":[{"text":"a"},{"image":"` + imageURI + `"}],"dimensions":256}`
				if nativeEnvelope {
					path = "/platform/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
					body = `{"model":"rank","input":{"contents":[{"text":"a"},{"image":"` + imageURI + `"}]},"parameters":{"dimension":256}}`
				}
			}
			if isEmbedding {
				path = "/platform/v1/embeddings"
				input := `["a","b"]`
				if variant == "token-flat" {
					input = `[0,1,2]`
				}
				if variant == "token-batch" {
					input = `[[0,1],[2,3]]`
				}
				body = `{"model":"rank","input":` + input + `,"dimensions":3}`
				if aliEmbedding {
					body = `{"model":"rank","input":` + input + `,"dimensions":1024}`
				}
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
				if variant == "client-cancel" && sends == 0 {
					ctx, cancel := context.WithCancel(r.Context())
					cancelClient = cancel
					r = r.WithContext(ctx)
				}
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
			denied := (frozenBoundary && !strings.HasSuffix(variant, "-boundary")) || variant == "multi-key" || variant == "wrong-grant" || variant == "unknown-native" || variant == "wrong-policy" || variant == "wrong-channel" || variant == "key-drift" || variant == "space-drift" || variant == "gate-disabled"
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
				if variant == "missing" || variant == "duplicate" || variant == "bad-index" || (aliEmbedding && variant == "zero") || strings.HasSuffix(variant, "overcap") {
					assert.Equal(t, "UNKNOWN", row.Status)
					assert.Equal(t, 0, settles)
					if aliEmbedding {
						assert.GreaterOrEqual(t, w.Code, 400)
						assert.NotContains(t, w.Body.String(), `"embedding"`)
					}
				} else if variant == "client-cancel" {
					assert.Equal(t, "SETTLED", row.Status)
					assert.Equal(t, 1, settles)
					assert.NotContains(t, w.Body.String(), `"embedding"`)
				} else if strings.HasPrefix(variant, "receipt-") {
					assert.Equal(t, "SETTLED", row.Status)
					assert.GreaterOrEqual(t, w.Code, 400)
					assert.NotContains(t, w.Body.String(), `"embedding"`)
				} else if variant == "settle-unavailable" {
					assert.Equal(t, "SETTLE_PENDING", row.Status)
					assert.GreaterOrEqual(t, w.Code, 400)
					assert.NotContains(t, w.Body.String(), `"embedding"`)
				} else {
					assert.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, "SETTLED", row.Status)
					if aliEmbedding {
						var usage metering.Usage
						require.NoError(t, common.UnmarshalUnique([]byte(row.Evidence), &usage))
						assert.Equal(t, dto.BillingUsageSourceAliEmbeddingV4, usage.Source)
					}
					assert.Equal(t, 1, settles)
					var result map[string]any
					require.NoError(t, common.UnmarshalUnique(w.Body.Bytes(), &result))
					if nativeEnvelope {
						assert.Contains(t, result, "output")
					} else {
						if kind == "multimodal" || isEmbedding {
							assert.Contains(t, result, "data")
						} else {
							assert.Contains(t, result, "results")
						}
					}
				}
				if variant == "changed-body" {
					if isEmbedding {
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
				if variant == "rotate-key" {
					require.NoError(t, db.Table("platform_knowledge_result_active").Where("id = ? AND key_id = ?", "active", "v1").Update("key_id", "v2").Error)
					cfg.AliEmbeddingResultActiveKey = "v2"
					rotated, err := platformbilling.New(db, cfg)
					require.NoError(t, err)
					_, err = rotated.AdmitWireOperation(context.Background(), "uh_model_fixture", "task", "call", platformbilling.WireOperation{ID: wire}, "application/json", []byte(body))
					assert.ErrorIs(t, err, platformbilling.ErrDuplicate)
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

// The fixture preserves the verified destination on the real request while
// replacing only the network transport with a controlled local supplier.
type aliEmbeddingFixtureTransport struct {
	target string
	next   http.RoundTripper
}

func (f aliEmbeddingFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "dashscope.aliyuncs.com" {
		return f.next.RoundTrip(r)
	}
	u, err := url.Parse(f.target)
	if err != nil {
		return nil, err
	}
	copy := r.Clone(r.Context())
	u.Path = r.URL.Path
	copy.URL = u
	copy.Host = u.Host
	return f.next.RoundTrip(copy)
}
