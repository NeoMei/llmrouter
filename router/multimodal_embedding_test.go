package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMultimodalEmbeddingHTTP(t *testing.T) {
	enableNativeCompatibility(t)
	scenarios := []string{"canonical", "native", "jpeg", "video", "remote-image", "text-only", "zero", "fixed-zero", "missing-token", "token-model", "channel-group", "missing-space", "space-semantics", "wrong-model", "dimension", "fps-zero", "fusion", "unknown-field", "override", "header-override", "missing-reserve", "bad-reserve", "missing-runtime", "bad-image", "bad-video", "image-limit", "video-limit", "video-pixels", "format-limit", "duplicate-index", "missing-index", "missing-vector", "dimension-output", "null-number", "string-number", "nonfinite", "fusion-output", "missing-usage", "null-usage", "negative-usage", "overflow-usage", "sum-usage", "text-cap", "visual-cap", "wrong-response-model", "response-extension", "pool-rate", "pool-veto", "pool-budget", "pool-unknown", "pool-transport", "cancel", "native-video", "omitted-fps", "audio", "null-fps", "too-many-images", "too-many-contents", "text-bytes", "body-bytes", "image-bytes", "response-bytes", "alias-search", "passthrough", "wrong-wire", "spaceless-legacy", "token-group", "null-dimension", "video-audio-policy", "missing-reserve-video", "video-frozen", "video-concurrent"}
	for _, name := range scenarios {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db, err := gorm.Open(sqlite.Open("file:mm_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}, &model.RelayMediaResource{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
			oldDB, oldLogDB, oldType := model.DB, model.LOG_DB, common.MainDatabaseType()
			oldCache, oldRedis, oldLog, oldRetry, oldTranslate := common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes, common.TranslateMessage
			oldRuntime := controller.RelayMediaRuntime
			controller.RelayMediaRuntime = nil
			model.DB = db
			common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			common.MemoryCacheEnabled = false
			common.RedisEnabled = false
			common.LogConsumeEnabled = true
			common.RetryTimes = 1
			if name == "pool-budget" {
				common.RetryTimes = 0
			}
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			require.NoError(t, i18n.Init())
			t.Cleanup(func() {
				require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
				controller.RelayMediaRuntime = oldRuntime
				model.DB = oldDB
				model.LOG_DB = oldLogDB
				common.SetMainDatabaseType(oldType)
				common.MemoryCacheEnabled = oldCache
				common.RedisEnabled = oldRedis
				common.LogConsumeEnabled = oldLog
				common.RetryTimes = oldRetry
				common.TranslateMessage = oldTranslate
				require.NoError(t, sqlDB.Close())
			})
			ratio_setting.InitRatioSettings()
			oldRatio, oldPrice, oldImage, oldGroup := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString(), ratio_setting.ImageRatio2JSONString(), ratio_setting.GroupRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"mm-billing":2,"qwen3-vl-embedding":9}`))
			require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"mm-billing":3,"qwen3-vl-embedding":9}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1.5}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			if name == "fixed-zero" {
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"mm-billing":0.001}`))
			}
			t.Cleanup(func() {
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio))
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice))
				require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(oldImage))
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroup))
			})
			t.Setenv("QWEN_MM_VISUAL_RESERVE_TOKENS", "1000")
			if name == "missing-reserve" || name == "missing-reserve-video" || name == "text-only" {
				t.Setenv("QWEN_MM_VISUAL_RESERVE_TOKENS", "")
			}
			if name == "bad-reserve" {
				t.Setenv("QWEN_MM_VISUAL_RESERVE_TOKENS", "10000001")
			}
			require.NoError(t, db.Create(&model.User{Id: 501, Username: "mm", Group: "default", Status: common.UserStatusEnabled, Quota: 1_000_000, Setting: `{"billing_preference":"wallet_only"}`}).Error)
			tok := model.Token{Id: 502, UserId: 501, Key: "mmfixture", Name: "mm", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1_000_000}
			if name == "token-group" {
				tok.Group = "private"
			}
			if name == "token-model" {
				tok.ModelLimitsEnabled = true
				tok.ModelLimits = "unrelated"
			}
			require.NoError(t, db.Create(&tok).Error)
			var money, sends, fetches atomic.Int32
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("mm_money", func(tx *gorm.DB) {
				if tx.Statement.Table == "users" || tx.Statement.Table == "tokens" {
					money.Add(1)
				}
			}))
			var img bytes.Buffer
			if name == "jpeg" {
				require.NoError(t, jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil))
			} else {
				require.NoError(t, png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2))))
			}
			mediaData := img.Bytes()
			isVideo := name == "video" || name == "bad-video" || name == "missing-runtime" || name == "video-limit" || name == "video-pixels" || strings.HasPrefix(name, "pool-") || name == "cancel" || name == "native-video" || name == "video-audio-policy" || name == "missing-reserve-video" || name == "video-frozen" || name == "video-concurrent"
			if isVideo {
				mediaData, err = os.ReadFile("../service/relaymedia/testdata/h264.mp4")
				require.NoError(t, err)
			}
			if name == "bad-video" {
				mediaData = []byte("not an MP4")
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fetchCount := fetches.Add(1)
				if name == "video-frozen" && fetchCount > 1 {
					_, _ = w.Write([]byte("mutated source bytes"))
					return
				}
				assert.Empty(t, r.Header.Get("Authorization"))
				_, _ = w.Write(mediaData)
			}))
			defer source.Close()
			settings := system_setting.GetFetchSetting()
			oldFetch := *settings
			*settings = system_setting.FetchSetting{EnableSSRFProtection: true, AllowPrivateIp: true, AllowedPorts: []string{"1-65535"}, ApplyIPFilterForDomain: true}
			t.Cleanup(func() { *settings = oldFetch })
			service.InitHttpClient()
			service.InitTokenEncoders()
			var attemptDeadline time.Time
			var attemptContextCanceled bool
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Next()
				if c.Request.Method != http.MethodPost {
					return
				}
				attemptDeadline, _ = c.Request.Context().Deadline()
				attemptContextCanceled = c.Request.Context().Err() != nil
			})
			SetRelayRouter(engine)
			public := httptest.NewServer(engine)
			defer public.Close()
			if isVideo && name != "missing-runtime" {
				objects, e := objectstore.NewLocal(t.TempDir())
				require.NoError(t, e)
				svc, e := relaymedia.NewService(db, relaymedia.Config{PublicOrigin: public.URL, GrantSecret: bytes.Repeat([]byte{9}, 32), Objects: objects, AllowLoopbackHTTP: true})
				require.NoError(t, e)
				controller.RelayMediaRuntime = &relaymedia.Runtime{Service: svc}
				SetRelayMediaRouter(engine, &controller.RelayMediaHandler{Service: svc})
			}
			var grantURL string
			var firstBody []byte
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := sends.Add(1)
				assert.Equal(t, "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", r.URL.Path)
				if !strings.HasPrefix(name, "pool-") {
					assert.Equal(t, "Bearer supplier-key", r.Header.Get("Authorization"))
				} else if attempt == 1 {
					assert.Equal(t, "Bearer fixture-A", r.Header.Get("Authorization"))
				} else {
					assert.Equal(t, "Bearer fixture-B", r.Header.Get("Authorization"))
				}
				raw, e := io.ReadAll(r.Body)
				require.NoError(t, e)
				if strings.HasPrefix(name, "pool-") {
					if attempt == 1 {
						firstBody = append([]byte(nil), raw...)
					} else if attempt == 2 {
						assert.Equal(t, firstBody, raw, "all attempts send identical frozen inputs")
					}
				}
				var body dto.QwenMultimodalEmbeddingRequest
				require.NoError(t, common.Unmarshal(raw, &body))
				assert.Equal(t, "qwen3-vl-embedding", body.Model)
				require.NotNil(t, body.Parameters.Dimension)
				assert.Equal(t, 1024, *body.Parameters.Dimension)
				require.NotNil(t, body.Parameters.FPS)
				assert.Equal(t, 1.0, *body.Parameters.FPS)
				require.NotNil(t, body.Parameters.EnableFusion)
				assert.False(t, *body.Parameters.EnableFusion)
				var wallet model.User
				require.NoError(t, db.First(&wallet, 501).Error)
				reserve := 201000
				if name == "text-only" {
					reserve = 192000
				}
				if name == "fixed-zero" {
					reserve = 750
				}
				previousCharge := 0
				if attempt == 3 {
					previousCharge = 210
				}
				assert.Equal(t, 1_000_000-reserve-previousCharge, wallet.Quota, "weighted ceiling reserved before actual send")
				if isVideo {
					grantURL = body.Input.Contents[1].Video
					assert.NotEqual(t, source.URL, grantURL)
					if name == "video-concurrent" {
						var wg sync.WaitGroup
						for range 2 {
							wg.Add(1)
							go func() {
								defer wg.Done()
								res, e := http.Get(grantURL)
								assert.NoError(t, e)
								if e != nil {
									return
								}
								actual, e := io.ReadAll(res.Body)
								res.Body.Close()
								assert.NoError(t, e)
								assert.Equal(t, mediaData, actual)
								assert.Equal(t, 200, res.StatusCode)
							}()
						}
						wg.Wait()
					}
					for range 2 {
						res, e := http.Get(grantURL)
						require.NoError(t, e)
						actual, e := io.ReadAll(res.Body)
						res.Body.Close()
						require.NoError(t, e)
						assert.Equal(t, mediaData, actual)
						assert.Equal(t, 200, res.StatusCode)
					}
				}
				if name == "remote-image" {
					assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(mediaData), body.Input.Contents[1].Image)
				}
				if name == "cancel" {
					cancel()
					<-r.Context().Done()
					return
				}
				if strings.HasPrefix(name, "pool-") && attempt == 1 {
					if name == "pool-veto" {
						w.Header().Set("x-should-retry", "false")
					}
					if name == "pool-transport" {
						h := w.(http.Hijacker)
						conn, _, err := h.Hijack()
						require.NoError(t, err)
						conn.Close()
						return
					}
					status := 429
					if name == "pool-unknown" {
						status = 503
					}
					w.WriteHeader(status)
					if name == "pool-unknown" {
						_, _ = w.Write([]byte(`{"code":"InternalError","message":"unknown"}`))
					} else {
						_, _ = w.Write([]byte(`{"code":"Throttling","message":"refused"}`))
					}
					return
				}
				if name == "response-bytes" {
					_, _ = io.WriteString(w, `{"padding":"`)
					block := strings.Repeat("x", 1<<20)
					for range 65 {
						if _, e := io.WriteString(w, block); e != nil {
							return
						}
					}
					return
				}
				embeddings := []map[string]any{}
				for i := len(body.Input.Contents) - 1; i >= 0; i-- {
					vector := make([]float64, 1024)
					vector[0] = float64(i) + 0.25
					embeddings = append(embeddings, map[string]any{"index": i, "embedding": vector, "type": "vl"})
				}
				if name == "duplicate-index" {
					embeddings[0]["index"] = 0
				}
				if name == "missing-index" {
					delete(embeddings[0], "index")
				}
				if name == "missing-vector" {
					embeddings = embeddings[:1]
				}
				if name == "dimension-output" {
					embeddings[0]["embedding"] = []float64{0.25}
				}
				if name == "fusion-output" {
					embeddings[0]["type"] = "fusion"
				}
				usage := map[string]any{"input_tokens": 10, "image_tokens": 20, "total_tokens": 30}
				if name == "text-only" {
					usage["image_tokens"] = 0
					usage["total_tokens"] = 10
				}
				if name == "zero" || name == "fixed-zero" {
					usage = map[string]any{"input_tokens": 0, "image_tokens": 0, "total_tokens": 0}
				}
				switch name {
				case "negative-usage":
					usage["input_tokens"] = -1
				case "overflow-usage":
					usage["input_tokens"] = uint64(18446744073709551615)
				case "sum-usage":
					usage["total_tokens"] = 31
				case "text-cap":
					usage["input_tokens"] = 64001
					usage["total_tokens"] = 64021
				case "visual-cap":
					usage["image_tokens"] = 1001
					usage["total_tokens"] = 1011
				case "null-usage":
					usage["input_tokens"] = nil
				}
				result := map[string]any{"output": map[string]any{"embeddings": embeddings}, "usage": usage}
				if name == "missing-usage" {
					delete(result, "usage")
				}
				if name == "wrong-response-model" {
					result["model"] = "other"
				}
				if name == "response-extension" {
					result["echo"] = source.URL
					embeddings[0]["source"] = source.URL
				}
				response, e := common.Marshal(result)
				require.NoError(t, e)
				if name == "null-number" {
					response = bytes.Replace(response, []byte("1.25"), []byte("null"), 1)
				}
				if name == "string-number" {
					response = bytes.Replace(response, []byte("1.25"), []byte(`"1.25"`), 1)
				}
				if name == "nonfinite" {
					response = bytes.Replace(response, []byte("1.25"), []byte("1e999"), 1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(response)
			}))
			defer supplier.Close()
			ch := model.Channel{Id: 503, Type: constant.ChannelTypeAli, Name: "mm", Key: "supplier-key", BaseURL: &supplier.URL, Models: "qwen3-vl-embedding", Group: "default", Status: common.ChannelStatusEnabled}
			if strings.HasPrefix(name, "pool-") {
				ch.Key = "fixture-A\nfixture-B"
				ch.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling}
			}
			if name == "passthrough" {
				ch.Setting = common.GetPointer(`{"pass_through_body_enabled":true}`)
			}
			if name == "override" {
				ch.ParamOverride = common.GetPointer(`{"fps":0}`)
			}
			if name == "header-override" {
				ch.HeaderOverride = common.GetPointer(`{"Authorization":"other"}`)
			}
			require.NoError(t, db.Create(&ch).Error)
			group := "default"
			if name == "channel-group" {
				group = "other"
			}
			require.NoError(t, db.Create(&model.Ability{ChannelId: 503, Group: group, Model: "qwen3-vl-embedding", Enabled: true}).Error)
			caps := dto.ModelCapabilities{Operations: []string{"multimodal.embeddings.create"}, InputModalities: []string{"text", "image", "video"}, OutputModalities: []string{"embedding"}, EmbeddingSpace: &dto.EmbeddingSpace{Provider: "aliyun", Model: "qwen3-vl-embedding", Version: "operator-v1", Dimensions: 1024, InputSemantics: dto.QwenMultimodalInputSemantics}}
			if name == "missing-space" {
				caps.EmbeddingSpace = nil
			}
			if name == "space-semantics" {
				caps.EmbeddingSpace.InputSemantics = "text"
			}
			if name == "wrong-model" {
				caps.EmbeddingSpace.Model = "text-embedding-v4"
			}
			if name == "video-audio-policy" {
				caps.MediaLimits = &dto.MediaLimits{VideoAudio: common.GetPointer(false), MaxPixels: common.GetPointer(int64(16_000_000)), MaxVideoSeconds: common.GetPointer(1.0), Formats: []string{"mp4"}}
			}
			if name == "image-limit" {
				caps.MediaLimits = &dto.MediaLimits{MaxPixels: common.GetPointer(int64(1))}
			}
			if name == "video-limit" {
				caps.MediaLimits = &dto.MediaLimits{MaxVideoSeconds: common.GetPointer(0.5)}
			}
			if name == "video-pixels" {
				caps.MediaLimits = &dto.MediaLimits{MaxPixels: common.GetPointer(int64(1))}
			}
			if name == "format-limit" {
				caps.MediaLimits = &dto.MediaLimits{Formats: []string{"jpeg"}}
			}
			metadata, e := common.Marshal(caps)
			require.NoError(t, e)
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 504, PublicId: "mm-public", Aliases: `["mm-alias","mm-search-preview"]`, Enabled: true, Metadata: string(metadata)}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 505, RouteId: 504, ChannelId: 503, UpstreamModel: "qwen3-vl-embedding", BillingModel: "mm-billing", PreferredWires: `["qwen_multimodal_embedding"]`, Weight: 1, Enabled: true}).Error)
			if name == "wrong-wire" {
				require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 505).Update("preferred_wires", `["openai_chat"]`).Error)
			}
			if name == "spaceless-legacy" {
				require.NoError(t, db.Where("id = ?", 504).Delete(&model.ClientModelRoute{}).Error)
			}
			content := dto.MultimodalEmbeddingContent{Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.Bytes())}
			if name == "bad-image" {
				content.Image = "data:image/png;base64,bm90aW1hZ2U="
			}
			if name == "remote-image" || name == "missing-reserve" || name == "bad-reserve" {
				content.Image = source.URL
			}
			if isVideo {
				content = dto.MultimodalEmbeddingContent{Video: source.URL}
			}
			input := []dto.MultimodalEmbeddingContent{{Text: "hello"}, content}
			if name == "text-only" {
				input = input[:1]
			}
			request := map[string]any{"model": "mm-alias", "input": input, "fps": 1, "enable_fusion": false}
			path := "/v1/multimodal/embeddings"
			if name == "native" || name == "native-video" {
				path = "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
				request = map[string]any{"model": "mm-alias", "input": map[string]any{"contents": input}, "parameters": map[string]any{"dimension": 1024, "fps": 1, "enable_fusion": false}}
			}
			if name == "alias-search" {
				request["model"] = "mm-search-preview"
			}
			if name == "spaceless-legacy" {
				request["model"] = "qwen3-vl-embedding"
			}
			if name == "null-dimension" {
				request["dimensions"] = nil
			}
			if name == "dimension" {
				request["dimensions"] = 512
			}
			if name == "fps-zero" {
				request["fps"] = 0
			}
			if name == "fusion" {
				request["enable_fusion"] = true
			}
			if name == "unknown-field" {
				request["metadata"] = map[string]any{"dimension": 1}
			}
			if name == "omitted-fps" {
				delete(request, "fps")
			}
			if name == "audio" {
				request["input"] = []any{map[string]any{"audio": "data:audio/wav;base64,AAAA"}}
			}
			if name == "null-fps" {
				request["fps"] = nil
			}
			if name == "too-many-images" {
				request["input"] = []dto.MultimodalEmbeddingContent{content, content, content, content, content, content}
			}
			if name == "too-many-contents" {
				items := make([]dto.MultimodalEmbeddingContent, 21)
				for i := range items {
					items[i].Text = "x"
				}
				request["input"] = items
			}
			if name == "text-bytes" {
				request["input"] = []dto.MultimodalEmbeddingContent{{Text: strings.Repeat("x", 32001)}}
			}
			if name == "body-bytes" {
				request["input"] = []dto.MultimodalEmbeddingContent{{Text: strings.Repeat("x", 70_000_001)}}
			}
			if name == "image-bytes" {
				request["input"] = []dto.MultimodalEmbeddingContent{{Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 10_000_001))}}
			}
			raw, e := common.Marshal(request)
			require.NoError(t, e)
			req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
			req = req.WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer mmfixture")
			if name == "missing-token" {
				req.Header.Del("Authorization")
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			valid := name == "canonical" || name == "native" || name == "jpeg" || name == "video" || name == "remote-image" || name == "text-only" || name == "zero" || name == "fixed-zero" || name == "response-extension" || name == "pool-rate" || name == "pool-unknown" || name == "pool-transport" || name == "native-video" || name == "omitted-fps" || name == "alias-search" || name == "video-audio-policy" || name == "video-frozen" || name == "video-concurrent"
			wantCharge := 0
			if valid {
				require.Equal(t, 200, rec.Code, rec.Body.String())
				wantSends := 1
				if name == "pool-rate" || name == "pool-unknown" || name == "pool-transport" {
					wantSends = 2
				}
				assert.EqualValues(t, wantSends, sends.Load())
				assert.Contains(t, rec.Body.String(), `"embedding_space"`)
				assert.NotContains(t, rec.Body.String(), source.URL)
				wantCharge = 210
				if name == "text-only" {
					wantCharge = 30
				}
				if name == "zero" {
					wantCharge = 0
				}
				if name == "fixed-zero" {
					wantCharge = 750
				}
				if name == "native" || name == "native-video" {
					assert.Contains(t, rec.Body.String(), `"usage":{"image_tokens":20,"input_tokens":10,"total_tokens":30}`)
				} else {
					var out dto.EmbeddingResponse
					require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &out))
					require.Len(t, out.Data, len(input))
					assert.Equal(t, 0.25, out.Data[0].Embedding[0])
					assert.Equal(t, 0, out.Data[0].Index)
					expectedModel := "mm-alias"
					if name == "alias-search" {
						expectedModel = "mm-search-preview"
					}
					assert.Equal(t, expectedModel, out.Model)
				}
			} else {
				require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
				assert.NotContains(t, rec.Body.String(), `"embedding":[`)
				assert.LessOrEqual(t, sends.Load(), int32(1))
				if sends.Load() == 0 {
					assert.Zero(t, money.Load(), "all precharge rejects must precede wallet callbacks")
				}
			}
			require.Eventually(t, func() bool {
				var u model.User
				var tok model.Token
				return db.First(&u, 501).Error == nil && db.First(&tok, 502).Error == nil && u.Quota == 1_000_000-wantCharge && tok.RemainQuota == u.Quota && tok.UsedQuota == wantCharge
			}, time.Second, time.Millisecond)
			var logs []model.Log
			require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
			if valid {
				require.Len(t, logs, 1)
				assert.NotContains(t, logs[0].Other, source.URL)
				assert.NotContains(t, logs[0].Other, "image_output")
				if wantCharge == 210 {
					assert.Contains(t, logs[0].Other, `"visual_input":20`)
				}
			} else {
				assert.Empty(t, logs)
			}
			if name == "pool-rate" {
				oldGrant := grantURL
				second := httptest.NewRecorder()
				secondReq := httptest.NewRequest("POST", path, bytes.NewReader(raw))
				secondReq.Header.Set("Content-Type", "application/json")
				secondReq.Header.Set("Authorization", "Bearer mmfixture")
				engine.ServeHTTP(second, secondReq)
				require.Equal(t, 200, second.Code, second.Body.String())
				assert.EqualValues(t, 3, sends.Load())
				assert.NotEqual(t, oldGrant, grantURL)
				assert.EqualValues(t, 2, fetches.Load())
				var state []model.UpstreamAccountState
				require.NoError(t, db.Find(&state).Error)
				require.NotEmpty(t, state)
				found := false
				for _, s := range state {
					if s.Reason == "rate_limit" {
						found = true
					}
				}
				assert.True(t, found)
			}
			if grantURL != "" {
				assert.False(t, attemptDeadline.IsZero())
				assert.LessOrEqual(t, time.Until(attemptDeadline), 14*time.Minute+30*time.Second)
				assert.Greater(t, time.Until(attemptDeadline), 14*time.Minute)
				assert.True(t, attemptContextCanceled, "request-owned cleanup cancels the attempt context")
				resp, e := http.Get(grantURL)
				require.NoError(t, e)
				resp.Body.Close()
				assert.Equal(t, 404, resp.StatusCode)
				wantFetches := 1
				if name == "pool-rate" {
					wantFetches = 2
				}
				assert.EqualValues(t, wantFetches, fetches.Load())
				assert.NotContains(t, rec.Body.String(), grantURL)
			}
			if isVideo {
				var ready int64
				require.NoError(t, db.Model(&model.RelayMediaResource{}).Where("state = ?", "ready").Count(&ready).Error)
				assert.Zero(t, ready, "success/failure/cancel and fact rejection revoke prepared grants")
			}
			if name == "missing-runtime" || name == "missing-reserve" || name == "bad-reserve" || name == "missing-reserve-video" {
				assert.Zero(t, fetches.Load())
			}
		})
	}
}
