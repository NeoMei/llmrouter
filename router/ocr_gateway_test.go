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
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
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
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type ocrGatewayFixture struct {
	engine                                     *gin.Engine
	adapter                                    *platformbilling.Adapter
	db                                         *gorm.DB
	now                                        time.Time
	sends, polls, reserves, settles, downloads int
	variant                                    string
	runtime                                    *ocr.Runtime
	image                                      []byte
	cfg                                        platformbilling.Config
	serviceCfg                                 ocr.Config
}

func ocrMultipart(t *testing.T, data []byte) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	require.NoError(t, w.WriteField("model", "ocr-model"))
	require.NoError(t, w.WriteField("optionalPayload", `{"useDocOrientationClassify":false,"temperature":0}`))
	p, e := w.CreateFormFile("file", "source.png")
	require.NoError(t, e)
	_, e = p.Write(data)
	require.NoError(t, e)
	require.NoError(t, w.Close())
	return w.FormDataContentType(), b.Bytes()
}
func newOCRGatewayFixture(t *testing.T, variant, hubOrigin, serviceToken, dbPath, objectPath string) *ocrGatewayFixture {
	t.Helper()
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	f := &ocrGatewayFixture{now: time.Now().UTC(), variant: variant}
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	f.image = imageBytes.Bytes()
	if dbPath == "" {
		dbPath = t.TempDir() + "/ocr.db"
	}
	if objectPath == "" {
		objectPath = t.TempDir()
	}
	db, e := gorm.Open(sqlite.Open("file:"+dbPath), &gorm.Config{})
	require.NoError(t, e)
	f.db = db
	for _, entry := range []any{&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.OCRJob{}, &model.OCROperation{}, &model.OCRAuditEvent{}, &model.OCRResource{}, &model.OCRResourceGrant{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.UserSubscription{}} {
		require.NoError(t, db.AutoMigrate(entry), "migration %T", entry)
	}
	oldDB, oldLog, oldRuntime, oldMemory, oldRedis := model.DB, model.LOG_DB, controller.OCRRuntime, common.MemoryCacheEnabled, common.RedisEnabled
	model.DB, model.LOG_DB = db, db
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB, controller.OCRRuntime = oldDB, oldLog, oldRuntime
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemory, oldRedis
	})
	origin, upstream, wire, channelType := "https://paddle.example", "PaddleOCR-VL-1.6", "ocr.jobs.create", constant.ChannelTypePaddleOCR
	if strings.HasPrefix(variant, "qwen") {
		origin, upstream, wire, channelType = "https://dashscope.aliyuncs.com", "qwen-vl-ocr-2025-11-20", "dashscope.ocr.recognitions.create", constant.ChannelTypeAli
	}
	providerTokens := strings.HasPrefix(variant, "qwen-token") || strings.HasPrefix(variant, "qwen35-token")
	if strings.HasPrefix(variant, "qwen-token") {
		upstream = "qwen-vl-ocr"
	}
	if strings.HasPrefix(variant, "qwen35-token") {
		upstream = "qwen3.5-ocr"
	}
	require.NoError(t, db.Save(&model.User{Id: 1, Username: "platform", AffCode: "platform", Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 7, Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.User{Id: 2, Username: "native", AffCode: "native", Status: common.UserStatusEnabled, Quota: 22222, Group: "default"}).Error)
	require.NoError(t, db.Save(&model.Token{Id: 1, UserId: 2, Key: "synthetic-native", Status: common.TokenStatusEnabled, RemainQuota: 54321, ExpiredTime: -1}).Error)
	require.NoError(t, db.Save(&model.Channel{Id: 1, Name: "ocr", Type: channelType, Key: "synthetic-paddle-key", Status: common.ChannelStatusEnabled, BaseURL: &origin, Models: upstream, Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.Ability{Group: "platform", Model: upstream, ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Save(&model.ClientModelRoute{Id: 1, PublicId: "ocr-model", Client: "all", Enabled: true, Metadata: fmt.Sprintf(`{"operations":[%q]}`, wire)}).Error)
	preferred := `["paddleocr_jobs"]`
	if strings.HasPrefix(variant, "qwen") {
		preferred = `["openai_chat"]`
	}
	require.NoError(t, db.Save(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: upstream, PreferredWires: preferred, Enabled: true, Weight: 1}).Error)
	oldRatio := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"platform":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatio)) })
	price := metering.Price{Version: "metered_v2", Operation: "ocr", Policy: "success_request", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "12", Denominator: "1"}}}}
	if providerTokens {
		price.Policy = "provider_tokens_v1"
		price.Terms = []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "3", Denominator: "10"}}, {Key: "output", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}}}
		if strings.HasPrefix(variant, "qwen35-token") {
			price.Terms = []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}}, {Key: "output", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "1"}}}
		}
	}
	rate := platformbilling.Rate{ID: "ocr-rate", Model: "ocr-model", Asset: "CNY/6", Status: "published", Semantics: "metered_v2", Metered: &price}
	var remote platformbilling.RemoteCall
	if hubOrigin == "" {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var out any
			switch r.URL.Path {
			case "/internal/v1/model-credentials/verify":
				out = platformbilling.Claims{CredentialKind: "task", ID: "original-credential", Audience: "llmrouter", ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", Models: []string{"ocr-model"}, Operations: map[string][]string{"ocr-model": {wire}}, TaskID: "ocr-task", ExpiresAt: time.Now().Add(time.Hour), Asset: "CNY/6", FundingMode: "prepaid_cash"}
			case "/internal/v1/rates":
				out = []platformbilling.Rate{rate}
			case "/internal/v1/calls/reserve":
				f.reserves++
				var in map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &in))
				remote = platformbilling.RemoteCall{WireOperation: in["wire_operation"], CallID: in["call_id"], ActorID: "actor", AccountID: "account", OriginProductID: "superwagie", InstanceID: "default", CredentialID: "original-credential", RequestDigest: in["request_digest"], Rate: rate, ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Held: in["amount"], Status: "RESERVED"}
				out = remote
			case "/internal/v1/calls/ocr-call/release":
				remote.Status = "RELEASED"
				out = remote
			case "/internal/v1/calls/ocr-call/settle":
				f.settles++
				remote.Status = "SETTLED"
				remote.Charged = "12"
				var settlement struct {
					Usage platformbilling.Usage `json:"usage"`
				}
				require.NoError(t, common.DecodeJson(r.Body, &settlement))
				if settlement.Usage.Metered != nil {
					amount, e := metering.Calculate(*rate.Metered, *settlement.Usage.Metered)
					require.NoError(t, e)
					remote.Charged = amount.RatString()
				}
				out = remote
			case "/internal/v1/call-reads/verify":
				var row platformbilling.Call
				require.NoError(t, db.First(&row, "call_id = ?", "ocr-call").Error)
				out = platformbilling.OwnedReadClaims{CredentialKind: "owned_call_read_v1", Audience: "llmrouter-read", ClientID: "sw", CallID: row.CallID, RequestDigest: row.Digest, Model: row.Model, WireOperation: row.WireOperation, TaskID: row.TaskID, CredentialID: row.CredentialID, OriginalCredentialKind: "task", ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, InstanceID: row.InstanceID, ExpiresAt: time.Now().Add(4 * time.Minute)}
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
		serviceToken = strings.Repeat("s", 32)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services/aigc/multimodal-generation/generation":
			f.sends++
			assert.Equal(t, "Bearer synthetic-paddle-key", r.Header.Get("Authorization"))
			var call platformbilling.Call
			require.NoError(t, db.First(&call, "call_id = ?", "ocr-call").Error)
			assert.Equal(t, "UNKNOWN", call.Status)
			var attempt platformbilling.Attempt
			require.NoError(t, db.First(&attempt, "call_id = ?", call.CallID).Error)
			raw, e := io.ReadAll(r.Body)
			require.NoError(t, e)
			var request map[string]any
			require.NoError(t, common.UnmarshalUnique(raw, &request))
			assert.Equal(t, upstream, request["model"])
			messages := request["input"].(map[string]any)["messages"].([]any)
			img := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
			assert.Equal(t, false, img["enable_rotate"])
			assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(f.image), img["image"])
			if strings.HasPrefix(variant, "qwen-refusal") {
				w.WriteHeader(401)
				switch variant {
				case "qwen-refusal":
					_, _ = w.Write([]byte(`{"code":"InvalidApiKey","message":"Invalid API-key provided.","request_id":"rejected-before-execution"}`))
				case "qwen-refusal-conflict":
					_, _ = w.Write([]byte(`{"code":"InvalidApiKey","message":"refusal","error":{"code":"other"},"request_id":"rejected-before-execution"}`))
				case "qwen-refusal-malformed":
					_, _ = w.Write([]byte(`{"code":"InvalidApiKey","request_id":`))
				case "qwen-refusal-output":
					_, _ = w.Write([]byte(`{"code":"InvalidApiKey","message":"refusal","request_id":"conflict","output":{"choices":[]}}`))
				case "qwen-refusal-unknown":
					_, _ = w.Write([]byte(`{"code":"UnknownError","request_id":"unknown"}`))
				}
				return
			}
			words := `[{"text":"OK","rotate_rect":[1,1,2,1,0]}]`
			usage := `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}`
			finish := "stop"
			if providerTokens && variant != "qwen-token-zero" {
				usage = `,"usage":{"input_tokens":10000,"output_tokens":2000,"total_tokens":12000}`
			}
			if providerTokens && variant == "qwen-token-zero" {
				usage = `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`
			}
			if providerTokens && variant == "qwen-token-over" {
				usage = `,"usage":{"input_tokens":20001,"output_tokens":2000,"total_tokens":22001}`
			}
			if variant == "qwen-missing" || variant == "qwen-token-missing" {
				usage = ""
			}
			if variant == "qwen-zero" {
				usage = `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`
			}
			if variant == "qwen-blank" || variant == "qwen-empty" {
				words = `[]`
			}
			if variant == "qwen-angle" {
				words = `[{"text":"bad","rotate_rect":[1,1,1,1,91]}]`
			}
			if variant == "qwen-bounds" {
				words = `[{"text":"bad","rotate_rect":[10000,1,2,1,0]}]`
			}
			if variant == "qwen-duplicate" {
				words = `[{"text":"OK","rotate_rect":[1,1,2,1,0]},{"text":"OK","rotate_rect":[1,1,2,1,0]}]`
			}
			if variant == "qwen-incomplete" {
				finish = "length"
			}
			fmt.Fprintf(w, `{"request_id":"native-qwen-id","output":{"choices":[{"finish_reason":%q,"message":{"role":"assistant","content":[{"ocr_result":{"words_info":%s}}]}}]}%s}`, finish, words, usage)
		case "/api/v2/ocr/jobs":
			f.sends++
			assert.Equal(t, "Bearer synthetic-paddle-key", r.Header.Get("Authorization"))
			var call platformbilling.Call
			require.NoError(t, db.First(&call, "call_id = ?", "ocr-call").Error)
			assert.Equal(t, "UNKNOWN", call.Status)
			var attempt platformbilling.Attempt
			require.NoError(t, db.First(&attempt, "call_id = ?", call.CallID).Error)
			assert.Equal(t, 1, attempt.ChannelID)
			var job model.OCRJob
			require.NoError(t, db.First(&job, "platform_call_id = ?", call.CallID).Error)
			assert.Equal(t, "submitting", job.SubmissionState)
			require.NoError(t, r.ParseMultipartForm(1<<20))
			defer r.MultipartForm.RemoveAll()
			assert.Equal(t, upstream, r.FormValue("model"))
			assert.JSONEq(t, `{"useDocOrientationClassify":false,"temperature":0}`, r.FormValue("optionalPayload"))
			file, _, e := r.FormFile("file")
			require.NoError(t, e)
			data, e := io.ReadAll(file)
			file.Close()
			require.NoError(t, e)
			assert.Equal(t, f.image, data)
			if variant == "refused" {
				w.WriteHeader(429)
				return
			}
			if variant == "failed" {
				_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"provider-owned"}}`))
				return
			}
			if variant == "transport" {
				h := w.(http.Hijacker)
				c, _, e := h.Hijack()
				require.NoError(t, e)
				c.Close()
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"provider-owned"}}`))
		case "/api/v2/ocr/jobs/provider-owned":
			f.polls++
			if variant == "failed" {
				_, _ = w.Write([]byte(`{"code":0,"data":{"state":"failed","jobId":"provider-owned"}}`))
				return
			}
			assert.Equal(t, "Bearer synthetic-paddle-key", r.Header.Get("Authorization"))
			if f.polls == 1 {
				_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"provider-owned","state":"pending"}}`))
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"jobId":"provider-owned","state":"done","extractProgress":{"totalPages":1,"extractedPages":1},"resultUrl":{"jsonUrl":%q}}}`, origin+"/result.jsonl")
		case "/result.jsonl":
			switch variant {
			case "empty-page":
				_, _ = w.Write([]byte(`{"result":{"layoutParsingResults":[{}]}}`))
				return
			case "incomplete-page":
				_, _ = w.Write([]byte(`{"result":{"layoutParsingResults":[{"markdown":{"images":{}}}]}}`))
				return
			case "blank-page":
				_, _ = w.Write([]byte(`{"result":{"layoutParsingResults":[{"markdown":{"text":"  ","images":{}}}]}}`))
				return
			case "wrong-text-type":
				_, _ = w.Write([]byte(`{"result":{"layoutParsingResults":[{"markdown":{"text":123,"images":{}}}]}}`))
				return
			}

			f.downloads++
			if variant == "json-expiry" && f.polls == 2 {
				w.WriteHeader(403)
				return
			}
			assert.Empty(t, r.Header.Get("Authorization"))
			if variant == "malformed" {
				_, _ = w.Write([]byte(`{"result":null}`))
				return
			}
			fmt.Fprintf(w, `{"result":{"layoutParsingResults":[{"markdown":{"text":"recognized ![](image.png)","images":{"image.png":%q}},"preserved":false}]}}`+"\n", origin+"/image.png")
		case "/image.png":
			f.downloads++
			if variant == "image-expiry" && f.polls == 2 {
				w.WriteHeader(403)
				return
			}
			assert.Empty(t, r.Header.Get("Authorization"))
			_, _ = w.Write(f.image)
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(provider.Close)
	transport := audioFixtureTransport{provider.URL, http.DefaultTransport}
	supplier := &http.Client{Transport: transport}
	h := sha256.Sum256([]byte("synthetic-paddle-key"))
	policy := platformbilling.OCRPolicy{Version: "ocr_v1", PublicModel: "ocr-model", WireOperation: "ocr.jobs.create", ChannelID: 1, ChannelType: constant.ChannelTypePaddleOCR, ChannelKeySHA256: hex.EncodeToString(h[:]), UpstreamModel: upstream, Origin: origin, APIversion: "paddle-v2", UsagePolicy: "success_request", ResourceOrigins: []string{origin}, RateIDs: map[string]string{"CNY/6": "ocr-rate"}}
	if strings.HasPrefix(variant, "qwen") {
		policy.WireOperation = wire
		policy.ChannelType = channelType
		policy.UpstreamModel = upstream
		policy.Origin = origin
		policy.APIversion = "dashscope-advanced-recognition-v1"
		policy.MaxImageBytes = 10 << 20
		policy.MaxImagePixels = 16 * 1024 * 1024
		policy.BlankPolicy = "reject_empty_v1"
		if providerTokens {
			policy.UsagePolicy = "provider_tokens_v1"
			policy.MaxInputTokens = 20000
			policy.MaxOutputTokens = 10000
		}
		if variant == "qwen-blank" {
			policy.BlankPolicy = "allow_empty_complete_v1"
		}
	}
	oldQwenClient := qwenOCRHTTPClient
	qwenOCRHTTPClient = func() *http.Client { return supplier }
	t.Cleanup(func() { qwenOCRHTTPClient = oldQwenClient })
	operationLimit := platformbilling.OperationLimits{MaxBodyBytes: 1 << 20, MaxQuantity: 1, Policy: "success_request"}
	if providerTokens {
		operationLimit.MaxQuantity = policy.MaxInputTokens
		operationLimit.Policy = policy.UsagePolicy
	}
	f.cfg = platformbilling.Config{Enabled: true, Origin: hubOrigin, ServiceToken: serviceToken, PrincipalID: 1, Group: "platform", MaxBodyBytes: 1 << 20, MaxInputTokens: 20000, MaxOutputTokens: 10000, OCRPolicies: map[string]map[string]platformbilling.OCRPolicy{wire: {"ocr-model": policy}}, OperationLimits: map[string]map[string]platformbilling.OperationLimits{"ocr": {"ocr-model": operationLimit}}, OperationRateIDs: map[string]map[string]map[string]string{"ocr": {"CNY/6": {"ocr-model": "ocr-rate"}}}}
	f.adapter, e = platformbilling.New(db, f.cfg)
	require.NoError(t, e)
	objects, e := objectstore.NewLocal(objectPath)
	require.NoError(t, e)
	billing, e := ocr.NewBilling(db)
	require.NoError(t, e)
	t.Cleanup(func() { require.NoError(t, billing.Close()) })
	f.serviceCfg = ocr.Config{PublicOrigin: "https://owned.example", GrantSecret: bytes.Repeat([]byte{3}, 32), GrantTTL: time.Minute, Retention: time.Hour, Now: func() time.Time { return f.now }, Objects: objects, SupplierHTTP: supplier, FetchHTTP: supplier}
	svc, e := ocr.NewService(db, billing, f.serviceCfg)
	require.NoError(t, e)
	f.runtime = &ocr.Runtime{DB: db, Billing: billing, Service: svc}
	controller.OCRRuntime = f.runtime
	f.engine = gin.New()
	SetPlatformRouter(f.engine, f.adapter)
	f.engine.GET("/api/ocr/jobs/:id/resources/:resourceID", (&controller.OCRHandler{Runtime: f.runtime}).Resource)
	return f
}
func (f *ocrGatewayFixture) request(t *testing.T, method, path, token, ct string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method == "POST" {
		r.Header.Set("Idempotency-Key", "ocr-call")
		r.Header.Set("X-UniHub-Task-ID", "ocr-task")
	}
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}
func TestPaddleActualNativeRouteOwnedResources(t *testing.T) {
	f := newOCRGatewayFixture(t, "success", "", "", "", "")
	ct, body := ocrMultipart(t, f.image)
	token := "uh_model_" + strings.Repeat("a", 64)
	read := "uh_read_" + strings.Repeat("b", 64)
	w := f.request(t, "POST", "/platform/api/v2/ocr/jobs", token, ct, body)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"jobId":"ocr-call"`)
	assert.Equal(t, 1, f.sends)
	for i := 0; i < 2; i++ {
		w = f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	require.Contains(t, w.Body.String(), `"state":"done"`)
	assert.Equal(t, 1, f.settles)
	var out struct {
		Data ocr.JobStatus `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &out))
	expired := out.Data.ResultURL.JSONURL
	u, e := url.Parse(expired)
	require.NoError(t, e)
	result := f.request(t, "GET", u.RequestURI(), "", "", nil)
	require.Equal(t, 200, result.Code, result.Body.String())
	assert.Contains(t, result.Body.String(), "recognized")
	var line map[string]any
	require.NoError(t, common.Unmarshal(result.Body.Bytes(), &line))
	pages := line["result"].(map[string]any)["layoutParsingResults"].([]any)
	imageURL := pages[0].(map[string]any)["markdown"].(map[string]any)["images"].(map[string]any)["image.png"].(string)
	iu, e := url.Parse(imageURL)
	require.NoError(t, e)
	f.now = f.now.Add(2 * time.Minute)
	assert.Equal(t, 404, f.request(t, "GET", u.RequestURI(), "", "", nil).Code)
	assert.Equal(t, 404, f.request(t, "GET", iu.RequestURI(), "", "", nil).Code)
	svc, e := ocr.NewService(f.db, f.runtime.Billing, f.serviceCfg)
	require.NoError(t, e)
	f.runtime.Service = svc
	w = f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &out))
	u, e = url.Parse(out.Data.ResultURL.JSONURL)
	require.NoError(t, e)
	result = f.request(t, "GET", u.RequestURI(), "", "", nil)
	require.Equal(t, 200, result.Code)
	require.NoError(t, common.Unmarshal(result.Body.Bytes(), &line))
	pages = line["result"].(map[string]any)["layoutParsingResults"].([]any)
	imageURL = pages[0].(map[string]any)["markdown"].(map[string]any)["images"].(map[string]any)["image.png"].(string)
	iu, e = url.Parse(imageURL)
	require.NoError(t, e)
	imageResult := f.request(t, "GET", iu.RequestURI(), "", "", nil)
	require.Equal(t, 200, imageResult.Code)
	assert.Equal(t, f.image, imageResult.Body.Bytes())
	assert.Equal(t, 1, f.sends)
	assert.Equal(t, 2, f.downloads)
	assert.Equal(t, 1, f.reserves)
	assert.Equal(t, 1, f.settles)
	assert.Equal(t, 404, f.request(t, "GET", "/platform/api/v2/ocr/jobs/provider-owned", read, "", nil).Code)
	assert.Equal(t, 401, f.request(t, "POST", "/platform/api/v2/ocr/jobs", read, ct, body).Code)
	var user model.User
	require.NoError(t, f.db.First(&user, 1).Error)
	assert.Equal(t, 12345, user.Quota)
	assert.Equal(t, 7, user.UsedQuota)
}

func TestQwenOCRNativeGeometryActualRoute(t *testing.T) {
	for _, variant := range []string{"qwen-success", "qwen-zero", "qwen-blank", "qwen-empty", "qwen-missing", "qwen-bounds", "qwen-angle", "qwen-duplicate", "qwen-incomplete"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, variant, "", "", "", "")
			body := []byte(fmt.Sprintf(`{"model":"ocr-model","input":{"messages":[{"role":"user","content":[{"image":"data:image/png;base64,%s","enable_rotate":false}]}]},"parameters":{"ocr_options":{"task":"advanced_recognition"}}}`, base64.StdEncoding.EncodeToString(f.image)))
			w := f.request(t, "POST", "/platform/v1/native/dashscope/ocr/recognitions", "uh_model_"+strings.Repeat("a", 64), "application/json", body)
			good := variant == "qwen-success" || variant == "qwen-zero" || variant == "qwen-blank"
			if good {
				require.Equal(t, 200, w.Code, w.Body.String())
				assert.Contains(t, w.Body.String(), "words_info")
				assert.Equal(t, 1, f.settles)
			} else {
				require.Equal(t, 502, w.Code, w.Body.String())
				assert.Zero(t, f.settles)
			}
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.reserves)
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", "ocr-call").Error)
			assert.NotEmpty(t, row.OCRObservation)
			if good {
				assert.Equal(t, "SETTLED", row.Status)
			} else {
				assert.Equal(t, "UNKNOWN", row.Status)
			}
			var user model.User
			require.NoError(t, f.db.First(&user, 1).Error)
			assert.Equal(t, 12345, user.Quota)
		})
	}
}

func TestQwenOCRProviderTokenOriginalPriceActualRoute(t *testing.T) {
	for variant, charged := range map[string]string{"qwen-token-success": "4000", "qwen35-token-success": "9000", "qwen-token-zero": "0"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, variant, "", "", "", "")
			body := []byte(fmt.Sprintf(`{"model":"ocr-model","input":{"messages":[{"role":"user","content":[{"image":"data:image/png;base64,%s","enable_rotate":false}]}]},"parameters":{"ocr_options":{"task":"advanced_recognition"}}}`, base64.StdEncoding.EncodeToString(f.image)))
			w := f.request(t, "POST", "/platform/v1/native/dashscope/ocr/recognitions", "uh_model_"+strings.Repeat("a", 64), "application/json", body)
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.Equal(t, 1, f.settles)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.reserves)
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", "ocr-call").Error)
			assert.Equal(t, "SETTLED", row.Status)
			assert.Equal(t, charged, row.Charged)
		})
	}
	for _, variant := range []string{"qwen-token-missing", "qwen-token-over"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, variant, "", "", "", "")
			body := []byte(fmt.Sprintf(`{"model":"ocr-model","input":{"messages":[{"role":"user","content":[{"image":"data:image/png;base64,%s","enable_rotate":false}]}]},"parameters":{"ocr_options":{"task":"advanced_recognition"}}}`, base64.StdEncoding.EncodeToString(f.image)))
			w := f.request(t, "POST", "/platform/v1/native/dashscope/ocr/recognitions", "uh_model_"+strings.Repeat("a", 64), "application/json", body)
			require.Equal(t, 502, w.Code, w.Body.String())
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.reserves)
			assert.Zero(t, f.settles)
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", "ocr-call").Error)
			assert.Equal(t, "UNKNOWN", row.Status)
			assert.Equal(t, "11000", row.Amount)
		})
	}
}

func TestPaddleSafetyAndImmutableReplay(t *testing.T) {
	for _, variant := range []string{"malformed", "transport", "refused", "failed"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, variant, "", "", "", "")
			ct, body := ocrMultipart(t, f.image)
			token := "uh_model_" + strings.Repeat("a", 64)
			read := "uh_read_" + strings.Repeat("b", 64)
			w := f.request(t, "POST", "/platform/api/v2/ocr/jobs", token, ct, body)
			if variant == "transport" || variant == "refused" {
				require.Equal(t, 502, w.Code, w.Body.String())
			} else {
				require.Equal(t, 200, w.Code, w.Body.String())
				for i := 0; i < 2; i++ {
					w = f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
				}
				if variant == "malformed" {
					require.Equal(t, 502, w.Code)
				} else {
					require.Equal(t, 200, w.Code)
				}
			}
			assert.Equal(t, 1, f.sends)
			assert.Zero(t, f.settles)
			// Replayed native multipart boundaries differ, but semantic request is stable.
			ct, body = ocrMultipart(t, f.image)
			w = f.request(t, "POST", "/platform/api/v2/ocr/jobs", token, ct, body)
			require.Equal(t, 409, w.Code, w.Body.String())
			assert.Equal(t, 1, f.sends)
			w = f.request(t, "POST", "/platform/v1/ocr/jobs", token, ct, body)
			require.Equal(t, 409, w.Code)
			assert.Equal(t, 1, f.sends)
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row, "call_id = ?", "ocr-call").Error)
			if variant == "refused" || variant == "failed" {
				assert.Equal(t, "RELEASED", row.Status)
			} else {
				assert.Equal(t, "UNKNOWN", row.Status)
			}
		})
	}
}
func TestPaddleAdmissionRejectsBeforeFunds(t *testing.T) {
	for _, variant := range []string{"bytes", "key", "origin", "multikey", "parameters"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, "success", "", "", "", "")
			data := f.image
			switch variant {
			case "bytes":
				data = []byte("not image")
			case "key":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "changed").Error)
			case "origin":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("base_url", "https://wrong.example").Error)
			case "multikey":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("channel_info", `{"is_multi_key":true}`).Error)
			case "parameters":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("param_override", `{"model":"other"}`).Error)
			}
			ct, body := ocrMultipart(t, data)
			w := f.request(t, "POST", "/platform/api/v2/ocr/jobs", "uh_model_"+strings.Repeat("a", 64), ct, body)
			require.True(t, w.Code >= 400, w.Body.String())
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
			var count int64
			require.NoError(t, f.db.Model(&platformbilling.Call{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestPaddleSupplierResourcesExpireIndependently(t *testing.T) {
	for _, variant := range []string{"json-expiry", "image-expiry"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, variant, "", "", "", "")
			ct, body := ocrMultipart(t, f.image)
			token := "uh_model_" + strings.Repeat("a", 64)
			read := "uh_read_" + strings.Repeat("b", 64)
			require.Equal(t, 200, f.request(t, "POST", "/platform/api/v2/ocr/jobs", token, ct, body).Code)
			require.Equal(t, 200, f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil).Code)
			require.Equal(t, 502, f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil).Code)
			assert.Zero(t, f.settles)
			svc, e := ocr.NewService(f.db, f.runtime.Billing, f.serviceCfg)
			require.NoError(t, e)
			f.runtime.Service = svc
			w := f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"state":"done"`)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.settles)
			assert.Equal(t, 3, f.polls)
		})
	}
}
func TestQwenOCRAdmissionBoundsBeforeFunds(t *testing.T) {
	for _, variant := range []string{"rotate", "pixels", "external", "mime", "task", "huge", "target-key", "target-origin"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, "qwen-success", "", "", "", "")
			data := fmt.Sprintf(`{"model":"ocr-model","input":{"messages":[{"role":"user","content":[{"image":"data:image/png;base64,%s","enable_rotate":false}]}]},"parameters":{"ocr_options":{"task":"advanced_recognition"}}}`, base64.StdEncoding.EncodeToString(f.image))
			switch variant {
			case "rotate":
				data = strings.Replace(data, `"enable_rotate":false`, `"enable_rotate":true`, 1)
			case "pixels":
				data = strings.Replace(data, `"enable_rotate":false`, `"max_pixels":18446744073709551615`, 1)
			case "external":
				data = strings.Replace(data, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(f.image), "https://evil.example/image", 1)
			case "mime":
				data = strings.Replace(data, "image/png", "image/jpeg", 1)
			case "task":
				data = strings.Replace(data, "advanced_recognition", "text_recognition", 1)
			case "huge":
				data = strings.Replace(data, `"task":"advanced_recognition"`, `"task":"advanced_recognition","max_tokens":18446744073709551615`, 1)
			case "target-key":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "changed").Error)
			case "target-origin":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("base_url", "https://wrong.example").Error)
			}
			w := f.request(t, "POST", "/platform/v1/native/dashscope/ocr/recognitions", "uh_model_"+strings.Repeat("a", 64), "application/json", []byte(data))
			require.True(t, w.Code >= 400, w.Body.String())
			assert.Zero(t, f.reserves)
			assert.Zero(t, f.sends)
		})
	}
}

func TestOriginalOCRReadCertificateRecovery(t *testing.T) {
	for _, mutation := range []string{"valid", "member", "key", "url", "origin", "attempt", "partial-conflict"} {
		t.Run(mutation, func(t *testing.T) {
			f := newOCRGatewayFixture(t, "success", "", "", "", "")
			ct, body := ocrMultipart(t, f.image)
			require.Equal(t, 200, f.request(t, "POST", "/platform/api/v2/ocr/jobs", "uh_model_"+strings.Repeat("a", 64), ct, body).Code)
			var job model.OCRJob
			require.NoError(t, f.db.First(&job).Error)
			var execution model.OCRExecution
			require.NoError(t, common.UnmarshalJsonStr(job.Execution, &execution))
			execution.RequestedModel = ""
			execution.KeySHA256 = ""
			execution.ResourceOrigins = nil
			require.NoError(t, f.db.Model(&platformbilling.Call{}).Where("call_id = ?", "ocr-call").Update("credential_kind", nil).Error)
			switch mutation {
			case "member":
				var binding model.OCRAccountBinding
				require.NoError(t, common.UnmarshalJsonStr(job.AccountBinding, &binding))
				binding.Ref.MemberID = "other"
				raw, e := common.Marshal(binding)
				require.NoError(t, e)
				require.NoError(t, f.db.Model(&job).Update("account_binding", string(raw)).Error)
			case "key":
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("key", "changed").Error)
			case "url":
				execution.JobsURL = "https://wrong.example/api/v2/ocr/jobs"
			case "origin":
				p := f.cfg.OCRPolicies["ocr.jobs.create"]["ocr-model"]
				p.Origin = "https://changed.example"
				f.cfg.OCRPolicies["ocr.jobs.create"]["ocr-model"] = p
				var e error
				f.adapter, e = platformbilling.New(f.db, f.cfg)
				require.NoError(t, e)
				f.engine = gin.New()
				SetPlatformRouter(f.engine, f.adapter)
			case "attempt":
				require.NoError(t, f.db.Model(&platformbilling.Attempt{}).Where("call_id = ?", "ocr-call").Update("channel_id", 2).Error)
			case "partial-conflict":
				execution.KeySHA256 = strings.Repeat("0", 64)
			}
			raw, e := common.Marshal(execution)
			require.NoError(t, e)
			require.NoError(t, f.db.Model(&job).Update("execution", string(raw)).Error)
			w := f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", "uh_read_"+strings.Repeat("b", 64), "", nil)
			if mutation == "valid" {
				require.Equal(t, 200, w.Code, w.Body.String())
				require.NoError(t, f.db.First(&job).Error)
				require.NoError(t, common.UnmarshalJsonStr(job.Execution, &execution))
				assert.Equal(t, "owned-call-exact-certificate-v1", execution.ReadProofVersion)
				assert.NotEmpty(t, execution.KeySHA256)
				assert.Equal(t, 1, f.polls)
			} else {
				require.Equal(t, 409, w.Code, w.Body.String())
				assert.Zero(t, f.polls)
			}
			assert.Equal(t, 1, f.sends)
			assert.Zero(t, f.settles)
		})
	}
}

func TestPaddleCanonicalCompatibility(t *testing.T) {
	f := newOCRGatewayFixture(t, "success", "", "", "", "")
	ct, body := ocrMultipart(t, f.image)
	w := f.request(t, "POST", "/platform/v1/ocr/jobs", "uh_model_"+strings.Repeat("a", 64), ct, body)
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"id":"ocr-call"`)
	read := "uh_read_" + strings.Repeat("b", 64)
	require.Equal(t, 404, f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil).Code)
	for i := 0; i < 2; i++ {
		w = f.request(t, "GET", "/platform/v1/ocr/jobs/ocr-call", read, "", nil)
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	assert.Equal(t, 1, f.sends)
	assert.Equal(t, 1, f.settles)
}

func TestLegacyOCRConcurrentConflictingCallScopeCannotRecover(t *testing.T) {
	f := newOCRGatewayFixture(t, "success", "", "", "", "")
	ct, body := ocrMultipart(t, f.image)
	require.Equal(t, 200, f.request(t, "POST", "/platform/api/v2/ocr/jobs", "uh_model_"+strings.Repeat("a", 64), ct, body).Code)
	var job model.OCRJob
	require.NoError(t, f.db.First(&job).Error)
	var execution model.OCRExecution
	require.NoError(t, common.UnmarshalJsonStr(job.Execution, &execution))
	execution.RequestedModel = ""
	execution.KeySHA256 = ""
	execution.ResourceOrigins = nil
	raw, e := common.Marshal(execution)
	require.NoError(t, e)
	require.NoError(t, f.db.Model(&job).Update("execution", string(raw)).Error)
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row).Error)
	stale := row
	stale.Digest = "conflicting-concurrent-verification"
	var group sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 1)
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		results <- recoverOwnedOCRRead(context.Background(), f.adapter, stale)
	}()
	close(start)
	err := recoverOwnedOCRRead(context.Background(), f.adapter, row)
	group.Wait()
	require.NoError(t, err)
	require.Error(t, <-results)
	require.NoError(t, f.db.First(&job).Error)
	require.NoError(t, common.UnmarshalJsonStr(job.Execution, &execution))
	assert.Equal(t, "owned-call-exact-certificate-v1", execution.ReadProofVersion)
	assert.Zero(t, f.polls)
	assert.Equal(t, 1, f.sends)
	assert.Zero(t, f.settles)
}

func TestOCRReviewedOutputAndRefusalContracts(t *testing.T) {
	for _, variant := range []string{"empty-page", "incomplete-page", "blank-page", "wrong-text-type", "qwen-refusal", "qwen-refusal-conflict", "qwen-refusal-malformed", "qwen-refusal-output", "qwen-refusal-unknown"} {
		t.Run(variant, func(t *testing.T) {
			f := newOCRGatewayFixture(t, variant, "", "", "", "")
			token := "uh_model_" + strings.Repeat("a", 64)
			if strings.HasPrefix(variant, "qwen") {
				body := []byte(fmt.Sprintf(`{"model":"ocr-model","input":{"messages":[{"role":"user","content":[{"image":"data:image/png;base64,%s","enable_rotate":false}]}]},"parameters":{"ocr_options":{"task":"advanced_recognition"}}}`, base64.StdEncoding.EncodeToString(f.image)))
				w := f.request(t, "POST", "/platform/v1/native/dashscope/ocr/recognitions", token, "application/json", body)
				require.Equal(t, 502, w.Code, w.Body.String())
			} else {
				ct, body := ocrMultipart(t, f.image)
				require.Equal(t, 200, f.request(t, "POST", "/platform/api/v2/ocr/jobs", token, ct, body).Code)
				read := "uh_read_" + strings.Repeat("b", 64)
				require.Equal(t, 200, f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil).Code)
				w := f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
				assert.Equal(t, 502, w.Code, w.Body.String())
				var resources int64
				require.NoError(t, f.db.Model(&model.OCRResource{}).Count(&resources).Error)
				assert.Zero(t, resources)
			}
			var row platformbilling.Call
			require.NoError(t, f.db.First(&row).Error)
			want := "UNKNOWN"
			if variant == "qwen-refusal" {
				want = "RELEASED"
			}
			assert.Equal(t, want, row.Status)
			assert.Equal(t, 1, f.sends)
			assert.Equal(t, 1, f.reserves)
			assert.Zero(t, f.settles)
		})
	}
}
