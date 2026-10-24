package controller

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDomesticImageHTTP(t *testing.T) {
	for _, name := range []string{"generation", "header-override", "passthrough", "prompt-extend", "default-n", "plain", "legacy-async", "oversized-price", "usage-null", "usage-negative", "usage-huge", "output-bound", "edit", "six", "one", "pro", "group-rate", "group-funding", "jpeg", "image-bytes", "image-pixels", "duplicate", "null-n", "null-seed", "bad-watermark", "rate", "compatible-rate", "funding", "compatible-funding", "invalid-key", "veto", "unknown", "transport", "cancel", "missing-usage", "mismatch", "missing-image", "bad-url", "bad-dimensions", "zero", "huge", "seven", "size", "prompt", "seed", "parameters", "input", "override", "bad-model", "unpriced", "tiny-price", "free-price", "bad-image", "too-many-images", "remote-image", "stream", "format"} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupTieredRelayBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
			oldLogDB, oldLog, oldCache, oldRetry := model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes
			model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = db, true, false, 1
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			oldTranslator := common.TranslateMessage
			require.NoError(t, i18n.Init())
			t.Cleanup(func() {
				common.TranslateMessage = oldTranslator
				model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = oldLogDB, oldLog, oldCache, oldRetry
			})
			ratio_setting.InitRatioSettings()
			oldPrice, oldRatio := ratio_setting.ModelPrice2JSONString(), ratio_setting.ModelRatio2JSONString()
			price := `{"qwen-image-2.0":0.001,"qwen-image-2.0-pro":0.001,"wan2.1-t2i-turbo":0.001,"unsupported":0.001}`
			if name == "unpriced" {
				price = `{}`
			}
			if name == "oversized-price" {
				price = `{"qwen-image-2.0":1e300}`
			}
			if name == "tiny-price" {
				price = `{"qwen-image-2.0":0.000000000001}`
			}
			if name == "free-price" {
				price = `{"qwen-image-2.0":0}`
			}
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(price))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qwen-image-2.0":1,"unsupported":1}`))
			t.Cleanup(func() {
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice))
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio))
			})
			createTieredRelayWalletFixtures(t, db, 910, 911, 100000, 100000, "imagetoken")
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", 910).Updates(map[string]any{"group": "default", "setting": `{"billing_preference":"wallet_only"}`}).Error)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 911).Updates(map[string]any{"expired_time": -1, "group": "default"}).Error)
			service.InitHttpClient()
			service.InitTokenEncoders()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sends, updates atomic.Int32
			count := 2
			if name == "six" {
				count = 6
			}
			if name == "one" || name == "default-n" {
				count = 1
			}
			actual := "qwen-image-2.0"
			if name == "legacy-async" {
				actual = "wan2.1-t2i-turbo"
			}
			if name == "pro" {
				actual = "qwen-image-2.0-pro"
			}
			if name == "bad-model" {
				actual = "unsupported"
			}
			firstBody := ""
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := sends.Add(1)
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Equal(t, "/api/v1/services/aigc/multimodal-generation/generation", r.URL.Path)
				assert.Empty(t, r.Header.Get("X-DashScope-Async"))
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				if n == 1 {
					firstBody = string(raw)
					assert.Equal(t, "Bearer fixture-A", r.Header.Get("Authorization"))
				} else {
					assert.JSONEq(t, firstBody, string(raw))
					assert.Equal(t, "Bearer fixture-B", r.Header.Get("Authorization"))
				}
				var sent map[string]any
				require.NoError(t, common.Unmarshal(raw, &sent))
				assert.Equal(t, actual, sent["model"])
				params := sent["parameters"].(map[string]any)
				assert.Equal(t, float64(count), params["n"])
				assert.Equal(t, "1024*1024", params["size"])
				assert.Equal(t, false, params["watermark"])
				if name == "prompt-extend" {
					assert.Equal(t, false, params["prompt_extend"])
				}
				if name != "plain" {
					assert.Equal(t, float64(0), params["seed"])
				}
				assert.Contains(t, string(raw), "draw a cat")
				if name == "jpeg" {
					assert.Contains(t, string(raw), "data:image/jpeg;base64,")
				}
				if name == "edit" {
					assert.Contains(t, string(raw), "data:image/png;base64,")
				}
				if n == 1 {
					switch name {
					case "rate", "veto", "group-rate":
						if name == "veto" {
							w.Header().Set("X-Should-Retry", "false")
						}
						w.WriteHeader(429)
						io.WriteString(w, `{"code":"Throttling.AllocationQuota","message":"secret"}`)
						return
					case "compatible-rate":
						w.WriteHeader(429)
						io.WriteString(w, `{"error":{"code":"insufficient_quota","message":"secret"}}`)
						return
					case "funding", "group-funding":
						w.WriteHeader(400)
						io.WriteString(w, `{"code":"Arrearage","message":"secret"}`)
						return
					case "compatible-funding":
						w.WriteHeader(400)
						io.WriteString(w, `{"error":{"code":"Arrearage","message":"secret"}}`)
						return
					case "invalid-key":
						w.WriteHeader(401)
						io.WriteString(w, `{"code":"InvalidApiKey","message":"secret"}`)
						return
					case "unknown":
						w.WriteHeader(503)
						io.WriteString(w, `{"code":"InternalError"}`)
						return
					case "transport":
						conn, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						conn.Close()
						return
					case "cancel":
						cancel()
						<-r.Context().Done()
						return
					}
				}
				if name == "output-bound" {
					io.WriteString(w, strings.Repeat("x", 65537))
					return
				}
				images := make([]map[string]string, count)
				for i := range images {
					images[i] = map[string]string{"image": fmt.Sprintf("https://example.com/result-%d.png", i)}
				}
				if name == "missing-image" {
					images = images[:1]
				}
				if name == "bad-url" {
					images[0]["image"] = "file:///tmp/a"
				}
				output := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": images}, "finish_reason": "stop"}}}
				usage := map[string]any{"image_count": count, "width": 1024, "height": 1024}
				if name == "usage-null" {
					usage["image_count"] = nil
				}
				if name == "usage-negative" {
					usage["image_count"] = -1
				}
				if name == "usage-huge" {
					usage["image_count"] = uint64(18446744073709551615)
				}
				if name == "mismatch" {
					usage["image_count"] = 1
				}
				if name == "bad-dimensions" {
					usage["width"] = -1
				}
				result := map[string]any{"output": output, "usage": usage}
				if name == "missing-usage" {
					delete(result, "usage")
				}
				raw, err = common.Marshal(result)
				require.NoError(t, err)
				w.Write(raw)
			}))
			defer upstream.Close()
			ch := model.Channel{Id: 921, Type: constant.ChannelTypeAli, Name: "image", Key: "fixture-A\nfixture-B", BaseURL: &upstream.URL, Models: actual, Group: "default", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling}}
			if name == "header-override" {
				ch.HeaderOverride = common.GetPointer(`{"X-DashScope-Async":"enable"}`)
			}
			if name == "passthrough" {
				ch.Setting = common.GetPointer(`{"pass_through_body_enabled":true}`)
			}
			if name == "override" {
				ch.ParamOverride = common.GetPointer(`{"parameters.n":6}`)
			}
			require.NoError(t, db.Create(&ch).Error)
			if name == "group-rate" || name == "group-funding" {
				for i, key := range []string{"fixture-A", "fixture-B"} {
					ref, err := model.ResolveUpstreamAccountRef(ctx, db, "channel:921", 921, i, key, "")
					require.NoError(t, err)
					require.NoError(t, model.SetUpstreamAccountQuotaGroup(ctx, db, ref, "shared-funds", time.Now()))
				}
			}

			require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: actual, Enabled: true}).Error)
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 931, PublicId: "image-public", Aliases: `["image-alias"]`, Enabled: true, Metadata: `{"input_modalities":["text","image"],"output_modalities":["image"],"operations":["images.generations.create","images.edits.create"],"streaming":false,"async":false}`}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 932, RouteId: 931, ChannelId: 921, UpstreamModel: actual, BillingModel: actual, PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}).Error)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("image_money", func(tx *gorm.DB) {
				if tx.Statement.Table == "users" || tx.Statement.Table == "tokens" {
					updates.Add(1)
				}
			}))
			t.Cleanup(func() { db.Callback().Update().Remove("image_money") })
			engine := gin.New()
			engine.Use(func(c *gin.Context) { c.Next(); common.CleanupBodyStorage(c) })
			for _, p := range []string{"/v1/images/generations", "/v1/images/edits"} {
				engine.POST(p, middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAIImage) })
			}
			fields := map[string]any{"model": "image-alias", "prompt": "draw a cat", "n": count, "size": "1024x1024", "watermark": false, "seed": 0}
			switch name {
			case "prompt-extend":
				fields["prompt_extend"] = false
			case "default-n":
				delete(fields, "n")
			case "plain":
				delete(fields, "seed")
			case "null-n":
				fields["n"] = nil
			case "null-seed":
				fields["seed"] = nil
			case "bad-watermark":
				fields["watermark"] = "no"
			case "zero":
				fields["n"] = 0
			case "huge":
				fields["n"] = uint64(18446744073686646784)
			case "seven":
				fields["n"] = 7
			case "size":
				fields["size"] = "999999999999999x999999999999999"
			case "prompt":
				fields["prompt"] = strings.Repeat("a", 1301)
			case "seed":
				fields["seed"] = -1
			case "parameters":
				fields["parameters"] = map[string]any{"n": 6}
			case "input":
				fields["input"] = map[string]any{"prompt": "bypass"}
			case "stream":
				fields["stream"] = true
			case "format":
				fields["response_format"] = "b64_json"
			case "remote-image":
				fields["image"] = "https://example.com/reference.png"
			}
			raw, err := common.Marshal(fields)
			require.NoError(t, err)
			path := "/v1/images/generations"
			contentType := "application/json"
			if name == "edit" || name == "jpeg" || name == "image-bytes" || name == "image-pixels" || name == "bad-image" || name == "too-many-images" {
				path = "/v1/images/edits"
				var buf bytes.Buffer
				form := multipart.NewWriter(&buf)
				for key, value := range fields {
					require.NoError(t, form.WriteField(key, fmt.Sprint(value)))
				}
				refs := 1
				if name == "too-many-images" {
					refs = 4
				}
				for i := 0; i < refs; i++ {
					file, err := form.CreateFormFile("image", "reference.png")
					require.NoError(t, err)
					if name == "image-bytes" {
						_, err = file.Write(make([]byte, 10<<20+1))
						require.NoError(t, err)
					} else if name == "image-pixels" {
						require.NoError(t, png.Encode(file, image.NewRGBA(image.Rect(0, 0, 4097, 4096))))
					} else if name == "jpeg" {
						require.NoError(t, jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil))
					} else if name == "bad-image" {
						_, err = file.Write([]byte("invalid"))
						require.NoError(t, err)
					} else {
						require.NoError(t, png.Encode(file, image.NewRGBA(image.Rect(0, 0, 16, 16))))
					}
				}
				require.NoError(t, form.Close())
				raw = buf.Bytes()
				contentType = form.FormDataContentType()
			}
			if name == "duplicate" {
				raw = []byte(`{"model":"image-alias","prompt":"draw a cat","n":2,"n":6}`)
			}
			request := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer imagetoken")
			request.Header.Set("Content-Type", contentType)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			rejected := map[string]bool{"header-override": true, "passthrough": true, "legacy-async": true, "oversized-price": true, "duplicate": true, "null-n": true, "null-seed": true, "bad-watermark": true, "image-bytes": true, "image-pixels": true, "zero": true, "huge": true, "seven": true, "size": true, "prompt": true, "seed": true, "parameters": true, "input": true, "override": true, "bad-model": true, "unpriced": true, "tiny-price": true, "bad-image": true, "too-many-images": true, "remote-image": true, "stream": true, "format": true}[name]
			failed := rejected || map[string]bool{"usage-null": true, "usage-negative": true, "usage-huge": true, "output-bound": true, "group-funding": true, "veto": true, "unknown": true, "transport": true, "cancel": true, "missing-usage": true, "mismatch": true, "missing-image": true, "bad-url": true, "bad-dimensions": true}[name]
			if rejected {
				assert.Zero(t, sends.Load(), recorder.Body.String())
				assert.Zero(t, updates.Load(), "precharge rejection")
			} else if failed {
				assert.Equal(t, int32(1), sends.Load())
				assert.NotContains(t, recorder.Body.String(), "result-0.png")
			} else {
				assert.Equal(t, 200, recorder.Code, recorder.Body.String())
				var result dto.ImageResponse
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
				assert.Len(t, result.Data, count)
			}
			if failed {
				assert.GreaterOrEqual(t, recorder.Code, 400)
			}
			if name == "group-rate" || name == "rate" || name == "compatible-rate" || name == "funding" || name == "compatible-funding" || name == "invalid-key" {
				assert.Equal(t, int32(2), sends.Load())
			}
			assert.NotContains(t, recorder.Body.String(), "secret")
			require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
			quota := 500 * count
			if failed || name == "free-price" {
				quota = 0
			}
			wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
			assert.Equal(t, 100000-quota, wallet)
			assert.Equal(t, 100000-quota, token)
			assert.Equal(t, quota, used)
			var logs []model.Log
			require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
			if failed {
				assert.Empty(t, logs)
			} else {
				require.Len(t, logs, 1)
				assert.Equal(t, quota, logs[0].Quota)
				assert.Zero(t, logs[0].PromptTokens)
				assert.Contains(t, logs[0].Other, `"native_usage_unit":"images"`)
			}
			var states []model.UpstreamAccountState
			require.NoError(t, db.Find(&states).Error)
			if name == "group-rate" || name == "rate" || name == "compatible-rate" || name == "veto" {
				require.Len(t, states, 1)
				assert.Equal(t, "rate_limit", states[0].Reason)
				assert.NotEmpty(t, states[0].Operation)
			}
			if name == "group-funding" || name == "funding" || name == "compatible-funding" {
				require.Len(t, states, 1)
				assert.Equal(t, "quota_exhausted", states[0].Reason)
				assert.Empty(t, states[0].Operation)
				if name == "group-funding" {
					assert.Equal(t, "quota:shared-funds", states[0].Subject)
				}
			}
		})
	}
}
