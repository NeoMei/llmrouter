package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type imageGatewayFixture struct {
	engine           *gin.Engine
	adapter          *platformbilling.Adapter
	db               *gorm.DB
	cfg              platformbilling.Config
	sends            atomic.Int64
	downloads        atomic.Int64
	settleFail       atomic.Bool
	wire, kind       string
	img              []byte
	providerReceived chan struct{}
	providerRelease  chan struct{}
	settleEntered    chan struct{}
	settleRelease    chan struct{}
}

func newImageGatewayFixture(t *testing.T, kind, hubOrigin, token, dbPath string) *imageGatewayFixture {
	t.Helper()
	enableUniHubBoundary(t)
	gin.SetMode(gin.TestMode)
	f := &imageGatewayFixture{kind: kind}
	if kind == "wan-edit-cancel" {
		f.providerReceived = make(chan struct{})
		f.providerRelease = make(chan struct{})
	}
	if kind == "wan-edit-conflict" {
		f.settleEntered = make(chan struct{})
		f.settleRelease = make(chan struct{})
	}
	var b bytes.Buffer
	size := 2
	if strings.HasPrefix(kind, "wan-edit") {
		size = 512
	}
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, size, size))))
	f.img = b.Bytes()
	if dbPath == "" {
		dbPath = t.TempDir() + "/images.db"
	}
	db, e := gorm.Open(sqlite.Open("file:"+dbPath), &gorm.Config{})
	require.NoError(t, e)
	f.db = db
	for _, entry := range []any{&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.UserSubscription{}} {
		require.NoError(t, db.AutoMigrate(entry))
	}
	oldDB, oldLog, oldMemory, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
	model.DB, model.LOG_DB = db, db
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemory, oldRedis
	})
	origin, upstream, wire, api, channelType := "https://api.openai.com", "gpt-image-1", "images.generations.create", "v1", constant.ChannelTypeOpenAI
	if strings.HasPrefix(kind, "gemini") {
		origin, upstream, wire, api, channelType = "https://generativelanguage.googleapis.com", "certified-image-model", "gemini.images.generate", "v1beta", constant.ChannelTypeGemini
	}
	if strings.HasPrefix(kind, "qwen") {
		origin, upstream, wire, channelType = "https://dashscope.aliyuncs.com", "qwen-image-2.0", "dashscope.images.generate", constant.ChannelTypeAli
	}
	if strings.HasPrefix(kind, "volc") {
		origin, upstream, api, channelType = "https://ark.cn-beijing.volces.com", "certified-seedream", "v3", constant.ChannelTypeVolcEngine
	}
	if kind == "qwen-compatible" {
		wire = "images.generations.create"
	}
	op := "image_generation"
	if strings.HasPrefix(kind, "wan-edit") {
		origin, upstream, api, channelType = "https://dashscope.aliyuncs.com", "wanx2.1-imageedit", "v1", constant.ChannelTypeAli
	}
	if strings.HasPrefix(kind, "edit") || strings.HasPrefix(kind, "wan-edit") {
		wire, op = "images.edits.create", "image_edit"
	}
	f.wire = wire
	require.NoError(t, db.Save(&model.User{Id: 1, Username: "platform", AffCode: "platform", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.User{Id: 2, Username: "native", AffCode: "native", Status: common.UserStatusEnabled, Quota: 22222, Group: "default"}).Error)
	require.NoError(t, db.Save(&model.Token{Id: 1, UserId: 2, Key: "synthetic-native", Status: common.TokenStatusEnabled, RemainQuota: 54321, ExpiredTime: -1}).Error)
	require.NoError(t, db.Save(&model.Channel{Id: 1, Name: "images", Type: channelType, Key: "synthetic-image-key", Status: common.ChannelStatusEnabled, BaseURL: &origin, Models: upstream, Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.Ability{Group: "platform", Model: upstream, ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Save(&model.ClientModelRoute{Id: 1, PublicId: "image-model", Client: "all", Enabled: true, Metadata: fmt.Sprintf(`{"operations":[%q]}`, wire)}).Error)
	preferred := `["openai_chat"]`
	if channelType == constant.ChannelTypeGemini {
		preferred = `["gemini"]`
	}
	require.NoError(t, db.Save(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: upstream, PreferredWires: preferred, Enabled: true, Weight: 1}).Error)
	oldPrice := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice)) })
	oldRatio := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"platform":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatio)) })
	price := metering.Price{Version: "metered_v2", Operation: op, Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "images", Unit: "image", Price: metering.Rational{Numerator: "12", Denominator: "1"}}}}
	rate := platformbilling.Rate{ID: "image-rate", Model: "image-model", Asset: "CNY/6", Status: "published", Semantics: "metered_v2", Metered: &price}
	var remote platformbilling.RemoteCall
	if hubOrigin == "" {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var out any
			switch r.URL.Path {
			case "/internal/v1/model-credentials/verify":
				allowed := wire
				if kind == "gemini-text-grant" {
					allowed = "gemini.generate"
				}
				out = platformbilling.Claims{CredentialKind: "task", ID: "original-credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", Models: []string{"image-model"}, Operations: map[string][]string{"image-model": {allowed}}, TaskID: "image-task", ExpiresAt: time.Now().Add(time.Hour), Asset: "CNY/6", FundingMode: "prepaid_cash"}
			case "/internal/v1/rates":
				out = []platformbilling.Rate{rate}
			case "/internal/v1/calls/reserve":
				var in map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &in))
				remote = platformbilling.RemoteCall{WireOperation: in["wire_operation"], CallID: in["call_id"], ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", CredentialID: "original-credential", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
				out = remote
			case "/internal/v1/calls/image-call/settle":
				if kind == "wan-edit-conflict" {
					close(f.settleEntered)
					<-f.settleRelease
				}
				if f.settleFail.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				remote.Status = "SETTLED"
				remote.Charged = "12"
				out = remote
			case "/internal/v1/calls/image-call/release":
				remote.Status = "RELEASED"
				out = remote
			case "/internal/v1/calls/image-call":
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
		token = strings.Repeat("s", 32)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(kind, "wan-edit") && r.Method == "GET" && r.URL.Path == "/api/v1/tasks/provider-task" {
			assert.Equal(t, "Bearer synthetic-image-key", r.Header.Get("Authorization"))
			if kind == "wan-edit-pending" {
				_, _ = w.Write([]byte(`{"request_id":"poll","output":{"task_id":"provider-task","task_status":"RUNNING"}}`))
				return
			}
			if kind == "wan-edit-failed" {
				_, _ = w.Write([]byte(`{"request_id":"poll","output":{"task_id":"provider-task","task_status":"FAILED","task_metrics":{"TOTAL":1,"SUCCEEDED":0,"FAILED":1},"results":[]},"usage":{"image_count":0}}`))
				return
			}
			if kind == "wan-edit-uncertain" {
				_, _ = w.Write([]byte(`{"request_id":"poll","output":{"task_id":"provider-task","task_status":"FAILED"}}`))
				return
			}
			if kind == "wan-edit-evil" {
				_, _ = w.Write([]byte(`{"request_id":"poll","output":{"task_id":"provider-task","task_status":"SUCCEEDED","results":[{"url":"https://evil.example/a.png"}]},"usage":{"image_count":1}}`))
				return
			}
			_, _ = w.Write([]byte(`{"request_id":"poll","output":{"task_id":"provider-task","task_status":"SUCCEEDED","results":[{"url":"https://images.example/image.png?signature=fixture"}],"task_metrics":{"TOTAL":1,"SUCCEEDED":1,"FAILED":0}},"usage":{"image_count":1}}`))
			return
		}
		if r.Method == "GET" {
			f.downloads.Add(1)
			assert.Empty(t, r.Header.Get("Authorization"))
			assert.Empty(t, r.Header.Get("x-goog-api-key"))
			w.Header().Set("Content-Type", "image/png")
			if strings.Contains(kind, "mime") {
				w.Header().Set("Content-Type", "image/jpeg")
			}
			if strings.Contains(kind, "truncated") {
				_, _ = w.Write(f.img[:len(f.img)-4])
			} else {
				_, _ = w.Write(f.img)
			}
			return
		}
		f.sends.Add(1)
		var call platformbilling.Call
		require.NoError(t, db.First(&call, "call_id = ?", "image-call").Error)
		assert.Equal(t, "UNKNOWN", call.Status)
		assert.Equal(t, wire, call.WireOperation)
		var attempt platformbilling.Attempt
		require.NoError(t, db.First(&attempt, "call_id = ?", call.CallID).Error)
		assert.Equal(t, 1, attempt.ChannelID)
		if channelType == constant.ChannelTypeGemini {
			assert.Equal(t, "synthetic-image-key", r.Header.Get("x-goog-api-key"))
			assert.Empty(t, r.Header.Get("Authorization"))
			assert.Equal(t, "/v1beta/models/"+upstream+":generateContent", r.URL.Path)
		} else {
			assert.Equal(t, "Bearer synthetic-image-key", r.Header.Get("Authorization"))
		}
		if strings.HasPrefix(kind, "wan-edit") {
			assert.Equal(t, "/api/v1/services/aigc/image2image/image-synthesis", r.URL.Path)
			assert.Equal(t, "enable", r.Header.Get("X-DashScope-Async"))
			var request struct {
				Model string            `json:"model"`
				Input map[string]string `json:"input"`
			}
			require.NoError(t, common.DecodeJson(r.Body, &request))
			assert.Equal(t, upstream, request.Model)
			assert.Equal(t, "description_edit_with_mask", request.Input["function"])
			assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(f.img), request.Input["base_image_url"])
			maskBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(request.Input["mask_image_url"], "data:image/png;base64,"))
			require.NoError(t, err)
			mask, err := png.Decode(bytes.NewReader(maskBytes))
			require.NoError(t, err)
			assert.Equal(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBAModel.Convert(mask.At(0, 0)))
			if kind == "wan-edit-cancel" {
				close(f.providerReceived)
				<-f.providerRelease
			}
			_, _ = w.Write([]byte(`{"request_id":"submit","output":{"task_id":"provider-task","task_status":"PENDING"}}`))
			return
		} else if wire == "images.edits.create" {
			assert.Equal(t, "/v1/images/edits", r.URL.Path)
			require.NoError(t, r.ParseMultipartForm(1<<20))
			defer r.MultipartForm.RemoveAll()
			assert.Equal(t, upstream, r.FormValue("model"))
			assert.Equal(t, "0", r.FormValue("output_compression"))
			for _, name := range []string{"image", "mask"} {
				file, header, e := r.FormFile(name)
				require.NoError(t, e)
				data, e := io.ReadAll(file)
				require.NoError(t, e)
				require.NoError(t, file.Close())
				assert.Equal(t, f.img, data)
				assert.Equal(t, name+".png", header.Filename)
				assert.Equal(t, "image/png", header.Header.Get("Content-Type"))
			}
		} else {
			raw, e := io.ReadAll(r.Body)
			require.NoError(t, e)
			var req map[string]any
			require.NoError(t, common.UnmarshalUnique(raw, &req))
			if channelType == constant.ChannelTypeGemini {
				assert.Contains(t, string(raw), `"temperature":0`)
				assert.Contains(t, string(raw), `"responseModalities":["IMAGE"]`)
				assert.Contains(t, string(raw), base64.StdEncoding.EncodeToString(f.img))
				assert.Nil(t, req["model"])
			} else {
				assert.Equal(t, upstream, req["model"])
			}
			if strings.HasPrefix(kind, "qwen") {
				assert.Equal(t, "/api/v1/services/aigc/multimodal-generation/generation", r.URL.Path)
				assert.Contains(t, string(raw), `"watermark":false`)
				assert.Contains(t, string(raw), `"seed":0`)
				if kind != "qwen-compatible" {
					assert.Contains(t, string(raw), base64.StdEncoding.EncodeToString(f.img))
				}
			}
			if strings.HasPrefix(kind, "volc") {
				assert.Equal(t, "/api/v3/images/generations", r.URL.Path)
				assert.Contains(t, string(raw), `"watermark":false`)
				assert.Contains(t, string(raw), `"seed":0`)
				if kind != "qwen-compatible" {
					assert.Contains(t, string(raw), base64.StdEncoding.EncodeToString(f.img))
				}
			}
		}
		if strings.Contains(kind, "refus") {
			w.WriteHeader(401)
			raw := `{"code":"InvalidApiKey","message":"Invalid API-key provided.","request_id":"refusal"}`
			if kind == "qwen-refusal-conflict" {
				raw = `{"code":"InvalidApiKey","message":"refusal","request_id":"refusal","output":{}}`
			}
			_, _ = w.Write([]byte(raw))
			return
		}
		if strings.Contains(kind, "transport") {
			conn, _, e := w.(http.Hijacker).Hijack()
			require.NoError(t, e)
			require.NoError(t, conn.Close())
			return
		}
		b64 := base64.StdEncoding.EncodeToString(f.img)
		if channelType == constant.ChannelTypeGemini {
			var jpegBytes bytes.Buffer
			require.NoError(t, jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil))
			b64 = base64.StdEncoding.EncodeToString(jpegBytes.Bytes())
		}
		output := fmt.Sprintf(`{"data":[{"b64_json":%q}],"usage":{"output_tokens":0},"created":1}`, b64)
		if channelType == constant.ChannelTypeGemini {
			output = fmt.Sprintf(`{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/jpeg","data":%q}}]}}],"usageMetadata":`+imageGatewayGeminiUsage+`}`, b64)
		}
		if strings.HasPrefix(kind, "qwen") {
			output = `{"request_id":"qwen-result","output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"image":"https://images.example/image.png"}]}}]},"usage":{"image_count":1,"width":2,"height":2}}`
		}
		if strings.Contains(kind, "empty") {
			output = `{"data":[]}`
		}
		if strings.Contains(kind, "duplicate") {
			output = fmt.Sprintf(`{"data":[{"b64_json":%q},{"b64_json":%q}]}`, b64, b64)
		}
		if strings.Contains(kind, "partial") {
			output = `{"data":[{"b64_json":`
		}
		if strings.Contains(kind, "wrongcount") {
			output = strings.Replace(output, `"image_count":1`, `"image_count":2`, 1)
		}
		_, _ = w.Write([]byte(output))
	}))
	t.Cleanup(provider.Close)
	oldClient := imageProducerHTTPClient
	imageProducerHTTPClient = func() *http.Client {
		return &http.Client{Transport: audioFixtureTransport{provider.URL, http.DefaultTransport}}
	}
	t.Cleanup(func() { imageProducerHTTPClient = oldClient })
	hash := sha256.Sum256([]byte("synthetic-image-key"))
	policy := platformbilling.ImagePolicy{Version: "image_v1", PublicModel: "image-model", WireOperation: wire, ChannelID: 1, ChannelType: channelType, UpstreamModel: upstream, ChannelKeySHA256: hex.EncodeToString(hash[:]), Origin: origin, APIVersion: api, MaxBodyBytes: 10 << 20, MaxImageBytes: 1 << 20, MaxPixels: 16 << 20, MaxImages: 3, MaxInputImages: 3, ResourceOrigins: []string{"https://images.example"}, RateIDs: map[string]string{"CNY/6": "image-rate"}}
	if strings.HasPrefix(kind, "wan-edit") {
		policy.MaxInputImages = 1
	}
	f.cfg = platformbilling.Config{Enabled: true, Origin: hubOrigin, ServiceToken: token, PrincipalID: 1, Group: "platform", MaxBodyBytes: 1 << 20, MaxInputTokens: 1000, MaxOutputTokens: 100, ImagePolicies: map[string]map[string]platformbilling.ImagePolicy{wire: {"image-model": policy}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{op: {"image-model": {MaxBodyBytes: 10 << 20, MaxQuantity: 3, Policy: "successful_outputs_v1"}}}, OperationRateIDs: map[string]map[string]map[string]string{op: {"CNY/6": {"image-model": "image-rate"}}}}
	f.adapter, e = platformbilling.New(db, f.cfg)
	require.NoError(t, e)
	f.engine = gin.New()
	SetPlatformRouter(f.engine, f.adapter)
	return f
}
func imageGatewayRequest(t *testing.T, kind string, img []byte) (string, string, []byte) {
	t.Helper()
	path, ct := "/platform/v1/images/generations", "application/json"
	b64 := base64.StdEncoding.EncodeToString(img)
	body := []byte(`{"model":"image-model","prompt":"slide","n":3,"output_compression":0}`)
	if strings.HasPrefix(kind, "gemini") {
		path = "/platform/v1/native/gemini/images/models/image-model:generateContent"
		body = []byte(fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"slide"},{"inlineData":{"mimeType":"image/png","data":%q}}]}],"generationConfig":{"responseModalities":["IMAGE"],"responseFormat":{"image":{"aspectRatio":"ASPECT_RATIO_SIXTEEN_BY_NINE","imageSize":"IMAGE_SIZE_TWO_K","mimeType":"IMAGE_JPEG"}},"temperature":0}}`, b64))
	}
	if strings.HasPrefix(kind, "qwen") {
		path = "/platform/v1/native/dashscope/images/generations"
		body = []byte(fmt.Sprintf(`{"model":"image-model","input":{"messages":[{"role":"user","content":[{"text":"slide"},{"image":"data:image/png;base64,%s"}]}]},"parameters":{"n":3,"seed":0,"watermark":false,"prompt_extend":false}}`, b64))
	}
	if kind == "qwen-compatible" {
		path = "/platform/v1/images/generations"
		body = []byte(`{"model":"image-model","prompt":"slide","n":3,"seed":0,"watermark":false,"prompt_extend":false}`)
	}
	if strings.HasPrefix(kind, "volc") {
		body = []byte(fmt.Sprintf(`{"model":"image-model","prompt":"slide","image":"data:image/png;base64,%s","seed":0,"watermark":false}`, b64))
	}
	if strings.HasPrefix(kind, "edit") || strings.HasPrefix(kind, "wan-edit") {
		path = "/platform/v1/images/edits"
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		fields := [][2]string{{"model", "image-model"}, {"prompt", "slide"}, {"n", "3"}, {"output_compression", "0"}}
		if strings.HasPrefix(kind, "wan-edit") {
			fields = [][2]string{{"model", "image-model"}, {"prompt", "slide"}, {"n", "1"}}
		}
		for _, field := range fields {
			require.NoError(t, w.WriteField(field[0], field[1]))
		}
		if kind == "edit-mime" {
			require.NoError(t, w.WriteField("output_format", "jpeg"))
		}
		for _, name := range []string{"image", "mask"} {
			header := textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"; filename="%s.png"`, name, name)}, "Content-Type": {"image/png"}}
			part, e := w.CreatePart(header)
			require.NoError(t, e)
			_, e = part.Write(img)
			require.NoError(t, e)
		}
		require.NoError(t, w.Close())
		ct = w.FormDataContentType()
		body = b.Bytes()
	}
	return path, ct, body
}
func (f *imageGatewayFixture) request(t *testing.T, path, ct string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	r.Header.Set("X-UniHub-Task-ID", "image-task")
	r.Header.Set("Idempotency-Key", "image-call")
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}
func TestImageSelectedNativeTransports(t *testing.T) {
	for _, kind := range []string{"openai", "edit", "edit-mime", "gemini", "qwen", "qwen-compatible", "volc", "empty", "duplicate", "partial", "transport", "qwen-wrongcount", "qwen-mime", "qwen-truncated", "qwen-refused", "qwen-refusal-conflict"} {
		t.Run(kind, func(t *testing.T) {
			f := newImageGatewayFixture(t, kind, "", "", "")
			path, ct, body := imageGatewayRequest(t, kind, f.img)
			w := f.request(t, path, ct, body)
			want := 200
			if (strings.Contains(kind, "-") && kind != "qwen-compatible") || kind == "empty" || kind == "duplicate" || kind == "partial" || kind == "transport" {
				want = 502
			}
			require.Equal(t, want, w.Code, w.Body.String())
			assert.EqualValues(t, 1, f.sends.Load())
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call, "call_id = ?", "image-call").Error)
			if want == 200 {
				assert.Equal(t, "SETTLED", call.Status)
				assert.Equal(t, "12", call.Charged)
				assert.NotEmpty(t, call.ImageObservation)
				assert.NotContains(t, w.Body.String(), "https://images.example")
			} else if kind == "qwen-refused" {
				assert.Equal(t, "RELEASED", call.Status)
			} else {
				assert.Equal(t, "UNKNOWN", call.Status)
			}
			w = f.request(t, path, ct, body)
			require.Equal(t, 409, w.Code)
			assert.EqualValues(t, 1, f.sends.Load())
			if kind == "edit" {
				_, ct, body = imageGatewayRequest(t, kind, f.img)
				w = f.request(t, path, ct, body)
				assert.Equal(t, 409, w.Code)
				assert.EqualValues(t, 1, f.sends.Load())
			}
		})
	}
}

func TestWanMaskedEditSubmitsOncePollsAndSettles(t *testing.T) {
	f := newImageGatewayFixture(t, "wan-edit", "", "", "")
	path, contentType, body := imageGatewayRequest(t, "wan-edit", f.img)
	w := f.request(t, path, contentType, body)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"id":"image-call"`)
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	assert.Equal(t, "UNKNOWN", row.Status)
	assert.Equal(t, "provider-task", row.ProviderTaskID)
	recovery, _, err := f.adapter.WanImageTaskForRecovery(context.Background(), row.CallID)
	require.NoError(t, err)
	assert.Equal(t, row.ProviderTaskID, recovery.ProviderTaskID)
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", row.CallID).Update("instance_id", "other").Error)
	_, _, err = f.adapter.WanImageTaskForRecovery(context.Background(), row.CallID)
	require.Error(t, err)
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", row.CallID).Update("instance_id", f.cfg.InstanceID).Error)
	assert.Equal(t, http.StatusConflict, f.request(t, path, contentType, body).Code)
	assert.EqualValues(t, 1, f.sends.Load())
	request := httptest.NewRequest(http.MethodGet, "/platform/v1/images/edits/image-call", nil)
	request.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	request.Header.Set("X-UniHub-Task-ID", "image-task")
	wrongTask := request.Clone(request.Context())
	wrongTask.Header.Set("X-UniHub-Task-ID", "other-task")
	denied := httptest.NewRecorder()
	f.engine.ServeHTTP(denied, wrongTask)
	assert.Equal(t, http.StatusNotFound, denied.Code)
	assert.Zero(t, f.downloads.Load())
	result := httptest.NewRecorder()
	f.engine.ServeHTTP(result, request)
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	assert.Contains(t, result.Body.String(), `"b64_json"`)
	assert.NotContains(t, result.Body.String(), "signature")
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.Equal(t, "12", row.Charged)
	assert.NotEmpty(t, row.ImageObservation)
	assert.EqualValues(t, 1, f.downloads.Load())

	// A crash after persisting the image and before metering evidence can be
	// recovered from that immutable observation without another supplier call.
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "image-call").Updates(map[string]any{"status": "UNKNOWN", "evidence": "", "intent": "", "charged": ""}).Error)
	recovered := httptest.NewRecorder()
	f.engine.ServeHTTP(recovered, request.Clone(request.Context()))
	require.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
	assert.Equal(t, result.Body.String(), recovered.Body.String())
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.EqualValues(t, 1, f.downloads.Load())

	// A subsequent read uses the verified persisted result, never the producer.
	replay := httptest.NewRecorder()
	f.engine.ServeHTTP(replay, request.Clone(request.Context()))
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	assert.Equal(t, result.Body.String(), replay.Body.String())
	assert.EqualValues(t, 1, f.sends.Load())
	assert.EqualValues(t, 1, f.downloads.Load())
}

func TestWanMaskedEditBindsTaskAfterClientCancellation(t *testing.T) {
	f := newImageGatewayFixture(t, "wan-edit-cancel", "", "", "")
	path, contentType, body := imageGatewayRequest(t, "wan-edit", f.img)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	req.Header.Set("X-UniHub-Task-ID", "image-task")
	req.Header.Set("Idempotency-Key", "image-call")
	done := make(chan struct{})
	go func() { f.engine.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-f.providerReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("supplier request never arrived")
	}
	cancel()
	close(f.providerRelease)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("submit handler did not finish")
	}
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	assert.Equal(t, "UNKNOWN", row.Status)
	assert.Equal(t, "provider-task", row.ProviderTaskID)
	assert.EqualValues(t, 1, f.sends.Load())
}

func TestWanMaskedEditWithholdsResultUntilSettlementRecovers(t *testing.T) {
	f := newImageGatewayFixture(t, "wan-edit", "", "", "")
	path, contentType, body := imageGatewayRequest(t, "wan-edit", f.img)
	require.Equal(t, http.StatusAccepted, f.request(t, path, contentType, body).Code)
	f.settleFail.Store(true)
	request := httptest.NewRequest(http.MethodGet, "/platform/v1/images/edits/image-call", nil)
	request.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	request.Header.Set("X-UniHub-Task-ID", "image-task")
	first := httptest.NewRecorder()
	f.engine.ServeHTTP(first, request)
	assert.Equal(t, http.StatusConflict, first.Code)
	assert.NotContains(t, first.Body.String(), "b64_json")
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	assert.Equal(t, "SETTLE_PENDING", row.Status)
	f.settleFail.Store(false)
	retry := httptest.NewRecorder()
	f.engine.ServeHTTP(retry, request.Clone(request.Context()))
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	assert.Contains(t, retry.Body.String(), "b64_json")
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.EqualValues(t, 1, f.sends.Load())
	assert.EqualValues(t, 1, f.downloads.Load())
}

func TestWanMaskedEditDoesNotReplayASettledConflictedObservation(t *testing.T) {
	f := newImageGatewayFixture(t, "wan-edit", "", "", "")
	path, ct, body := imageGatewayRequest(t, "wan-edit", f.img)
	require.Equal(t, http.StatusAccepted, f.request(t, path, ct, body).Code)
	request := httptest.NewRequest(http.MethodGet, "/platform/v1/images/edits/image-call", nil)
	request.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	request.Header.Set("X-UniHub-Task-ID", "image-task")
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, request)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "image-call").Update("evidence_conflict", true).Error)
	w = httptest.NewRecorder()
	f.engine.ServeHTTP(w, request.Clone(request.Context()))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.NotContains(t, w.Body.String(), "b64_json")
}

func TestWanMaskedEditWithholdsResultWhenConflictAppearsDuringSettlement(t *testing.T) {
	f := newImageGatewayFixture(t, "wan-edit-conflict", "", "", "")
	path, ct, body := imageGatewayRequest(t, "wan-edit", f.img)
	require.Equal(t, http.StatusAccepted, f.request(t, path, ct, body).Code)
	request := httptest.NewRequest(http.MethodGet, "/platform/v1/images/edits/image-call", nil)
	request.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	request.Header.Set("X-UniHub-Task-ID", "image-task")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.engine.ServeHTTP(w, request); close(done) }()
	select {
	case <-f.settleEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("settlement never started")
	}
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	alternative := image.NewRGBA(image.Rect(0, 0, 512, 512))
	alternative.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, alternative))
	policy := f.cfg.ImagePolicies["images.edits.create"]["image-model"]
	second, err := platformbilling.ObserveWanMaskedEdit(policy, "provider-task", []byte(`{"request_id":"poll","output":{"task_id":"provider-task","task_status":"SUCCEEDED","results":[{"url":"https://images.example/image.png"}],"task_metrics":{"TOTAL":1,"SUCCEEDED":1,"FAILED":0}},"usage":{"image_count":1}}`), func(string) ([]byte, string, error) { return encoded.Bytes(), "image/png", nil })
	require.NoError(t, err)
	assert.ErrorIs(t, f.adapter.CompleteWanImage(context.Background(), row, second), platformbilling.ErrConflict)
	close(f.settleRelease)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("settlement never finished")
	}
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.NotContains(t, w.Body.String(), "b64_json")
	again := httptest.NewRecorder()
	f.engine.ServeHTTP(again, request.Clone(context.Background()))
	assert.Equal(t, http.StatusConflict, again.Code)
}

func TestWanMaskedEditPollDoesNotChargeOrReleaseUncertainResults(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		status  int
		billing string
	}{
		{"wan-edit-pending", http.StatusAccepted, "UNKNOWN"},
		{"wan-edit-uncertain", http.StatusConflict, "UNKNOWN"},
		{"wan-edit-evil", http.StatusBadGateway, "UNKNOWN"},
		{"wan-edit-failed", http.StatusOK, "RELEASED"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := newImageGatewayFixture(t, tc.kind, "", "", "")
			path, contentType, body := imageGatewayRequest(t, tc.kind, f.img)
			require.Equal(t, http.StatusAccepted, f.request(t, path, contentType, body).Code)
			request := httptest.NewRequest(http.MethodGet, "/platform/v1/images/edits/image-call", nil)
			request.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
			request.Header.Set("X-UniHub-Task-ID", "image-task")
			result := httptest.NewRecorder()
			f.engine.ServeHTTP(result, request)
			require.Equal(t, tc.status, result.Code, result.Body.String())
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
			assert.Equal(t, tc.billing, row.Status)
			assert.Empty(t, row.ImageObservation)
			assert.Zero(t, f.downloads.Load())
			assert.EqualValues(t, 1, f.sends.Load())
		})
	}
}
func TestImageLocalLiveFixture(t *testing.T) {
	if os.Getenv("UNIHUB_IMAGE_E2E") != "1" {
		t.Skip("explicit isolated image fixture")
	}
	dir := os.Getenv("UNIHUB_IMAGE_E2E_DIR")
	require.True(t, filepath.IsAbs(dir))
	token, e := os.ReadFile(filepath.Join(dir, "service-token"))
	require.NoError(t, e)
	f := newImageGatewayFixture(t, os.Getenv("UNIHUB_IMAGE_CASE"), os.Getenv("UNIHUB_IMAGE_ORIGIN"), string(token), filepath.Join(dir, "gateway.db"))
	f.engine.GET("/__fixture/status", func(c *gin.Context) {
		var user, native model.User
		var token model.Token
		var call platformbilling.Call
		require.NoError(t, f.db.First(&user, 1).Error)
		require.NoError(t, f.db.First(&native, 2).Error)
		require.NoError(t, f.db.First(&token, 1).Error)
		require.NoError(t, f.db.First(&call, "call_id = ?", "image-call").Error)
		c.JSON(200, gin.H{"provider_calls": f.sends.Load(), "quota": user.Quota, "used_quota": user.UsedQuota, "native_quota": native.Quota, "native_token_quota": token.RemainQuota, "status": call.Status, "charged": call.Charged, "observation": call.ImageObservation, "wire": call.WireOperation})
	})
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, e)
	server := &http.Server{Handler: f.engine}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready"), []byte("http://"+listener.Addr().String()), 0600))
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	<-signals
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))
	require.ErrorIs(t, <-done, http.ErrServerClosed)
}

func TestImageAuthorityBeforeFundsAndImmutableReplay(t *testing.T) {
	for _, variant := range []string{"channel-key", "upstream", "origin", "rate", "disabled", "multikey", "body", "wire"} {
		t.Run(variant, func(t *testing.T) {
			f := newImageGatewayFixture(t, "gemini", "", "", "")
			path, ct, body := imageGatewayRequest(t, "gemini", f.img)
			switch variant {
			case "channel-key":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = 1").Update("key", "different-key").Error)
			case "upstream":
				require.NoError(t, f.db.Model(&model.ClientModelTarget{}).Where("id = 1").Update("upstream_model", "wrong-model").Error)
			case "origin":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = 1").Update("base_url", "https://wrong.example").Error)
			case "rate":
				p := f.cfg.ImagePolicies[f.wire]["image-model"]
				p.RateIDs = map[string]string{"CNY/6": "wrong-rate"}
				f.cfg.ImagePolicies[f.wire]["image-model"] = p
				var e error
				f.adapter, e = platformbilling.New(f.db, f.cfg)
				require.NoError(t, e)
				f.engine = gin.New()
				SetPlatformRouter(f.engine, f.adapter)
			case "disabled":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = 1").Update("status", common.ChannelStatusManuallyDisabled).Error)
			case "multikey":
				var channel model.Channel
				require.NoError(t, f.db.First(&channel, 1).Error)
				channel.ChannelInfo.IsMultiKey = true
				require.NoError(t, f.db.Save(&channel).Error)
			case "body":
				body = bytes.Replace(body, []byte(`"IMAGE"`), []byte(`"TEXT"`), 1)
			case "wire":
				path = "/platform/v1beta/models/image-model:generateContent"
			}
			w := f.request(t, path, ct, body)
			assert.NotEqual(t, 200, w.Code)
			assert.Zero(t, f.sends.Load())
			var calls int64
			require.NoError(t, f.db.Model(&platformbilling.Call{}).Count(&calls).Error)
			assert.Zero(t, calls)
		})
	}
	f := newImageGatewayFixture(t, "edit", "", "", "")
	path, ct, body := imageGatewayRequest(t, "edit", f.img)
	require.Equal(t, 200, f.request(t, path, ct, body).Code)
	f.cfg.ImagePolicies = nil
	var e error
	f.adapter, e = platformbilling.New(f.db, f.cfg)
	require.NoError(t, e)
	f.engine = gin.New()
	SetPlatformRouter(f.engine, f.adapter)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = 1").Update("key", "retired-key").Error)
	require.Equal(t, 409, f.request(t, path, ct, body).Code)
	changed := bytes.Replace(body, []byte("slide"), []byte("other"), 1)
	exists, e := f.adapter.ImageReplay(context.Background(), "uh_model_"+strings.Repeat("a", 64), "image-task", "image-call", platformbilling.WireOperation{ID: "images.edits.create"}, ct, changed)
	assert.True(t, exists)
	assert.ErrorIs(t, e, platformbilling.ErrConflict)
	assert.EqualValues(t, 1, f.sends.Load())
}

func TestImageCancellationAfterSendKeepsUnknown(t *testing.T) {
	f := newImageGatewayFixture(t, "openai", "", "", "")
	path, ct, body := imageGatewayRequest(t, "openai", f.img)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := imageProducerHTTPClient()
	imageProducerHTTPClient = func() *http.Client { return &http.Client{Transport: imageCancelTransport{base.Transport, cancel}} }
	r := httptest.NewRequest("POST", path, bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer uh_model_"+strings.Repeat("a", 64))
	r.Header.Set("X-UniHub-Task-ID", "image-task")
	r.Header.Set("Idempotency-Key", "image-call")
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	assert.Equal(t, 502, w.Code)
	assert.EqualValues(t, 1, f.sends.Load())
	var call platformbilling.Call
	require.NoError(t, f.db.First(&call, "call_id = ?", "image-call").Error)
	assert.Equal(t, "UNKNOWN", call.Status)
	assert.Equal(t, 409, f.request(t, path, ct, body).Code)
	assert.EqualValues(t, 1, f.sends.Load())
}

type imageCancelTransport struct {
	base   http.RoundTripper
	cancel context.CancelFunc
}

func (r imageCancelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, e := r.base.RoundTrip(req)
	r.cancel()
	return response, e
}

func TestGeminiImageNeverWidensTextGrant(t *testing.T) {
	f := newImageGatewayFixture(t, "gemini-text-grant", "", "", "")
	path, ct, body := imageGatewayRequest(t, "gemini", f.img)
	w := f.request(t, path, ct, body)
	assert.Equal(t, 400, w.Code)
	assert.Zero(t, f.sends.Load())
	var calls int64
	require.NoError(t, f.db.Model(&platformbilling.Call{}).Count(&calls).Error)
	assert.Zero(t, calls)
}

// Native usage is a supplier observation; billing still counts one decoded image.
const imageGatewayGeminiUsage = `{"promptTokenCount":258,"cachedContentTokenCount":0,"candidatesTokenCount":1290,"toolUsePromptTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":1548,"promptTokensDetails":[{"modality":"IMAGE","tokenCount":258}],"cacheTokensDetails":[{"modality":"TEXT","tokenCount":0}],"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1290}],"toolUsePromptTokensDetails":[],"serviceTier":"standard"}`

type imageResponseMutationTransport struct {
	base                http.RoundTripper
	needle, replacement []byte
}

func (tr imageResponseMutationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := tr.base.RoundTrip(req)
	if err != nil {
		return res, err
	}
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	raw = bytes.Replace(raw, tr.needle, tr.replacement, 1)
	res.Body = io.NopCloser(bytes.NewReader(raw))
	res.ContentLength = int64(len(raw))
	return res, nil
}
func TestImageGatewayReviewBoundaries(t *testing.T) {
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)
	truncated := append(append([]byte{}, webp...), []byte{'J', 'U', 'N', 'K', 255, 255, 255, 127}...)
	binary.LittleEndian.PutUint32(truncated[4:8], uint32(len(truncated)-8))
	duplicate := append(append([]byte{}, webp...), webp[12:]...)
	binary.LittleEndian.PutUint32(duplicate[4:8], uint32(len(duplicate)-8))
	for _, tc := range []struct {
		name, kind, needle, replacement string
		valid                           bool
	}{
		{"gemini-native-usage", "gemini", "", "", true},
		{"gemini-flex", "gemini", `"serviceTier":"standard"`, `"serviceTier":"flex"`, true},
		{"gemini-priority", "gemini", `"serviceTier":"standard"`, `"serviceTier":"priority"`, true},
		{"gemini-usage-secret", "gemini", `"modality":"IMAGE"`, `"modality":"https://supplier.example/private?signature=synthetic"`, false},
		{"created-object", "openai", `"created":1`, `"created":{"download_url":"https://supplier.example/private?signature=synthetic"}`, false},
		{"size-object", "openai", `"created":1`, `"created":1,"size":{"download_url":"https://supplier.example/private?signature=synthetic"}`, false},
		{"revised-prompt-object", "openai", `"b64_json":`, `"revised_prompt":{"token":"synthetic-private"},"b64_json":`, false},
		{"revised-prompt-text", "openai", `"b64_json":`, `"revised_prompt":"A scene: blue sky, 16:9.","b64_json":`, true},
		{"revised-prompt-url-projection", "openai", `"b64_json":`, `"revised_prompt":"A scene at https://supplier.example/private?signature=synthetic","b64_json":`, true},
		{"native-id-object", "qwen", `"request_id":"qwen-result"`, `"request_id":{"url":"https://supplier.example/private?signature=synthetic"}`, false},
		{"gemini-index-object", "gemini", `"finishReason":"STOP"`, `"finishReason":"STOP","index":{"url":"https://supplier.example/private?signature=synthetic"}`, false},
		{"gemini-safety-url", "gemini", `"finishReason":"STOP"`, `"finishReason":"STOP","safetyRatings":[{"category":"https://supplier.example/private?signature=synthetic","probability":"LOW"}]`, false},
		{"gemini-id-url-projection", "gemini", `"usageMetadata":`, `"responseId":"https://supplier.example/private?signature=synthetic","usageMetadata":`, true},
		{"usage-key-url", "openai", `"output_tokens":0`, `"https://supplier.example/private":0`, false},
		{"usage-detail-object", "openai", `"output_tokens":0`, `"input_tokens_details":{"text_tokens":{"url":"https://supplier.example/private"}}`, false},
		{"native-numeric-usage", "openai", `"output_tokens":0`, `"input_tokens":27,"total_tokens":27,"output_tokens":0,"input_tokens_details":{"text_tokens":2,"image_tokens":25}`, true},
		{"gemini-native-id", "gemini", `"usageMetadata":`, `"responseId":"native+//result==","modelVersion":"gemini-image-certified","usageMetadata":`, true},
		{"webp-valid", "openai", `"b64_json":`, `"b64_json":`, true},
		{"webp-truncated", "openai", `"b64_json":`, `"b64_json":`, false},
		{"webp-duplicate", "openai", `"b64_json":`, `"b64_json":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImageGatewayFixture(t, tc.kind, "", "", "")
			needle, replacement := []byte(tc.needle), []byte(tc.replacement)
			if strings.HasPrefix(tc.name, "webp-") {
				data := webp
				if tc.name == "webp-truncated" {
					data = truncated
				}
				if tc.name == "webp-duplicate" {
					data = duplicate
				}
				needle = []byte(base64.StdEncoding.EncodeToString(f.img))
				replacement = []byte(base64.StdEncoding.EncodeToString(data))
			}
			base := imageProducerHTTPClient()
			imageProducerHTTPClient = func() *http.Client {
				return &http.Client{Transport: imageResponseMutationTransport{base.Transport, needle, replacement}}
			}
			path, ct, body := imageGatewayRequest(t, tc.kind, f.img)
			w := f.request(t, path, ct, body)
			var call platformbilling.Call
			require.NoError(t, f.db.First(&call, "call_id = ?", "image-call").Error)
			assert.EqualValues(t, 1, f.sends.Load())
			assert.NotContains(t, w.Body.String(), "supplier.example")
			assert.NotContains(t, w.Body.String(), "synthetic-private")
			if tc.valid {
				require.Equal(t, 200, w.Code, w.Body.String())
				assert.Equal(t, "SETTLED", call.Status)
				assert.Equal(t, "12", call.Charged)
				if tc.name == "gemini-native-usage" {
					assert.Contains(t, w.Body.String(), `"modality":"IMAGE"`)
					assert.Contains(t, call.ImageObservation, imageGatewayGeminiUsage)
				}
				if tc.name == "revised-prompt-text" {
					assert.Contains(t, w.Body.String(), "A scene: blue sky, 16:9.")
				}
				if tc.name == "gemini-native-id" {
					assert.Contains(t, w.Body.String(), "native+//result==")
				}
			} else {
				require.Equal(t, 502, w.Code)
				assert.Equal(t, "UNKNOWN", call.Status)
				assert.Empty(t, call.ImageObservation)
				assert.Empty(t, call.Charged)
			}
			w = f.request(t, path, ct, body)
			assert.Equal(t, 409, w.Code)
			assert.EqualValues(t, 1, f.sends.Load())
		})
	}
}
