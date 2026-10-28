package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/channel/nativespeech"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type audioFixtureTransport struct {
	origin    string
	transport http.RoundTripper
}

func (t audioFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, e := url.Parse(t.origin)
	if e != nil {
		return nil, e
	}
	r = r.Clone(r.Context())
	r.URL.Scheme = u.Scheme
	r.URL.Host = u.Host
	r.Host = u.Host
	return t.transport.RoundTrip(r)
}
func audioWAV() []byte {
	b := make([]byte, 16044)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 8000)
	binary.LittleEndian.PutUint32(b[28:], 16000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], 16000)
	return b
}
func audioBody(t *testing.T, kind string) (string, string, []byte) {
	switch kind {
	case "bailian":
		return "/platform/v1/native/bailian/audio/speech", "application/json", []byte(`{"model":"voice","input":{"text":"你好 A","voice":"Cherry","instructions":"Read calmly","optimize_instructions":false}}`)
	case "eleven":
		return "/platform/v1/native/elevenlabs/audio/speech", "application/json", []byte(`{"model":"voice","voice_id":"voice123","text":"你好 A","voice_settings":{"stability":0,"similarity_boost":0,"style":0,"use_speaker_boost":false}}`)
	case "minimax":
		return "/platform/v1/audio/speech", "application/json", []byte(`{"model":"voice","input":"你好 A","voice":"English_Graceful_Lady","speed":1,"response_format":"mp3","metadata":{"audio_setting":{"sample_rate":32000,"bitrate":128000,"channel":1,"force_cbr":false},"voice_setting":{"pitch":0,"latex_read":false}}}`)
	case "openai":
		return "/platform/v1/audio/speech", "application/json", []byte(`{"model":"voice","input":"你好 A","voice":"alloy","speed":1,"response_format":"wav"}`)
	case "qwen-asr":
		dataURI := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(audioWAV())
		payload := "{\"model\":\"voice\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + dataURI + "\"}}]}],\"asr_options\":{\"language\":\"zh\",\"enable_itn\":false}}"
		return "/platform/v1/native/dashscope/audio/transcriptions", "application/json", []byte(payload)
	default:
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		for _, f := range [][2]string{{"model", "voice"}, {"language", "zh"}, {"response_format", "json"}, {"temperature", "0"}} {
			require.NoError(t, w.WriteField(f[0], f[1]))
		}
		p, e := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="short.wav"`}, "Content-Type": {"audio/wav"}})
		require.NoError(t, e)
		_, e = p.Write(audioWAV())
		require.NoError(t, e)
		require.NoError(t, w.Close())
		return "/platform/v1/audio/transcriptions", w.FormDataContentType(), b.Bytes()
	}
}

type audioGatewayFixture struct {
	sent                     chan struct{}
	engine                   *gin.Engine
	adapter                  *platformbilling.Adapter
	db                       *gorm.DB
	sends, reserves, settles int
	price                    metering.Price
	wire                     string
	config                   platformbilling.Config
}

func newAudioGatewayFixture(t *testing.T, kind, variant, hubOrigin, serviceToken, dbPath string) *audioGatewayFixture {
	t.Helper()
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	f := &audioGatewayFixture{sent: make(chan struct{})}
	if dbPath == "" {
		dbPath = t.TempDir() + "/audio.db"
	}
	db, e := gorm.Open(sqlite.Open("file:"+dbPath), &gorm.Config{})
	require.NoError(t, e)
	f.db = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	oldDB, oldLog, oldMemory, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
	model.DB, model.LOG_DB = db, db
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemory, oldRedis
	})
	channel, upstream, origin, policy, wire, bill := constant.ChannelTypeOpenAI, "tts-1", "https://api.openai.com", "unicode_scalar_v1", "audio.speech.create", "speech"
	switch kind {
	case "bailian":
		channel, upstream, origin, policy, wire = constant.ChannelTypeAli, "qwen3-tts-instruct-flash", "https://dashscope.aliyuncs.com", "provider_reported_v1", "bailian.audio.speech.create"
	case "eleven":
		channel, upstream, origin, policy, wire = constant.ChannelTypeCustom, "eleven_multilingual_v2", "https://api.elevenlabs.io", "elevenlabs_character_cost_v1", "elevenlabs.audio.speech.create"
	case "minimax":
		channel, upstream, origin, policy = constant.ChannelTypeMiniMax, "speech-2.8-hd", "https://api.minimax.io", "provider_reported_v1"
	case "asr":
		upstream, policy, wire, bill = "whisper-1", "server_audio_measurement_v1", "audio.transcriptions.create", "transcription"
	case "qwen-asr":
		channel, upstream, origin, policy, wire, bill = constant.ChannelTypeAli, "qwen3-asr-flash", "https://dashscope.aliyuncs.com", "provider_reported_seconds_v1", "dashscope.audio.transcriptions.create", "transcription"
	}
	f.wire = wire
	term := metering.Term{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "3", Denominator: "1"}}
	if kind == "eleven" {
		term.Key, term.Unit, term.ProviderUnit = "provider", "provider_unit", "elevenlabs.character_cost.multilingual_v2.v1"
	}
	if kind == "asr" {
		term.Key, term.Unit = "audio", "audio_second"
	}
	if kind == "qwen-asr" {
		term.Key, term.Unit = "audio", "audio_second"
	}
	f.price = metering.Price{Version: "metered_v2", Operation: bill, Policy: policy, Terms: []metering.Term{term}}
	rate := platformbilling.Rate{ID: "audio-rate", Model: "voice", Asset: "CNY/6", Status: "published", Semantics: "metered_v2", Metered: &f.price}
	var remote platformbilling.RemoteCall
	if hubOrigin == "" {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var out any
			switch r.URL.Path {
			case "/internal/v1/model-credentials/verify":
				ops := map[string][]string{"voice": {wire}}
				if variant == "wronggrant" {
					ops["voice"] = []string{"chat.completions.create"}
				}
				out = platformbilling.Claims{ID: "credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", Models: []string{"voice"}, Operations: ops, TaskID: "audio-task", ExpiresAt: time.Now().Add(time.Hour), Asset: "CNY/6", FundingMode: "prepaid_cash"}
			case "/internal/v1/rates":
				out = []platformbilling.Rate{rate}
			case "/internal/v1/calls/reserve":
				f.reserves++
				var input map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &input))
				remote = platformbilling.RemoteCall{WireOperation: input["wire_operation"], CallID: input["call_id"], ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", CredentialID: "credential", RequestDigest: input["request_digest"], Rate: rate, ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Held: input["amount"], Status: "RESERVED"}
				out = remote
			case "/internal/v1/calls/audio-call/settle":
				f.settles++
				remote.Status = "SETTLED"
				out = remote
			case "/internal/v1/calls/audio-call/release":
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
		t.Cleanup(hub.Close)
		hubOrigin = hub.URL
		serviceToken = "synthetic-service-service-service-token"
	}
	mp3source := audioMP3
	if kind == "minimax" {
		mp3source = audioMiniMaxMP3
	}
	mp3data, e := base64.StdEncoding.DecodeString(mp3source)
	require.NoError(t, e)
	providerRelease := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			assert.Empty(t, r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(audioWAV())
			return
		}
		f.sends++
		if variant == "cancelled" {
			close(f.sent)
			select {
			case <-r.Context().Done():
			case <-providerRelease:
			}
			return
		}
		var call platformbilling.Call
		require.NoError(t, db.Order("created_at DESC").First(&call).Error)
		assert.Equal(t, "UNKNOWN", call.Status)
		var attempts int64
		require.NoError(t, db.Model(&platformbilling.Attempt{}).Where("call_id = ?", call.CallID).Count(&attempts).Error)
		assert.EqualValues(t, 1, attempts)
		if variant == "transport" {
			h, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, e := h.Hijack()
			require.NoError(t, e)
			conn.Close()
			return
		}
		if kind == "asr" {
			require.NoError(t, r.ParseMultipartForm(1<<20))
			assert.Equal(t, "0", r.FormValue("temperature"))
			assert.Equal(t, "zh", r.FormValue("language"))
			file, head, e := r.FormFile("file")
			require.NoError(t, e)
			data, e := io.ReadAll(file)
			require.NoError(t, e)
			file.Close()
			assert.Equal(t, audioWAV(), data)
			assert.Equal(t, "short.wav", head.Filename)
			assert.Equal(t, "audio/wav", head.Header.Get("Content-Type"))
			w.Header().Set("Content-Type", "application/json")
			if variant == "empty" {
				_, _ = w.Write([]byte(`{"text":""}`))
			} else {
				_, _ = w.Write([]byte(`{"text":"你好","duration":99999}`))
			}
			return
		}
		raw, e := io.ReadAll(r.Body)
		require.NoError(t, e)
		var body map[string]any
		require.NoError(t, common.UnmarshalUnique(raw, &body))
		amount := "4"
		if variant == "zero" {
			amount = "0"
		}
		if variant == "overcap" {
			amount = "99999"
		}
		switch kind {
		case "bailian":
			assert.Equal(t, "/api/v1/services/aigc/multimodal-generation/generation", r.URL.Path)
			assert.Equal(t, upstream, body["model"])
			assert.Equal(t, false, body["input"].(map[string]any)["optimize_instructions"])
			usage := `"characters":` + amount
			if variant == "missing" {
				usage = ""
			}
			if variant == "duplicate" {
				usage += `,"characters":4`
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"output":{"finish_reason":"stop","audio":{"url":"https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/audio.wav"}},"usage":{%s}}`, usage)
		case "eleven":
			assert.Equal(t, "/v1/text-to-speech/voice123", r.URL.Path)
			assert.Equal(t, "mp3_44100_128", r.URL.Query().Get("output_format"))
			assert.Equal(t, upstream, body["model_id"])
			assert.Equal(t, false, body["voice_settings"].(map[string]any)["use_speaker_boost"])
			cost := "0.50"
			if variant == "zero" {
				cost = "0"
			}
			if variant == "invalid" {
				cost = "1e2"
			}
			if variant == "overcap" {
				cost = "99999"
			}
			if variant != "missing" {
				w.Header().Set("character-cost", cost)
			}
			if variant == "duplicate" {
				w.Header().Add("character-cost", "0.50")
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			if variant == "truncated" {
				mp3data = mp3data[:100]
			}
			_, _ = w.Write(mp3data)
		case "minimax":
			assert.Equal(t, upstream, body["model"])
			assert.Contains(t, string(raw), `"sample_rate":32000`)
			assert.Contains(t, string(raw), `"bitrate":128000`)
			assert.Contains(t, string(raw), `"channel":1`)
			assert.Contains(t, string(raw), `"force_cbr":false`)
			assert.Contains(t, string(raw), `"pitch":0`)
			assert.Contains(t, string(raw), `"latex_read":false`)
			w.Header().Set("Content-Type", "application/json")
			data := mp3data
			if variant == "truncated" {
				data = data[:100]
			}
			usage := `,"usage_characters":` + amount
			if variant == "missing" {
				usage = ""
			}
			fmt.Fprintf(w, `{"base_resp":{"status_code":0},"data":{"status":2,"audio":"%s"},"extra_info":{"audio_format":"mp3"%s}}`, hex.EncodeToString(data), usage)
		case "qwen-asr":
			assert.Equal(t, "/compatible-mode/v1/chat/completions", r.URL.Path)
			assert.Equal(t, upstream, body["model"])
			messages := body["messages"].([]any)
			require.Len(t, messages, 1)
			message := messages[0].(map[string]any)
			assert.Equal(t, "user", message["role"])
			content := message["content"].([]any)
			require.Len(t, content, 1)
			part := content[0].(map[string]any)
			assert.Equal(t, "input_audio", part["type"])
			data := part["input_audio"].(map[string]any)["data"].(string)
			assert.True(t, strings.HasPrefix(data, "data:audio/wav;base64,"))
			w.Header().Set("Content-Type", "application/json")
			switch variant {
			case "empty":
				_, _ = w.Write([]byte("{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"\"}}],\"usage\":{\"seconds\":1}}"))
			case "missing":
				_, _ = w.Write([]byte("{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"你好\"}}]}"))
			case "zero":
				_, _ = w.Write([]byte("{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"你好\"}}],\"usage\":{\"seconds\":0}}"))
			case "invalid":
				_, _ = w.Write([]byte("{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"你好\"}}],\"usage\":{\"seconds\":\"abc\"}}"))
			default:
				_, _ = w.Write([]byte("{\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"你好\",\"annotations\":[{\"type\":\"audio_info\",\"language\":\"zh\",\"emotion\":\"neutral\"}]}}],\"usage\":{\"seconds\":1}}"))
			}
			return
		default:
			assert.Equal(t, "/v1/audio/speech", r.URL.Path)
			assert.Equal(t, upstream, body["model"])
			w.Header().Set("Content-Type", "audio/wav")
			data := audioWAV()
			if variant == "truncated" {
				data = data[:100]
			}
			_, _ = w.Write(data)
		}
	}))
	t.Cleanup(provider.Close)
	t.Cleanup(func() { close(providerRelease) })
	oldNew := newNativeSpeechProducer
	newNativeSpeechProducer = func(target nativespeech.Target) (*nativespeech.Producer, error) {
		return nativespeech.NewWithTransport(target, audioFixtureTransport{provider.URL, http.DefaultTransport})
	}
	t.Cleanup(func() { newNativeSpeechProducer = oldNew })
	oldQwen := newQwenASRProducer
	newQwenASRProducer = func(origin, credential string) (*nativespeech.Producer, error) {
		return nativespeech.NewQwenASRProducerWithTransport(origin, credential, audioFixtureTransport{provider.URL, http.DefaultTransport})
	}
	t.Cleanup(func() { newQwenASRProducer = oldQwen })
	require.NoError(t, db.Save(&model.User{Id: 1, Username: "platform", AffCode: "fixture", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, RequestCount: 3, Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.User{Id: 2, Username: "native-marker", AffCode: "native-fixture", Status: common.UserStatusEnabled, Quota: 22222, UsedQuota: 11, Group: "default"}).Error)
	require.NoError(t, db.Save(&model.Token{Id: 1, UserId: 2, Key: "synthetic-native-token", Status: common.TokenStatusEnabled, RemainQuota: 54321, UsedQuota: 19, ExpiredTime: -1}).Error)
	require.NoError(t, db.Save(&model.Channel{Id: 1, Type: channel, Key: "key", Status: common.ChannelStatusEnabled, Name: "synthetic", BaseURL: &origin, Models: upstream, Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.Ability{Group: "platform", Model: upstream, ChannelId: 1, Enabled: true}).Error)
	metadata := fmt.Sprintf(`{"operations":["%s"]}`, wire)
	require.NoError(t, db.Save(&model.ClientModelRoute{Id: 1, PublicId: "voice", Client: "all", Enabled: true, Metadata: metadata}).Error)
	require.NoError(t, db.Save(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	oldRatio := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio)) })
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	oldTransport := service.GetHttpClient().Transport
	service.GetHttpClient().Transport = audioFixtureTransport{provider.URL, http.DefaultTransport}
	t.Cleanup(func() { service.GetHttpClient().Transport = oldTransport })
	maxQuantity := 600
	if kind == "qwen-asr" {
		maxQuantity = 300
	}
	p := platformbilling.AudioPolicy{Version: "audio_v1", PublicModel: "voice", WireOperation: wire, ChannelID: 1, ChannelType: channel, UpstreamModel: upstream, ChannelKeySHA256: "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683", Origin: origin, UsagePolicy: policy, MaxQuantity: maxQuantity, RateIDs: map[string]string{"CNY/6": "audio-rate"}}
	if variant == "wrongtarget" {
		p.ChannelKeySHA256 = strings.Repeat("0", 64)
	}
	limit := platformbilling.OperationLimits{Policy: policy, MaxQuantity: maxQuantity}
	if kind == "eleven" {
		limit.ProviderUnit = "elevenlabs.character_cost.multilingual_v2.v1"
		limit.ProviderUnitCap = "10"
	}
	f.config = platformbilling.Config{Enabled: true, Origin: hubOrigin, ServiceToken: serviceToken, PrincipalID: 1, Group: "platform", MaxInputTokens: 20000, MaxOutputTokens: 100, MaxBodyBytes: 1 << 20, AudioPolicies: map[string]map[string]platformbilling.AudioPolicy{wire: {"voice": p}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{bill: {"voice": limit}}, OperationRateIDs: map[string]map[string]map[string]string{bill: {"CNY/6": {"voice": "audio-rate"}}}}
	f.adapter, e = platformbilling.New(db, f.config)
	require.NoError(t, e)
	catalog, e := readTechnicalCatalog(context.Background(), f.adapter, "superwagie")
	require.NoError(t, e)
	require.Len(t, catalog.Models, 1)
	if variant != "wrongtarget" {
		require.True(t, catalog.Models[0].Operations[0].Available, catalog.Models[0].Operations[0].DisabledReason)
	}
	f.engine = gin.New()
	f.engine.Use(middlewareBodyCleanupForTest())
	SetPlatformRouter(f.engine, f.adapter)
	return f
}
func TestAudioGatewayNativeProducerAndJournal(t *testing.T) {
	for _, kind := range []string{"bailian", "eleven", "minimax", "openai", "asr", "qwen-asr"} {
		for _, variant := range []string{"success", "wrongtarget", "wronggrant", "transport", "truncated", "zero", "missing", "overcap", "duplicate", "invalid", "empty"} {
			if kind == "asr" && variant != "success" && variant != "empty" && variant != "wrongtarget" && variant != "wronggrant" {
				continue
			}
			if kind == "qwen-asr" && variant != "success" && variant != "empty" && variant != "wrongtarget" && variant != "wronggrant" && variant != "missing" && variant != "zero" && variant != "invalid" {
				continue
			}
			if variant == "empty" && kind != "asr" && kind != "qwen-asr" {
				continue
			}
			if kind == "openai" && variant != "success" && variant != "truncated" && variant != "wrongtarget" && variant != "wronggrant" && variant != "transport" {
				continue
			}
			if (variant == "invalid" && kind != "eleven") || (variant == "duplicate" && kind == "minimax") || (variant == "truncated" && kind == "bailian") {
				continue
			}
			t.Run(kind+"/"+variant, func(t *testing.T) {
				f := newAudioGatewayFixture(t, kind, variant, "", "", "")
				path, ct, body := audioBody(t, kind)
				send := func(data []byte) *httptest.ResponseRecorder {
					r := httptest.NewRequest("POST", path, bytes.NewReader(data))
					r.Header.Set("Content-Type", ct)
					r.Header.Set("Authorization", "Bearer uh_model_synthetic")
					r.Header.Set("X-UniHub-Task-ID", "audio-task")
					r.Header.Set("Idempotency-Key", "audio-call")
					w := httptest.NewRecorder()
					f.engine.ServeHTTP(w, r)
					return w
				}
				w := send(body)
				if variant == "wrongtarget" || variant == "wronggrant" {
					assert.NotEqual(t, 200, w.Code)
					assert.Zero(t, f.sends)
					assert.Zero(t, f.reserves)
					return
				}
				var row platformbilling.Call
				require.NoError(t, f.db.First(&row, "call_id = ?", "audio-call").Error, "status=%d body=%s", w.Code, w.Body.String())
				if variant == "success" || (variant == "zero" && kind != "qwen-asr") {
					assert.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, "SETTLED", row.Status)
					assert.Equal(t, 1, f.settles)
				} else {
					assert.Equal(t, "UNKNOWN", row.Status)
					assert.Zero(t, f.settles)
				}
				assert.Equal(t, 1, f.sends)
				assert.Equal(t, 409, send(body).Code)
				assert.Equal(t, 1, f.sends)
				// Policy retirement/key rotation cannot turn an old logical call into new work.
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "rotated").Error)
				f.config.AudioPolicies = nil
				restarted, e := platformbilling.New(f.db, f.config)
				require.NoError(t, e)
				f.engine = gin.New()
				f.engine.Use(middlewareBodyCleanupForTest())
				SetPlatformRouter(f.engine, restarted)
				assert.Equal(t, 409, send(body).Code)
				assert.Equal(t, 1, f.sends)
				if kind == "bailian" || kind == "eleven" || kind == "qwen-asr" {
					assert.NotEmpty(t, row.AudioObservation)
					if kind == "eleven" && variant == "success" {
						assert.Contains(t, row.AudioObservation, `"RawQuantity":"0.50"`)
					}
				}
				var user model.User
				require.NoError(t, f.db.First(&user, 1).Error)
				assert.Equal(t, 12345, user.Quota)
				assert.Equal(t, 7, user.UsedQuota)
			})
		}
	}
}

const audioMP3 = "//uQxAAAFFSVIFW3gAshOCMbPUAABZATg4E311NnUQUxmPKxwVId1hHdUhwCYZ2KmHi5j5GZCOmMiZh4WYOBlo0wxMBbBCA1AhguBCDQUCvT6HnOh6Hq9nj0gPGBkePKa94b9+/vv3+KUpr3pr//0pHu/w8MAPgAAeHh4eGADoAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAO94eAQAAAAADOYs5TRjI8WGBkEMY/toBgvAKGTihcbnxcpg0haHOMMASgcne0HoSAkmIWFAYSoTwQEVCwEBVKwM9HA4TsyP4G4hgYU0BnTCKPgZlAAYJAxQkDED0lJ9cLEgAgQGBCBsoNw2/wbVD9gxUFoQpENW//hkUY4QWEFhyRQIoFv/5DhcwuYho5o5pNDnDnfb/8okVIqZEWIsYl0umRe//9XyLGJFS6cJ42Ol01pOtH//WqqpPp7Kq16TLRSSMkUTFSRl///WMCEA2zAjANswfAJBMBiB9jA/QrI4H351NmSPIzH5hRcw//uSxBCD1eIJAB3ygAKeQCAB/g059YLFMHTCTDCFQgYwkEG8MDDAsEamLpExWVDhI+ZjGIrCrtZzPK4TQzka5lEBQrMzoVJlU4mOzIahaFxETd0XdiGohLkZkGxGQTKzrQjvRTHukzO7dWnRnMq59XNdzFUxTy5GK55VnMjstmdpWs7MQ1R+90zSoxDu7O5VRxpmY4ndmIVinMkjotS290jnFDAJQC0wIoCWMENBLTB9gmgxIsdfN2Y4Yzc5R2MxKAKoMItBxzBLQU04lKjiP9NVp0xECEU2cP/DlJT8/8e46y/kaqZfh3n556ocOZp05Z8XJ8enygyq0SiZ5CrfrvDGz2L1W+TMVY5E21IiZjE8ks981ZTpVQ8X08s8iEq5+fSelyBaqiDNI5kX76pmebKUzzf+665p8L0hQqcJIfsRXR+UVqpNKHqsSjAjwJ4wJwDTMDVBSjBZgn0wzcemNV47UDU6R3owvQIlNfpIykZTaq0OIWc1wazE4AStcadzOQ1pd0cBT2omooNBH8qsV7PodCy1GsSaxisrChPO8xL3ZP/7ksQrg9NSBQIP8KbK00Ffwf6g2X286z1JQcydFRdXWTnMjGVxlkS9WJdT81KlqVSERKLMrmdGntchmOWmyehnKqXVZXZGOR5yVdnauYY72KVE3VtiLGazANgCowKsCnME8BSjCTwqcxUwgYOJ592jhqCCAxWML3OhIENWGCNNVhNLpUMvi6MKQPLwMPfyGw/HD4vmlZkCa+pd5t+QrzfDzKdtNVFURVcq1zMTpHBaoiXdwXFuMmYuPqmvihS5d/MKmM5LbVkq7vPh4pTHEtcs/UtlWPpIkYvbVTM8Xd3LD76u4i66dBRFuapo3g9GXl6o8Y1mT858WPoYzdJcZ6VE68MpSLs6y3SxVFjFajAjAJ8wJkDWMDMBUTBWQowwxQfTNSj8mTUGB6cwtEI2NVKMy+WDbirOM1g16ZgETUknVkWCFM91KzlgPTfo5wUrIy1KYUV3M6KuqoID3oRlQqTZXZrJaiWnd2vFTKp2tedNZB7fUxPbqutAgKbl8/st6CVHHg5ydsnhV9/ZGuLsnSK2viYts36sv39wHwspLVcsPvz/+5LESgPSzPUCD/CmyuI9n8K+gAGNSqYBQAemA9giJhA4OCYOIIcGI4EVx1lnCactWlHmUGjhJirYUiYNeGgmF2hUZhaIVCYFaB5mAWgA4kAFpeOI6gcEGO6jN+pBwsmUeei2sNNSJmTN0+dcHyiX+QPmrd7WxiG8rNCg6GXWLruN4i+akctsmOn6npa1Gy7enNX1UTMLExSTaP27UL3P/ST8jSvNDm8pHjHxNtTN3F06rML2fz/0cz1XNtcjWepWX4bhDKtJkdRcfzSQPPnPAgAgQggQgMIUJAylJrjJ6NYMx7xQxQBlTDvMEPH8FgRApHQmGOZXBBRnwoNGC4EiYK4BcPGHsHEYUgIxgjADmAIBDF6Q6t5NXRV1mHDN6xmZuImhExigUYsJhcKMXGO9w4DgwDD7bxsxkaCgSYcHmHCfP/nH5VO26QiAQQgBgYAYGDKUpE/+X/6sBeNB9a6ABMQv67UPO1yVd/9ax9kiYbE3XZZGJZGoi1lnMBM6Xd+/73//5HU5K4bl7+OxFH1Zyw2JP85Uplsp/9c1/e/33Ld+//uSxGkAJ64vFtntgAFui2ELnjAGLuRDksh+ntyzmX/qmjUzGYZmo1+v/9b///n//6pLFPG7cYjE5D8btxikf6ZjMM1qaVY83+ONam///9f//////////////Yp88+bzqUlinp7cspLEvp7f/////8qq0sZrU0aq0tLlll//urSiAg5IgEoBOJMdo9J4j6BTHCX4eo6o6dQ1l0oCAgKxm+MZ/G/h6hQGCsRHhEegrDThEeER7WGvwaiKVOiUNQaUDUqdKu/8S8RHiwdgqPBVwNBwGgaWCqwV4ieoGkxBTUU0LjBVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVQ=="

const audioMiniMaxMP3 = "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"

func TestAudioCancellationAfterSupplierSendRetainsHold(t *testing.T) {
	for _, kind := range []string{"eleven", "openai"} {
		t.Run(kind, func(t *testing.T) {
			f := newAudioGatewayFixture(t, kind, "cancelled", "", "", "")
			path, ct, body := audioBody(t, kind)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest("POST", path, bytes.NewReader(body)).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer uh_model_synthetic")
			request.Header.Set("X-UniHub-Task-ID", "audio-task")
			request.Header.Set("Idempotency-Key", "audio-call")
			request.Header.Set("Content-Type", ct)
			done := make(chan struct{})
			go func() { defer close(done); f.engine.ServeHTTP(httptest.NewRecorder(), request) }()
			select {
			case <-f.sent:
			case <-time.After(5 * time.Second):
				t.Fatal("supplier request not reached")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not finish")
			}
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", "audio-call").Error)
			assert.Equal(t, "UNKNOWN", row.Status)
			assert.Equal(t, 1, f.sends)
			assert.Zero(t, f.settles)
		})
	}
}

func TestMiniMaxAdmissionMatchesNativeProducerBounds(t *testing.T) {
	for _, name := range []string{"oversize", "unsupported-empty-instructions", "exact-limit"} {
		t.Run(name, func(t *testing.T) {
			f := newAudioGatewayFixture(t, "minimax", "success", "", "", "")
			path, ct, body := audioBody(t, "minimax")
			switch name {
			case "oversize":
				body = append(body, bytes.Repeat([]byte(" "), (128<<10)+1-len(body))...)
			case "unsupported-empty-instructions":
				body = append(body[:len(body)-1], []byte(`,"instructions":""}`)...)
			case "exact-limit":
				body = append(body, bytes.Repeat([]byte(" "), (128<<10)-len(body))...)
			}
			request := httptest.NewRequest("POST", path, bytes.NewReader(body))
			request.Header.Set("Content-Type", ct)
			request.Header.Set("Authorization", "Bearer uh_model_synthetic")
			request.Header.Set("X-UniHub-Task-ID", "audio-task")
			request.Header.Set("Idempotency-Key", "audio-call")
			response := httptest.NewRecorder()
			f.engine.ServeHTTP(response, request)
			if name == "exact-limit" {
				assert.Equal(t, 200, response.Code, response.Body.String())
				assert.Equal(t, 1, f.reserves)
				assert.Equal(t, 1, f.sends)
				assert.Equal(t, 1, f.settles)
			} else {
				assert.Equal(t, 400, response.Code, response.Body.String())
				assert.Zero(t, f.reserves)
				assert.Zero(t, f.sends)
				var calls int64
				require.NoError(t, f.db.Model(&platformbilling.Call{}).Count(&calls).Error)
				assert.Zero(t, calls)
			}
		})
	}
	t.Run("advertised-producer-and-private-body-limits", func(t *testing.T) {
		f := newAudioGatewayFixture(t, "minimax", "success", "", "", "")
		catalog, err := readTechnicalCatalog(context.Background(), f.adapter, "superwagie")
		require.NoError(t, err)
		assert.EqualValues(t, 128<<10, catalog.Models[0].Operations[0].Limits.MaxBodyBytes)
		f.config.OperationLimits["speech"]["voice"] = platformbilling.OperationLimits{Policy: "provider_reported_v1", MaxQuantity: 600, MaxBodyBytes: 64 << 10}
		adapter, err := platformbilling.New(f.db, f.config)
		require.NoError(t, err)
		catalog, err = readTechnicalCatalog(context.Background(), adapter, "superwagie")
		require.NoError(t, err)
		assert.EqualValues(t, 64<<10, catalog.Models[0].Operations[0].Limits.MaxBodyBytes)
	})
}
