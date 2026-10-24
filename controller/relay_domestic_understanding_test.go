package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
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
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDomesticUnderstandingHTTP(t *testing.T) {
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 16, 16))))
	imageURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	wav := make([]byte, 44+16000)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 16000)
	audioURI := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav)
	for _, tc := range []struct {
		name, part, limits string
		responses, denied  bool
		quota              int
	}{
		{name: "image", part: `{"type":"image_url","image_url":{"url":"` + imageURI + `","detail":"low"}}`, limits: `{"max_pixels":256,"formats":["png"]}`, quota: 20},
		{name: "audio", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, limits: `{"max_audio_seconds":1,"formats":["wav"]}`, quota: 34},
		{name: "mixed-visual-audio", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, quota: 34},
		{name: "missing-audio-usage", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "negative-usage", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "overflow-usage", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "responses-invalid-usage", part: `{"type":"input_audio","audio_url":"https://example.test/a","format":"wav"}`, responses: true, denied: true},
		{name: "invalid-usage", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "stream-unsupported", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "passthrough-unsupported", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "override-unsupported", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "responses-passthrough-unsupported", part: `{"type":"input_audio","audio_url":"https://example.test/a","format":"wav"}`, responses: true, denied: true},
		{name: "responses-bad-carrier", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, responses: true, denied: true},
		{name: "responses-video-fps", part: `{"type":"input_video","video_url":"https://example.test/clip","fps":0}`, responses: true, denied: true},
		{name: "fixed-price", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `","format":"wav"}}`, quota: 500},
		{name: "vision-zero", denied: true, part: `{"type":"video_url","video_url":{"url":"https://example.test/clip"},"fps":0}`},
		{name: "video-zero", denied: true, part: `{"type":"video_url","video_url":{"url":"https://example.test/clip","fps":0}}`, quota: 34},
		{name: "video-positive", part: `{"type":"video_url","video_url":{"url":"https://example.test/clip"},"fps":2}`, quota: 20},
		{name: "video", part: `{"type":"video_url","video_url":{"url":"https://example.test/clip"}}`, quota: 34},
		{name: "pool-first-output", part: `{"type":"video_url","video_url":{"url":"https://example.test/clip"},"fps":2}`, denied: true},
		{name: "pool-cancel", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "pool-rate", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, quota: 34},
		{name: "pool-veto", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "responses-audio", part: `{"type":"input_audio","audio_url":"https://example.test/recording","format":"wav"}`, responses: true, quota: 34},
		{name: "responses-inline", part: `{"type":"input_audio","data":"` + audioURI + `","format":"wav"}`, responses: true, limits: `{"max_audio_seconds":1,"formats":["wav"]}`, quota: 34},
		{name: "responses-video", part: `{"type":"input_video","video_url":"https://example.test/clip"}`, responses: true, quota: 34},
		{name: "bad-format", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `","format":"exe"}}`, denied: true},
		{name: "bad-model-media", part: `{"type":"input_audio","input_audio":{"data":"https://example.test/a","format":"wav"}}`, denied: true},
		{name: "audio-output", part: `{"type":"input_audio","input_audio":{"data":"` + audioURI + `"}}`, denied: true},
		{name: "remote-facts", part: `{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}`, limits: `{"max_pixels":256}`, denied: true},
		{name: "video-track-unknown", part: `{"type":"video_url","video_url":{"url":"https://example.test/clip","fps":0}}`, limits: `{"video_audio":false}`, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			oldRatios, oldCompletion, oldAudio, oldPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString(), ratio_setting.AudioRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qwen3.8-omni-flash":1,"qwen-turbo":1,"qwen3.8-max":1}`))
			require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"qwen3.8-omni-flash":2,"qwen3.8-max":2}`))
			require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"qwen3.8-omni-flash":3}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			if tc.name == "fixed-price" {
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"qwen3.8-omni-flash":0.001}`))
			}
			t.Cleanup(func() {
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios))
				require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(oldCompletion))
				require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(oldAudio))
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
			})
			createTieredRelayWalletFixtures(t, db, 910, 911, 100000, 100000, "understanding-token")
			service.InitHttpClient()
			service.InitTokenEncoders()
			var sends atomic.Int32
			var firstBody []byte
			requestContext, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			actual := "qwen3.8-omni-flash"
			if tc.name == "video-positive" || tc.name == "vision-zero" || tc.name == "pool-first-output" {
				actual = "qwen3.8-max"
			}
			if tc.name == "bad-model-media" {
				actual = "qwen-turbo"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := sends.Add(1)
				raw, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				var sent map[string]any
				require.NoError(t, common.Unmarshal(raw, &sent))
				assert.Equal(t, actual, sent["model"])
				if n == 1 {
					firstBody = raw
				} else {
					assert.JSONEq(t, string(firstBody), string(raw))
				}
				assert.Contains(t, string(raw), "What happened and why?")
				var sentParts []any
				if tc.responses {
					assert.Equal(t, "/api/v2/apps/protocols/compatible-mode/v1/responses", r.URL.Path)
					sentParts = sent["input"].([]any)[0].(map[string]any)["content"].([]any)
				} else {
					assert.Equal(t, "/compatible-mode/v1/chat/completions", r.URL.Path)
					sentParts = sent["messages"].([]any)[0].(map[string]any)["content"].([]any)
				}
				expected := map[string]any{}
				require.NoError(t, common.Unmarshal([]byte(tc.part), &expected))
				assert.Equal(t, expected, sentParts[0])
				if tc.name == "pool-first-output" {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, `data: {"id":"chat1","model":"qwen3.8-max","choices":[{"index":0,"delta":{"content":"visible-first"}}]}`+"\n\n")
					w.(http.Flusher).Flush()
					io.WriteString(w, "data: {\"choices\":\"malformed\"}\n\n")
					return
				}
				if tc.name == "pool-cancel" {
					cancelRequest()
					<-r.Context().Done()
					return
				}
				if strings.HasPrefix(tc.name, "pool-") && n == 1 {
					assert.Equal(t, "Bearer fixture-A", r.Header.Get("Authorization"))
					if tc.name == "pool-veto" {
						w.Header().Set("X-Should-Retry", "false")
					}
					w.WriteHeader(429)
					io.WriteString(w, `{"error":{"code":"rate_limit","message":"fixture"}}`)
					return
				}
				if tc.name == "pool-rate" {
					assert.Equal(t, "Bearer fixture-B", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.name == "missing-audio-usage" {
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"INVALID MUST NOT ESCAPE"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
					return
				}
				if tc.name == "negative-usage" || tc.name == "overflow-usage" {
					count := "-1"
					if tc.name == "overflow-usage" {
						count = "2147483648"
					}
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"INVALID MUST NOT ESCAPE"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"audio_tokens":`+count+`}}}`)
					return
				}
				if tc.name == "responses-invalid-usage" {
					io.WriteString(w, `{"id":"resp_understand","object":"response","status":"completed","output":[{"type":"message","id":"msg1","role":"assistant","content":[{"type":"output_text","text":"INVALID MUST NOT ESCAPE"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"text_tokens":3,"audio_tokens":20}}}`)
					return
				}
				if tc.name == "invalid-usage" {
					io.WriteString(w, `{"id":"chat1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"INVALID MUST NOT ESCAPE"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"text_tokens":3,"audio_tokens":20}}}`)
					return
				}
				if tc.responses {
					io.WriteString(w, `{"id":"resp_understand","object":"response","status":"completed","model":"qwen3.8-omni-flash","output":[{"type":"message","id":"msg1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"A sound explains the event.","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"text_tokens":3,"audio_tokens":7},"output_tokens_details":{"text_tokens":2,"reasoning_tokens":3}}}`)
				} else if tc.name == "image" || tc.name == "video-positive" {
					io.WriteString(w, `{"id":"chat1","object":"chat.completion","model":"`+actual+`","choices":[{"index":0,"message":{"role":"assistant","content":"A visible event."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
				} else {
					response := `{"id":"chat1","object":"chat.completion","model":"qwen3.8-omni-flash","choices":[{"index":0,"message":{"role":"assistant","content":"A sound explains the event."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"text_tokens":3,"audio_tokens":7},"completion_tokens_details":{"text_tokens":2,"reasoning_tokens":3}}}`
					if tc.name == "mixed-visual-audio" {
						response = strings.Replace(response, `"text_tokens":3,"audio_tokens":7`, `"text_tokens":2,"audio_tokens":7,"image_tokens":1`, 1)
					}
					if tc.name == "video" || tc.name == "responses-video" {
						response = strings.Replace(response, `"text_tokens":3,"audio_tokens":7`, `"text_tokens":1,"audio_tokens":7,"video_tokens":2`, 1)
					}
					io.WriteString(w, response)
				}
			}))
			defer upstream.Close()
			channel := model.Channel{Id: 921, Type: constant.ChannelTypeAli, Name: "understanding", Key: "fixture", BaseURL: &upstream.URL, Models: actual, Group: "default", Status: common.ChannelStatusEnabled}
			if tc.name == "passthrough-unsupported" || tc.name == "responses-passthrough-unsupported" {
				channel.Setting = common.GetPointer(`{"pass_through_body_enabled":true}`)
			}
			if strings.HasPrefix(tc.name, "pool-") || tc.name == "invalid-usage" {
				channel.Key = "fixture-A\nfixture-B"
				channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling}
			}
			if tc.name == "override-unsupported" {
				channel.ParamOverride = common.GetPointer(`{"modalities":["text","audio"]}`)
			}
			require.NoError(t, db.Create(&channel).Error)
			require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: actual, Enabled: true}).Error)
			metadata := `{"input_modalities":["text","image","audio","video"],"output_modalities":["text"],"operations":["chat.completions.create","responses.create"],"streaming":false`
			if tc.limits != "" {
				metadata += `,"media_limits":` + tc.limits
			}
			metadata += `}`
			if tc.name == "override-unsupported" {
				metadata = ""
			}
			if tc.name == "pool-first-output" {
				metadata = strings.Replace(metadata, `"streaming":false`, `"streaming":true`, 1)
				oldTimeout := constant.StreamingTimeout
				constant.StreamingTimeout = 5
				t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
			}
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 931, PublicId: "domestic-public", Aliases: `["understanding-alias"]`, Enabled: true, Metadata: metadata}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 932, RouteId: 931, ChannelId: 921, UpstreamModel: actual, BillingModel: actual, PreferredWires: `["openai_chat","responses"]`, Weight: 1, Enabled: true}).Error)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserId, 910)
				common.SetContextKey(c, constant.ContextKeyTokenId, 911)
				common.SetContextKey(c, constant.ContextKeyTokenKey, "understanding-token")
				common.SetContextKey(c, constant.ContextKeyUserQuota, 100000)
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
				c.Next()
				common.CleanupBodyStorage(c)
			})
			path := "/v1/chat/completions"
			format := types.RelayFormatOpenAI
			part := tc.part
			if tc.name == "mixed-visual-audio" {
				part += `,{"type":"image_url","image_url":{"url":"` + imageURI + `","detail":"low"}}`
			}
			content := `"messages":[{"role":"user","content":[` + part + `,{"type":"text","text":"What happened and why?"}]}]`
			if tc.responses {
				path = "/v1/responses"
				format = types.RelayFormatOpenAIResponses
				content = `"input":[{"role":"user","content":[` + tc.part + `,{"type":"input_text","text":"What happened and why?"}]}]`
			}
			engine.POST(path, middleware.Distribute(), func(c *gin.Context) {
				if tc.name == "audio" {
					models := listRuntimeClientModels(c, []string{"default"}, constant.ChannelTypeOpenAI)
					require.Len(t, models, 1)
					assert.Equal(t, []string{"text", "image", "audio", "video"}, models[0].InputModalities)
					assert.Equal(t, []string{"text"}, models[0].OutputModalities)
					require.NotNil(t, models[0].Streaming)
					assert.False(t, *models[0].Streaming)
				}
				Relay(c, format)
			})
			body := `{"model":"understanding-alias",` + content + `,"max_tokens":10}`
			if tc.name == "stream-unsupported" || tc.name == "pool-first-output" {
				body = strings.TrimSuffix(body, "}") + `,"stream":true}`
			}
			if tc.name == "audio-output" {
				body = strings.TrimSuffix(body, "}") + `,"modalities":["text","audio"]}`
			}
			request := httptest.NewRequest("POST", path, strings.NewReader(body))
			request = request.WithContext(requestContext)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if tc.denied {
				if tc.name == "pool-first-output" {
					assert.Equal(t, 200, recorder.Code)
					assert.Contains(t, recorder.Body.String(), "visible-first")
				} else {
					require.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
				}
				assert.NotContains(t, recorder.Body.String(), "INVALID MUST NOT ESCAPE")
				expectedSends := 0
				if tc.name == "pool-veto" || tc.name == "pool-first-output" || tc.name == "pool-cancel" || (tc.name == "invalid-usage" || tc.name == "negative-usage" || tc.name == "overflow-usage" || tc.name == "responses-invalid-usage" || tc.name == "missing-audio-usage") {
					expectedSends = 1
				}
				assert.EqualValues(t, expectedSends, sends.Load())
				require.Eventually(t, func() bool {
					w, k, u := tieredRelayQuotaState(t, db, 910, 911)
					return w == 100000 && k == 100000 && u == 0
				}, time.Second, time.Millisecond)
			} else {
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
				expectedSends := 1
				if tc.name == "pool-rate" {
					expectedSends = 2
				}
				assert.EqualValues(t, expectedSends, sends.Load())
				w, k, u := tieredRelayQuotaState(t, db, 910, 911)
				assert.Equal(t, 100000-tc.quota, w)
				assert.Equal(t, 100000-tc.quota, k)
				assert.Equal(t, tc.quota, u)
				assert.Contains(t, recorder.Body.String(), `"total_tokens":15`)
			}
			var logs []model.Log
			require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
			if tc.denied {
				assert.Empty(t, logs)
			} else {
				require.Len(t, logs, 1)
				assert.Equal(t, tc.quota, logs[0].Quota)
				assert.Equal(t, 10, logs[0].PromptTokens)
				assert.Equal(t, 5, logs[0].CompletionTokens)
				if tc.name == "mixed-visual-audio" || tc.name == "video" {
					var other map[string]any
					require.NoError(t, common.Unmarshal([]byte(logs[0].Other), &other))
					assert.EqualValues(t, 3, other["non_audio_input_billing_units"])
					assert.EqualValues(t, 5, other["non_audio_output_billing_units"])
					assert.EqualValues(t, 3, other["reasoning_output"])
					if tc.name == "mixed-visual-audio" {
						assert.EqualValues(t, 2, other["text_input"])
						assert.EqualValues(t, 1, other["image_input"])
					} else {
						assert.EqualValues(t, 1, other["text_input"])
						assert.EqualValues(t, 2, other["video_input"])
					}
				}
			}
			require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
		})
	}
}
