package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDomesticAudioHTTP(t *testing.T) {
	mp3, err := os.ReadFile("testdata/domestic-audio/silence.mp3")
	require.NoError(t, err)
	scenarios := []struct {
		name string
		tts  bool
	}{
		{"missing-prompt", false}, {"null-completion", false}, {"negative-audio", false}, {"huge-audio", false}, {"bad-seconds", false}, {"missing-seconds", false}, {"json", false}, {"text", false}, {"pool-rate", false}, {"pool-veto", false}, {"bad-wav", false}, {"duration", false}, {"encoded-bound", false}, {"body-bound", false}, {"timestamp", false}, {"duplicate", false}, {"unknown", false}, {"translation", false}, {"bad-model", false}, {"missing-usage", false}, {"zero-usage", false},
		{"custom-voice", true}, {"http-rate", true}, {"stream-rate", true}, {"legacy-alias", true}, {"legacy-chain", true}, {"catalog-chain", true}, {"http-500", true}, {"free-price", true}, {"tiny-price", true}, {"missing-base", true}, {"missing-status", true}, {"mp3", true}, {"wav", true}, {"write-failure", true}, {"stream-format", true}, {"stream-conflict", true}, {"stream", true}, {"aggregate", true}, {"stream-error", true}, {"missing-terminal", true}, {"cancel", true}, {"rate", true}, {"veto", true}, {"invalid-key", true}, {"balance", true}, {"credit", true}, {"ambiguous", true}, {"missing-usage", true}, {"negative-usage", true}, {"huge-usage", true}, {"zero-usage", true}, {"metadata-override", true}, {"bad-codec", true}, {"bad-model", true}, {"unpriced", true}, {"too-long", true}, {"bad-option", true}, {"stream-wav", true}, {"override", true},
	}
	for _, tc := range scenarios {
		kind := "asr"
		if tc.tts {
			kind = "tts"
		}
		t.Run(kind+"/"+tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupTieredRelayBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
			oldLogDB, oldLog, oldCache, oldRetry, oldTimeout := model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes, constant.StreamingTimeout
			model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes, constant.StreamingTimeout = db, true, false, 1, 5
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			oldTranslator := common.TranslateMessage
			require.NoError(t, i18n.Init())
			t.Cleanup(func() {
				common.TranslateMessage = oldTranslator
				model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes, constant.StreamingTimeout = oldLogDB, oldLog, oldCache, oldRetry, oldTimeout
			})
			ratio_setting.InitRatioSettings()
			oldRatio, oldCompletion, oldAudio, oldPrice := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString(), ratio_setting.AudioRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qwen3-asr-flash":1,"speech-2.8-hd":1,"unsupported":1}`))
			require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"qwen3-asr-flash":2}`))
			require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"qwen3-asr-flash":3}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"speech-2.8-hd":0.001}`))
			if tc.name == "free-price" {
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"speech-2.8-hd":0}`))
			}
			if tc.name == "tiny-price" {
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"speech-2.8-hd":0.000000000001}`))
			}
			if tc.name == "unpriced" {
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			}
			t.Cleanup(func() {
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio))
				require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(oldCompletion))
				require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(oldAudio))
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice))
			})
			createTieredRelayWalletFixtures(t, db, 910, 911, 100000, 100000, "audiotoken")
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", 910).Updates(map[string]any{"group": "default", "setting": `{"billing_preference":"wallet_only"}`}).Error)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 911).Updates(map[string]any{"expired_time": -1, "group": "default"}).Error)
			service.InitHttpClient()
			service.InitTokenEncoders()
			var sends, updates atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			actual := "qwen3-asr-flash"
			channelType := constant.ChannelTypeAli
			path := "/v1/audio/transcriptions"
			metadata := `{"input_modalities":["audio"],"output_modalities":["text"],"operations":["audio.transcriptions.create"],"media_limits":{"max_audio_seconds":300,"formats":["wav"]}}`
			codec := "mp3"
			if tc.name == "wav" || tc.name == "stream-wav" {
				codec = "wav"
			}
			stream := tc.name == "stream-rate" || tc.name == "write-failure" || tc.name == "stream-format" || tc.name == "stream-conflict" || tc.name == "stream" || tc.name == "aggregate" || tc.name == "stream-error" || tc.name == "missing-terminal" || tc.name == "cancel" || tc.name == "stream-wav"
			if tc.tts {
				actual = "speech-2.8-hd"
				channelType = constant.ChannelTypeMiniMax
				path = "/v1/audio/speech"
				metadata = `{"input_modalities":["text"],"output_modalities":["audio"],"operations":["audio.speech.create"],"streaming":true}`
			}
			if tc.name == "bad-model" {
				actual = "unsupported"
			}
			if tc.name == "translation" {
				path = "/v1/audio/translations"
			}
			wav := make([]byte, 16044)
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
			audioBytes := mp3
			if codec == "wav" {
				audioBytes = wav
			}
			audioHex := hex.EncodeToString(audioBytes)
			firstBody := ""
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := sends.Add(1)
				raw, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				if n == 1 {
					firstBody = string(raw)
				} else if tc.name != "legacy-alias" || n == 2 {
					assert.JSONEq(t, firstBody, string(raw))
					assert.Equal(t, "Bearer fixture-B", r.Header.Get("Authorization"))
				}
				var sent map[string]any
				require.NoError(t, common.Unmarshal(raw, &sent))
				if !tc.tts {
					assert.Equal(t, actual, sent["model"])
				}
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				if !tc.tts {
					assert.Equal(t, "/compatible-mode/v1/chat/completions", r.URL.Path)
					assert.Contains(t, string(raw), "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(wav))
					if strings.HasPrefix(tc.name, "pool-") && n == 1 {
						if tc.name == "pool-veto" {
							w.Header().Set("X-Should-Retry", "false")
						}
						w.WriteHeader(429)
						io.WriteString(w, `{"error":{"code":"rate_limit"}}`)
						return
					}
					usage := `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"seconds":1,"completion_tokens_details":{"text_tokens":5},"prompt_tokens_details":{"text_tokens":3,"audio_tokens":7}}`
					if tc.name == "missing-usage" {
						usage = `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`
					}
					switch tc.name {
					case "missing-prompt":
						usage = strings.Replace(usage, `"prompt_tokens":10,`, "", 1)
					case "null-completion":
						usage = strings.Replace(usage, `"completion_tokens":5`, `"completion_tokens":null`, 1)
					case "negative-audio":
						usage = strings.Replace(usage, `"audio_tokens":7`, `"audio_tokens":-1`, 1)
					case "huge-audio":
						usage = strings.Replace(usage, `"audio_tokens":7`, `"audio_tokens":2147483648`, 1)
					case "bad-seconds":
						usage = strings.Replace(usage, `"seconds":1`, `"seconds":301`, 1)
					case "missing-seconds":
						usage = strings.Replace(usage, `"seconds":1,`, "", 1)
					}
					if tc.name == "zero-usage" {
						usage = `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"seconds":0,"completion_tokens_details":{"text_tokens":0},"prompt_tokens_details":{"text_tokens":0,"audio_tokens":0}}`
					}
					io.WriteString(w, `{"choices":[{"message":{"content":"real transcript"},"finish_reason":"stop"}],"usage":`+usage+`}`)
					return
				}
				assert.Equal(t, "/v1/t2a_v2", r.URL.Path)
				expectedModel := actual
				if tc.name == "legacy-alias" && n == 4 {
					expectedModel = "speech-2.8-turbo"
				}
				assert.Equal(t, expectedModel, sent["model"])
				if tc.name == "legacy-alias" && n == 3 {
					assert.Equal(t, "Bearer fixture-B", r.Header.Get("Authorization"))
				}
				if tc.name == "legacy-alias" && n == 4 {
					assert.Equal(t, "Bearer fixture-A", r.Header.Get("Authorization"))
				}
				assert.Equal(t, "hello", sent["text"])
				assert.Equal(t, "hex", sent["output_format"])
				voice := sent["voice_setting"].(map[string]any)
				assert.Equal(t, "English_Graceful_Lady", voice["voice_id"])
				assert.Equal(t, float64(0), voice["pitch"])
				assert.Equal(t, false, voice["text_normalization"])
				if (tc.name == "http-rate" || tc.name == "stream-rate" || tc.name == "legacy-alias" || tc.name == "legacy-chain" || tc.name == "catalog-chain") && n == 1 {
					assert.Equal(t, "Bearer fixture-A", r.Header.Get("Authorization"))
					w.WriteHeader(429)
					io.WriteString(w, `{"error":"throttled"}`)
					return
				}
				if tc.name == "http-500" {
					w.WriteHeader(500)
					io.WriteString(w, `{"error":"unknown"}`)
					return
				}
				if tc.name == "missing-base" {
					io.WriteString(w, `{"data":{"audio":"494433","status":2},"extra_info":{"usage_characters":5,"audio_format":"mp3"}}`)
					return
				}
				if tc.name == "missing-status" {
					io.WriteString(w, `{"base_resp":{},"data":{"audio":"494433","status":2},"extra_info":{"usage_characters":5,"audio_format":"mp3"}}`)
					return
				}
				code := 0
				switch tc.name {
				case "rate", "veto":
					code = 1002
				case "invalid-key":
					code = 2049
				case "balance":
					code = 1008
				case "credit":
					code = 2053
				case "ambiguous":
					code = 1004
				}
				if code != 0 && (n == 1 || tc.name == "ambiguous") {
					if tc.name == "veto" {
						w.Header().Set("X-Should-Retry", "false")
					}
					fmt.Fprintf(w, `{"base_resp":{"status_code":%d,"status_msg":"secret-native-error"},"data":null}`, code)
					return
				}
				usage := "5"
				if tc.name == "zero-usage" {
					usage = "0"
				}
				if tc.name == "negative-usage" {
					usage = "-1"
				}
				if tc.name == "huge-usage" {
					usage = "2147483648"
				}
				extra := `{"usage_characters":` + usage + `,"audio_format":"` + codec + `"}`
				if tc.name == "missing-usage" {
					extra = `{"audio_format":"mp3"}`
				}
				if stream {
					assert.Equal(t, true, sent["stream"])
					assert.Equal(t, true, sent["stream_options"].(map[string]any)["exclude_aggregated_audio"])
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"base_resp\":{\"status_code\":0},\"data\":{\"audio\":%q,\"status\":1}}\n\n", audioHex)
					w.(http.Flusher).Flush()
					if tc.name == "cancel" {
						cancel()
						<-r.Context().Done()
						return
					}
					if tc.name == "missing-terminal" {
						return
					}
					if tc.name == "stream-error" {
						io.WriteString(w, "data: {\"base_resp\":{\"status_code\":1002}}\n\n")
						return
					}
					audio := ""
					if tc.name == "aggregate" {
						audio = audioHex
					}
					fmt.Fprintf(w, "data: {\"base_resp\":{\"status_code\":0},\"data\":{\"audio\":%q,\"status\":2},\"extra_info\":%s}\n\n", audio, extra)
					return
				}
				fmt.Fprintf(w, `{"base_resp":{"status_code":0},"data":{"audio":%q,"status":2},"extra_info":%s}`, audioHex, extra)
			}))
			defer upstream.Close()
			ch := model.Channel{Id: 921, Type: channelType, Name: "domestic-audio", Key: "fixture-A\nfixture-B", BaseURL: &upstream.URL, Models: actual, Group: "default", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling}}
			if tc.name == "override" {
				ch.ParamOverride = common.GetPointer(`{"model":"unpriced"}`)
			}
			if tc.name == "legacy-alias" || tc.name == "legacy-chain" || tc.name == "catalog-chain" {
				ch.Models = "audio-alias,audio-other,speech-2.8-turbo"
				ch.ModelMapping = common.GetPointer(`{"audio-alias":"speech-2.8-hd","audio-other":"speech-2.8-hd","speech-2.8-hd":"speech-2.8-hd"}`)
				if tc.name == "legacy-chain" {
					ch.ModelMapping = common.GetPointer(`{"audio-alias":"middle","middle":"speech-2.8-hd","audio-other":"speech-2.8-hd","speech-2.8-hd":"speech-2.8-hd"}`)
				}
				if tc.name == "catalog-chain" {
					ch.ModelMapping = common.GetPointer(`{"audio-alias":"speech-2.8-hd","speech-2.8-hd":"do-not-remap"}`)
				}
			}
			require.NoError(t, db.Create(&ch).Error)
			require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: actual, Enabled: true}).Error)
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 931, PublicId: "audio-public", Aliases: `["audio-alias"]`, Enabled: true, Metadata: metadata}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 932, RouteId: 931, ChannelId: 921, UpstreamModel: actual, BillingModel: actual, PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}).Error)
			if tc.name == "catalog-chain" {
				require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 932).Update("upstream_model", "audio-alias").Error)
			}
			if tc.name == "legacy-alias" || tc.name == "legacy-chain" {
				require.NoError(t, db.Where("id = ?", 931).Delete(&model.ClientModelRoute{}).Error)
				for _, name := range []string{"audio-alias", "audio-other", "speech-2.8-turbo"} {
					require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: name, Enabled: true}).Error)
				}
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"speech-2.8-hd":0.009,"audio-alias":0.001,"audio-other":0.001,"speech-2.8-turbo":0.001}`))
			}
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("audio_money", func(tx *gorm.DB) {
				if tx.Statement.Table == "users" || tx.Statement.Table == "tokens" {
					updates.Add(1)
				}
			}))
			t.Cleanup(func() { db.Callback().Update().Remove("audio_money") })
			engine := gin.New()
			engine.Use(func(c *gin.Context) { c.Next(); common.CleanupBodyStorage(c) })
			engine.POST(path, middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAIAudio) })
			var request *http.Request
			if tc.tts {
				input := "hello"
				if tc.name == "too-long" {
					input = strings.Repeat("a", 10000)
				}
				if tc.name == "bad-codec" {
					codec = "aac"
				}
				voice := "English_Graceful_Lady"
				if tc.name == "custom-voice" {
					voice = "owned-voice"
				}
				opts := `{"voice_setting":{"pitch":0,"text_normalization":false}}`
				if tc.name == "metadata-override" {
					opts = `{"model":"unpriced"}`
				}
				if tc.name == "bad-option" {
					opts = `{"voice_setting":{"pitch":13}}`
				}
				request = httptest.NewRequest("POST", path, strings.NewReader(fmt.Sprintf(`{"model":"audio-alias","input":%q,"voice":%q,"response_format":%q,"stream":%t,"metadata":%s}`, input, voice, codec, stream, opts)))
				request.Header.Set("Content-Type", "application/json")
				if tc.name == "stream-format" || tc.name == "stream-conflict" {
					raw, err := io.ReadAll(request.Body)
					require.NoError(t, err)
					var fields map[string]any
					require.NoError(t, common.Unmarshal(raw, &fields))
					delete(fields, "stream")
					fields["stream_format"] = "audio"
					if tc.name == "stream-conflict" {
						fields["stream"] = false
					}
					raw, err = common.Marshal(fields)
					require.NoError(t, err)
					request = httptest.NewRequest("POST", path, bytes.NewReader(raw))
					request.Header.Set("Content-Type", "application/json")
				}
			} else {
				var body bytes.Buffer
				form := multipart.NewWriter(&body)
				require.NoError(t, form.WriteField("model", "audio-alias"))
				format := "json"
				if tc.name == "text" {
					format = "text"
				}
				if tc.name == "timestamp" {
					format = "verbose_json"
				}
				require.NoError(t, form.WriteField("response_format", format))
				if tc.name == "duplicate" {
					require.NoError(t, form.WriteField("model", "audio-alias"))
				}
				if tc.name == "unknown" {
					require.NoError(t, form.WriteField("timestamp_granularities[]", "word"))
				}
				file, err := form.CreateFormFile("file", "fixture.wav")
				require.NoError(t, err)
				if tc.name == "bad-wav" {
					wav = []byte("not audio")
				}
				if tc.name == "duration" {
					binary.LittleEndian.PutUint32(wav[24:], 1)
					binary.LittleEndian.PutUint32(wav[28:], 2)
				}
				if tc.name == "encoded-bound" {
					header := append([]byte(nil), wav[:44]...)
					wav = make([]byte, 8<<20)
					copy(wav, header)
					binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
					binary.LittleEndian.PutUint32(wav[24:], 44100)
					binary.LittleEndian.PutUint32(wav[28:], 88200)
					binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))
					require.Greater(t, len("data:audio/wav;base64,")+base64.StdEncoding.EncodedLen(len(wav)), helper.MaxInlineMediaBytes)
					fact, parseErr := helper.DecodeMediaFact("audio", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(wav))
					require.NoError(t, parseErr)
					require.Equal(t, "wav", fact.Format, "encoded-size fixture must pass the PCM parser independently")
					require.Greater(t, fact.Seconds, float64(0))
					require.LessOrEqual(t, fact.Seconds, float64(300))
				}
				if tc.name == "body-bound" {
					wav = make([]byte, 11<<20)
				}
				_, err = file.Write(wav)
				require.NoError(t, err)
				require.NoError(t, form.Close())
				if tc.name == "encoded-bound" {
					require.Less(t, body.Len(), helper.MaxInlineMediaBytes)
				}
				request = httptest.NewRequest("POST", path, &body)
				request.Header.Set("Content-Type", form.FormDataContentType())
			}
			request = request.WithContext(ctx)
			request.Header.Set("Authorization", "Bearer audiotoken")
			savedBody, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			request.Body = io.NopCloser(bytes.NewReader(savedBody))
			recorder := httptest.NewRecorder()
			if tc.name == "write-failure" {
				engine.ServeHTTP(&audioHTTPFailWriter{ResponseRecorder: recorder}, request)
			} else {
				engine.ServeHTTP(recorder, request)
			}
			rejected := map[string]bool{"stream-conflict": true, "tiny-price": true, "custom-voice": true, "bad-wav": true, "duration": true, "encoded-bound": true, "body-bound": true, "timestamp": true, "duplicate": true, "unknown": true, "translation": true, "bad-model": true, "metadata-override": true, "bad-codec": true, "unpriced": true, "too-long": true, "bad-option": true, "stream-wav": true, "override": true}[tc.name]
			usageFailure := map[string]bool{"missing-prompt": true, "null-completion": true, "negative-audio": true, "huge-audio": true, "bad-seconds": true, "missing-seconds": true}[tc.name]
			failed := rejected || tc.name == "write-failure" || usageFailure || tc.name == "http-500" || tc.name == "missing-base" || tc.name == "missing-status" || tc.name == "missing-usage" || tc.name == "negative-usage" || tc.name == "huge-usage" || tc.name == "pool-veto" || tc.name == "veto" || tc.name == "ambiguous" || tc.name == "stream-error" || tc.name == "missing-terminal" || tc.name == "cancel"
			quota := 34
			if tc.tts {
				quota = 500
			}
			if failed || tc.name == "free-price" || !tc.tts && tc.name == "zero-usage" {
				quota = 0
			}
			if tc.name == "encoded-bound" {
				assert.Contains(t, recorder.Body.String(), "encoded ASR audio exceeds 10 MiB")
			}
			if rejected {
				assert.Equal(t, 0, int(sends.Load()), recorder.Body.String())
				assert.Zero(t, updates.Load(), "rejection before money")
				assert.GreaterOrEqual(t, recorder.Code, 400)
			} else if !failed {
				assert.Equal(t, 200, recorder.Code, recorder.Body.String())
			}
			if failed && !stream && !rejected {
				assert.GreaterOrEqual(t, recorder.Code, 400)
				assert.Equal(t, int32(1), sends.Load())
				assert.NotContains(t, recorder.Body.String(), "real transcript")
			}
			if !failed {
				if tc.tts {
					assert.Empty(t, recorder.Result().Header.Get("Retry-After"), "successful audio must not commit stale backoff")
				}
				if tc.tts {
					assert.Equal(t, audioBytes, recorder.Body.Bytes())
					assert.Contains(t, recorder.Header().Get("Content-Type"), "audio/")
				} else if tc.name == "text" {
					assert.Equal(t, "real transcript", recorder.Body.String())
				} else {
					assert.JSONEq(t, `{"text":"real transcript"}`, recorder.Body.String())
				}
			}
			if tc.name == "write-failure" {
				assert.Empty(t, recorder.Body.Bytes())
				assert.Equal(t, int32(1), sends.Load())
			}
			if tc.name == "stream-error" || tc.name == "missing-terminal" {
				assert.Equal(t, audioBytes, recorder.Body.Bytes())
				assert.Equal(t, int32(1), sends.Load())
			}
			if tc.name == "http-rate" || tc.name == "pool-rate" || tc.name == "rate" || tc.name == "invalid-key" || tc.name == "balance" || tc.name == "credit" {
				assert.Equal(t, int32(2), sends.Load())
			}
			if tc.name == "veto" || tc.name == "pool-veto" {
				assert.NotEmpty(t, recorder.Result().Header.Get("Retry-After"))
			}
			if tc.name == "veto" || tc.name == "pool-veto" || tc.name == "missing-usage" || tc.name == "negative-usage" || tc.name == "huge-usage" || tc.name == "ambiguous" {
				assert.Equal(t, int32(1), sends.Load())
			}
			var states []model.UpstreamAccountState
			require.NoError(t, db.Find(&states).Error)
			if tc.name == "legacy-alias" || tc.name == "legacy-chain" || tc.name == "catalog-chain" || tc.name == "stream-rate" || tc.name == "http-rate" || tc.name == "rate" || tc.name == "veto" || tc.name == "pool-rate" || tc.name == "pool-veto" {
				require.Len(t, states, 1)
				assert.Equal(t, "rate_limit", states[0].Reason)
				assert.Equal(t, actual, states[0].Model)
			}
			if tc.name == "balance" || tc.name == "credit" {
				require.Len(t, states, 1)
				assert.Equal(t, "quota_exhausted", states[0].Reason)
				assert.Empty(t, states[0].Operation)
			}
			if tc.name == "invalid-key" {
				require.Len(t, states, 1)
				assert.Equal(t, model.UpstreamAccountInvalid, states[0].Kind)
			}
			if tc.name == "ambiguous" {
				assert.Empty(t, states)
			}
			assert.NotContains(t, recorder.Body.String(), "secret-native-error")
			require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
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
				if tc.name == "legacy-alias" || tc.name == "legacy-chain" {
					assert.Equal(t, "audio-alias", logs[0].ModelName)
				}
				if !tc.tts {
					assert.Contains(t, logs[0].Other, `"actual_audio_seconds":`)
				}
				if tc.tts {
					assert.Zero(t, logs[0].PromptTokens)
					assert.Zero(t, logs[0].CompletionTokens)
					assert.Contains(t, logs[0].Other, `"native_usage_unit":"characters"`)
				}
			}
			if tc.name == "legacy-alias" || tc.name == "legacy-chain" || tc.name == "catalog-chain" {
				admin := gin.New()
				admin.GET("/api/channel/:id/account-pool", GetAccountPool)
				admin.POST("/api/channel/:id/account-pool/recover", MutateAccountPool)
				projection := httptest.NewRecorder()
				admin.ServeHTTP(projection, httptest.NewRequest("GET", "/api/channel/921/account-pool?operation=audio.speech.create&model="+actual, nil))
				var result struct {
					Success bool                `json:"success"`
					Data    accountPoolSnapshot `json:"data"`
				}
				require.NoError(t, common.Unmarshal(projection.Body.Bytes(), &result))
				require.True(t, result.Success, projection.Body.String())
				assert.Equal(t, 1, result.Data.Counts["cooling"])
				if tc.name == "legacy-alias" {
					for _, name := range []string{"audio-other", "speech-2.8-turbo"} {
						next := httptest.NewRequest("POST", path, strings.NewReader(strings.Replace(string(savedBody), "audio-alias", name, 1)))
						next.Header.Set("Authorization", "Bearer audiotoken")
						next.Header.Set("Content-Type", "application/json")
						nextResult := httptest.NewRecorder()
						engine.ServeHTTP(nextResult, next)
						require.Equal(t, 200, nextResult.Code, nextResult.Body.String())
					}
					assert.Equal(t, int32(4), sends.Load())
					projection = httptest.NewRecorder()
					admin.ServeHTTP(projection, httptest.NewRequest("GET", "/api/channel/921/account-pool?operation=audio.speech.create&model="+actual, nil))
					require.NoError(t, common.Unmarshal(projection.Body.Bytes(), &result))
					require.True(t, result.Success)
					assert.Equal(t, 1, result.Data.Counts["cooling"], "success on a different model must not clear canonical cooldown")
				}
				var cooling accountPoolRow
				for _, member := range result.Data.Members {
					if member.State == "cooling" {
						cooling = member
					}
				}
				require.NotEmpty(t, cooling.MemberID)
				raw, err := common.Marshal(accountPoolMutation{Operation: "audio.speech.create", Model: actual, PoolID: result.Data.PoolID, MemberID: cooling.MemberID, GenerationID: cooling.GenerationID, TargetScope: "selected"})
				require.NoError(t, err)
				recovered := httptest.NewRecorder()
				recovery := httptest.NewRequest("POST", "/api/channel/921/account-pool/recover", bytes.NewReader(raw))
				recovery.Header.Set("Content-Type", "application/json")
				admin.ServeHTTP(recovered, recovery)
				var recoveryResult struct {
					Success bool `json:"success"`
				}
				require.NoError(t, common.Unmarshal(recovered.Body.Bytes(), &recoveryResult))
				require.True(t, recoveryResult.Success, recovered.Body.String())
				projection = httptest.NewRecorder()
				admin.ServeHTTP(projection, httptest.NewRequest("GET", "/api/channel/921/account-pool?operation=audio.speech.create&model="+actual, nil))
				require.NoError(t, common.Unmarshal(projection.Body.Bytes(), &result))
				require.True(t, result.Success)
				assert.Zero(t, result.Data.Counts["cooling"])
				assert.Equal(t, 2, result.Data.Counts["available"])
			}
			if tc.name == "http-rate" {
				second := httptest.NewRequest("POST", path, bytes.NewReader(savedBody))
				second.Header.Set("Authorization", "Bearer audiotoken")
				second.Header.Set("Content-Type", "application/json")
				secondRecorder := httptest.NewRecorder()
				engine.ServeHTTP(secondRecorder, second)
				assert.Equal(t, 200, secondRecorder.Code, secondRecorder.Body.String())
				assert.Equal(t, int32(3), sends.Load())
				wallet, token, used = tieredRelayQuotaState(t, db, 910, 911)
				assert.Equal(t, 99000, wallet)
				assert.Equal(t, 99000, token)
				assert.Equal(t, 1000, used)
				require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
			}
		})
	}
}

// A real router response writer fails at the first audio write. The controller
// must not attempt to append JSON error framing after the binary write began.
type audioHTTPFailWriter struct{ *httptest.ResponseRecorder }

func (w *audioHTTPFailWriter) Write(p []byte) (int, error) {
	return 0, errors.New("client disconnected")
}
