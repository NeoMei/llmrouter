package router

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPlatformMeteredSyncActualUsageAndReplay(t *testing.T) {
	for _, tc := range []struct{ op, path, body, response, unit, key, policy string }{
		{"ocr", "/ocr/jobs", "", "", "request", "requests", "success_request"},
		{"chat", "/chat/completions", `{"model":"platform-test","messages":[{"role":"user","content":"hello"}],"max_tokens":10}`, `{"id":"chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "token", "input", "sum"},
		{"responses", "/responses", `{"model":"platform-test","input":"hello","max_output_tokens":10}`, `{"id":"resp","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, "token", "input", "sum"},
		{"transcription", "/audio/transcriptions", "", `{"text":"hello"}`, "audio_second", "audio", "server_audio_measurement_v1"},
		{"image_edit", "/images/edits", "", `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`, "image", "images", "successful_outputs_v1"},
		{"embedding", "/embeddings", `{"model":"platform-test","input":"hello"}`, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"platform-test","usage":{"prompt_tokens":1,"total_tokens":1}}`, "token", "input", "sum"},
		{"speech", "/audio/speech", `{"model":"platform-test","input":"hello","voice":"alloy"}`, "audio-bytes", "character", "characters", "unicode_scalar_v1"},
		{"image_generation", "/images/generations", `{"model":"platform-test","prompt":"hello","n":1}`, `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`, "image", "images", "successful_outputs_v1"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			require.NoError(t, i18n.Init())
			gin.SetMode(gin.TestMode)
			sends, settles := 0, 0
			var up *httptest.Server
			up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.op == "ocr" {
					switch r.URL.Path {
					case "/api/v2/ocr/jobs":
						sends++
						_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"provider-owned"}}`))
					case "/api/v2/ocr/jobs/provider-owned":
						fmt.Fprintf(w, `{"code":0,"data":{"state":"done","extractProgress":{"totalPages":1,"extractedPages":1},"resultUrl":{"jsonUrl":%q}}}`, up.URL+"/results")
					case "/results":
						_, _ = w.Write([]byte(`{"result":{"layoutParsingResults":[{"markdown":{"text":"hello","images":{}}}]}}` + "\n"))
					default:
						w.WriteHeader(404)
					}
					return
				}
				sends++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer up.Close()
			terms := []metering.Term{{Key: tc.key, Unit: tc.unit, Price: metering.Rational{Numerator: "1", Denominator: "1"}}}
			if tc.op == "chat" || tc.op == "responses" {
				terms = []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}, {Key: "output", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}
			}
			price := metering.Price{Version: "metered_v2", Operation: tc.op, Policy: tc.policy, Terms: terms}
			rate := platformbilling.Rate{ID: "rate", Model: "platform-test", Asset: "CREDIT/6", Status: "published", Semantics: "metered_v2", Metered: &price}
			var remote platformbilling.RemoteCall
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var out any
				switch r.URL.Path {
				case "/internal/v1/model-credentials/verify":
					out = platformbilling.Claims{ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "origin", Models: []string{"platform-test"}, TaskID: "task", ExpiresAt: time.Now().Add(time.Hour)}
				case "/internal/v1/rates":
					out = []platformbilling.Rate{rate}
				case "/internal/v1/calls/reserve":
					var in map[string]string
					require.NoError(t, common.DecodeJson(r.Body, &in))
					remote = platformbilling.RemoteCall{CallID: "call", ActorID: "actor", AccountID: "account", OriginProductID: "origin", CredentialID: "credential", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
					out = remote
				case "/internal/v1/calls/call/settle":
					settles++
					remote.Status = "SETTLED"
					out = remote
				case "/internal/v1/calls/call/release":
					remote.Status = "RELEASED"
					out = remote
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				b, e := common.Marshal(out)
				require.NoError(t, e)
				_, _ = w.Write(b)
			}))
			defer hub.Close()
			db, e := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/db"), &gorm.Config{})
			require.NoError(t, e)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.OCRJob{}, &model.OCROperation{}, &model.OCRAuditEvent{}, &model.OCRResource{}, &model.OCRResourceGrant{}))
			oldDB, oldLog, oldCache, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
			model.DB = db
			model.LOG_DB = db
			common.MemoryCacheEnabled = true
			common.RedisEnabled = false
			defer func() {
				model.DB = oldDB
				model.LOG_DB = oldLog
				common.MemoryCacheEnabled = oldCache
				common.RedisEnabled = oldRedis
			}()
			user := model.User{Id: 1, Username: "platform", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, RequestCount: 3, Group: "platform"}
			require.NoError(t, db.Create(&user).Error)
			base := up.URL
			channelType := constant.ChannelTypeOpenAI
			if tc.op == "ocr" {
				channelType = constant.ChannelTypePaddleOCR
			}
			require.NoError(t, db.Create(&model.Channel{Id: 1, Type: channelType, Key: "key", Status: common.ChannelStatusEnabled, Name: "platform", BaseURL: &base, Models: "platform-test", Group: "platform"}).Error)
			require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "platform-test", ChannelId: 1, Enabled: true}).Error)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"platform-test":0}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
			model.InitChannelCache()
			service.InitHttpClient()
			service.InitTokenEncoders()
			a, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 65536, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"transcription": {"platform-test": {MaxQuantity: 60, Policy: "server_audio_measurement_v1"}}}, OperationRateIDs: map[string]map[string]map[string]string{"chat": {"CREDIT/6": {"platform-test": "rate"}}}})
			require.NoError(t, e)
			if tc.op == "ocr" {
				objects, e := objectstore.NewLocal(t.TempDir())
				require.NoError(t, e)
				billing := &ocr.Billing{OCRBillingStore: model.NewOCRBillingStore(db, nil)}
				defer billing.Close()
				svc, e := ocr.NewService(db, billing, ocr.Config{PublicOrigin: "https://owned.example", GrantSecret: bytes.Repeat([]byte{3}, 32), Objects: objects, SupplierHTTP: up.Client(), FetchHTTP: up.Client()})
				require.NoError(t, e)
				old := controller.OCRRuntime
				controller.OCRRuntime = &ocr.Runtime{DB: db, Billing: billing, Service: svc}
				defer func() { controller.OCRRuntime = old }()
			}
			engine := gin.New()
			engine.Use(middlewareBodyCleanupForTest())
			SetPlatformRouter(engine, a)
			send := func() *httptest.ResponseRecorder {
				body := []byte(tc.body)
				ct := "application/json"
				if tc.op == "transcription" || tc.op == "image_edit" || tc.op == "ocr" {
					var buf bytes.Buffer
					mw := multipart.NewWriter(&buf)
					require.NoError(t, mw.WriteField("model", "platform-test"))
					field, filename := "image", "image.png"
					data := []byte("image")
					if tc.op == "transcription" {
						field, filename = "file", "audio.wav"
						data = make([]byte, 16044)
						copy(data, "RIFF")
						binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
						copy(data[8:], "WAVEfmt ")
						binary.LittleEndian.PutUint32(data[16:], 16)
						binary.LittleEndian.PutUint16(data[20:], 1)
						binary.LittleEndian.PutUint16(data[22:], 1)
						binary.LittleEndian.PutUint32(data[24:], 8000)
						binary.LittleEndian.PutUint32(data[28:], 16000)
						binary.LittleEndian.PutUint16(data[32:], 2)
						binary.LittleEndian.PutUint16(data[34:], 16)
						copy(data[36:], "data")
						binary.LittleEndian.PutUint32(data[40:], 16000)
					} else if tc.op == "ocr" {
						field, filename = "file", "page.png"
						var img bytes.Buffer
						require.NoError(t, png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2))))
						data = img.Bytes()
					} else {
						require.NoError(t, mw.WriteField("prompt", "hello"))
					}
					part, e := mw.CreateFormFile(field, filename)
					require.NoError(t, e)
					_, e = part.Write(data)
					require.NoError(t, e)
					require.NoError(t, mw.Close())
					body = buf.Bytes()
					ct = mw.FormDataContentType()
				}
				r := httptest.NewRequest("POST", "/platform/v1"+tc.path, bytes.NewReader(body))
				r.Header.Set("Content-Type", ct)
				r.Header.Set("Authorization", "Bearer uh_model_token")
				r.Header.Set("Idempotency-Key", "call")
				r.Header.Set("X-UniHub-Task-ID", "task")
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, r)
				return w
			}
			w := send()
			expected := 200
			if tc.op == "ocr" {
				expected = 202
			}
			require.Equal(t, expected, w.Code, w.Body.String())
			if tc.op == "ocr" {
				r := httptest.NewRequest("GET", "/platform/v1/ocr/jobs/call", nil)
				r.Header.Set("Authorization", "Bearer uh_model_token")
				r.Header.Set("X-UniHub-Task-ID", "task")
				w = httptest.NewRecorder()
				engine.ServeHTTP(w, r)
				require.Equal(t, 200, w.Code, w.Body.String())
			}

			require.Equal(t, 1, sends)
			var call platformbilling.Call
			require.NoError(t, db.First(&call).Error)
			assert.Equal(t, "SETTLED", call.Status, call.Evidence)
			assert.Equal(t, 1, settles)
			w = send()
			assert.Equal(t, 409, w.Code)
			assert.Equal(t, 1, sends)
			var actual model.User
			require.NoError(t, db.First(&actual, 1).Error)
			assert.Equal(t, user.Quota, actual.Quota)
			assert.Equal(t, user.UsedQuota, actual.UsedQuota)
			assert.Equal(t, user.RequestCount, actual.RequestCount)
		})
	}
}
