package router

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOCRPublicFixedConsumerAndCanonical(t *testing.T) {
	var sends, polls, mode atomic.Int32
	var img bytes.Buffer
	require.NoError(t, png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	var supplier *httptest.Server
	supplier = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/ocr/jobs":
			sends.Add(1)
			if mode.Load() == 1 {
				w.WriteHeader(503)
				return
			}
			if mode.Load() == 2 {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(429)
				return
			}
			require.NoError(t, r.ParseMultipartForm(1<<20))
			assert.Equal(t, "PaddleOCR-VL-1.6", r.FormValue("model"))
			assert.JSONEq(t, `{"temperature":0,"useTableRecognition":true}`, r.FormValue("optionalPayload"))
			f, _, err := r.FormFile("file")
			require.NoError(t, err)
			b, _ := io.ReadAll(f)
			f.Close()
			assert.Equal(t, img.Bytes(), b)
			if r.Header.Get("Authorization") == "Bearer A" {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(429)
				return
			}
			assert.Equal(t, "Bearer B", r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"code":0,"data":{"jobId":"private-provider"}}`)
		case "/api/v2/ocr/jobs/private-provider":
			polls.Add(1)
			assert.Equal(t, "Bearer B", r.Header.Get("Authorization"))
			fmt.Fprintf(w, `{"code":0,"data":{"state":"done","extractProgress":{"totalPages":1,"extractedPages":1},"resultUrl":{"jsonUrl":%q}}}`, supplier.URL+"/result")
		case "/result":
			assert.Empty(t, r.Header.Get("Authorization"))
			fmt.Fprintf(w, `{"result":{"layoutParsingResults":[{"prunedResult":{"width":100,"height":200,"page_index":0,"parsing_res_list":[{"block_label":"table","block_content":"<table>A</table>","block_bbox":[1,2,3,4]},{"block_label":"formula","block_content":"x^2"}]},"markdown":{"text":"Actual text ![](p.png)","images":{"p.png":%q}},"unknown":9007199254740993}]}}`, supplier.URL+"/image")
		case "/image":
			assert.Empty(t, r.Header.Get("Authorization"))
			w.Write(img.Bytes())
		default:
			t.Errorf("unexpected supplier %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer supplier.Close()
	oldDB := model.DB
	oldMaster, oldPath := common.IsMasterNode, common.SQLitePath
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	t.Setenv("SQL_DSN", "local")
	common.IsMasterNode = false
	common.SQLitePath = filepath.Join(t.TempDir(), "public.db")
	require.NoError(t, model.InitDB())
	db := model.DB
	t.Cleanup(func() {
		model.DB = oldDB
		common.IsMasterNode = oldMaster
		common.SQLitePath = oldPath
		common.SetDatabaseTypes(oldMainType, oldLogType)
	})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.OCRJob{}, &model.OCROperation{}, &model.OCRAuditEvent{}, &model.OCRResource{}, &model.OCRResourceGrant{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	oldRedis, oldMemory, oldPrice, oldRetry := common.RedisEnabled, common.MemoryCacheEnabled, ratio_setting.ModelPrice2JSONString(), common.RetryTimes
	model.DB = db
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.RetryTimes = 1
	t.Cleanup(func() {
		model.DB = oldDB
		common.RedisEnabled = oldRedis
		common.MemoryCacheEnabled = oldMemory
		common.RetryTimes = oldRetry
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice))
	})
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ocr-price":0.0002}`))
	u := model.User{Username: uuid.NewString(), AffCode: uuid.NewString(), Quota: 1000, Status: common.UserStatusEnabled, Group: "default"}
	require.NoError(t, db.Create(&u).Error)
	token := model.Token{UserId: u.Id, Key: strings.ReplaceAll(uuid.NewString(), "-", ""), RemainQuota: 1000, Status: common.TokenStatusEnabled, ExpiredTime: -1, ModelLimitsEnabled: true, ModelLimits: "public-ocr"}
	require.NoError(t, db.Create(&token).Error)
	base := supplier.URL
	ch := model.Channel{Type: constant.ChannelTypePaddleOCR, Name: "ocr", Key: "A\nB", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: "PaddleOCR-VL-1.6", Group: "default", ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	require.NoError(t, db.Create(&ch).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: ch.Id, Group: "default", Model: "PaddleOCR-VL-1.6", Enabled: true}).Error)
	route := model.ClientModelRoute{PublicId: "public-ocr", Client: "all", Enabled: true, Metadata: `{"operations":["ocr.jobs.create","ocr.jobs.retrieve"],"input_modalities":["image","document"],"output_modalities":["text"],"async":true,"streaming":false}`}
	require.NoError(t, db.Create(&route).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{RouteId: route.Id, ChannelId: ch.Id, UpstreamModel: "PaddleOCR-VL-1.6", BillingModel: "ocr-price", PreferredWires: `["paddleocr_jobs"]`, Enabled: true, Weight: 1}).Error)
	engine := gin.New()
	server := httptest.NewServer(engine)
	defer server.Close()
	objects, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)
	billing := &ocr.Billing{OCRBillingStore: model.NewOCRBillingStore(db, nil)}
	defer billing.Close()
	svc, err := ocr.NewService(db, billing, ocr.Config{PublicOrigin: server.URL, GrantSecret: bytes.Repeat([]byte{7}, 32), Objects: objects, SupplierHTTP: supplier.Client(), FetchHTTP: supplier.Client()})
	require.NoError(t, err)
	runtime := &ocr.Runtime{DB: db, Billing: billing, Service: svc}
	SetOCRRouter(engine, &controller.OCRHandler{Runtime: runtime})
	idempotency := ""
	submit := func(path, key string) *http.Response {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		require.NoError(t, mw.WriteField("model", "public-ocr"))
		require.NoError(t, mw.WriteField("optionalPayload", `{"temperature":0,"useTableRecognition":true}`))
		f, err := mw.CreateFormFile("file", "doc.png")
		require.NoError(t, err)
		_, err = f.Write(img.Bytes())
		require.NoError(t, err)
		require.NoError(t, mw.Close())
		req, err := http.NewRequest("POST", server.URL+path, &body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if idempotency != "" {
			req.Header.Set("Idempotency-Key", idempotency)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}
	get := func(path, key string) *http.Response {
		req, err := http.NewRequest("GET", server.URL+path, nil)
		require.NoError(t, err)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}
	resp := submit("/api/v2/ocr/jobs", "")
	assert.Equal(t, 401, resp.StatusCode)
	resp.Body.Close()
	assert.Zero(t, sends.Load())
	resp = submit("/api/v2/ocr/jobs", token.Key)
	require.Equal(t, 200, resp.StatusCode)
	var envelope struct {
		Data ocr.JobStatus `json:"data"`
	}
	require.NoError(t, common.DecodeJson(resp.Body, &envelope))
	resp.Body.Close()
	id := envelope.Data.JobID
	require.NotEmpty(t, id)
	assert.NotEqual(t, "private-provider", id)
	assert.EqualValues(t, 2, sends.Load())
	resp = get("/api/v2/ocr/jobs/"+id, token.Key)
	require.Equal(t, 200, resp.StatusCode)
	require.NoError(t, common.DecodeJson(resp.Body, &envelope))
	resp.Body.Close()
	assert.Equal(t, "done", envelope.Data.State)
	resource, err := url.Parse(envelope.Data.ResultURL.JSONURL)
	require.NoError(t, err)
	resp = get(resource.RequestURI(), "")
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	assert.Contains(t, string(data), "9007199254740993")
	assert.NotContains(t, string(data), supplier.URL)
	var line struct {
		Result struct {
			Pages []struct {
				Width    *float64 `json:"width"`
				Height   *float64 `json:"height"`
				Index    *int     `json:"index"`
				Markdown struct {
					Images map[string]string `json:"images"`
				} `json:"markdown"`
			} `json:"layoutParsingResults"`
		} `json:"result"`
	}
	require.NoError(t, common.Unmarshal(data, &line))
	imageURL, err := url.Parse(line.Result.Pages[0].Markdown.Images["p.png"])
	require.NoError(t, err)
	resp = get(imageURL.RequestURI(), "")
	require.Equal(t, 200, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, img.Bytes(), b)
	resp = get("/v1/ocr/"+id, token.Key)
	require.Equal(t, 200, resp.StatusCode)
	var canonical struct {
		Asynchronous bool `json:"asynchronous"`
		Result       struct {
			Pages []struct {
				Width    *float64 `json:"width"`
				Height   *float64 `json:"height"`
				Index    *int     `json:"index"`
				Markdown struct {
					Text string `json:"text"`
				} `json:"markdown"`
				Layout []struct {
					Label   string    `json:"label"`
					Content string    `json:"content"`
					BBox    []float64 `json:"bbox"`
				} `json:"layout"`
			} `json:"pages"`
		} `json:"result"`
	}
	require.NoError(t, common.DecodeJson(resp.Body, &canonical))
	resp.Body.Close()
	assert.True(t, canonical.Asynchronous)
	require.Len(t, canonical.Result.Pages, 1)
	require.NotNil(t, canonical.Result.Pages[0].Width)
	assert.Equal(t, float64(100), *canonical.Result.Pages[0].Width)
	require.NotNil(t, canonical.Result.Pages[0].Height)
	assert.Equal(t, float64(200), *canonical.Result.Pages[0].Height)
	require.NotNil(t, canonical.Result.Pages[0].Index)
	assert.Zero(t, *canonical.Result.Pages[0].Index)
	assert.Equal(t, "Actual text ![](p.png)", canonical.Result.Pages[0].Markdown.Text)
	require.Len(t, canonical.Result.Pages[0].Layout, 2)
	assert.Equal(t, "table", canonical.Result.Pages[0].Layout[0].Label)
	assert.Equal(t, []float64{1, 2, 3, 4}, canonical.Result.Pages[0].Layout[0].BBox)
	assert.Equal(t, "x^2", canonical.Result.Pages[0].Layout[1].Content)
	// Same-user different-token is rejected before supplier poll.
	other := token
	other.Id = 0
	other.Key = strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, db.Create(&other).Error)
	before := polls.Load()
	resp = get("/api/v2/ocr/jobs/"+id, other.Key)
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()
	assert.Equal(t, before, polls.Load())
	// Current token model restrictions are revalidated on GET, even for a ready job.
	require.NoError(t, db.Model(&token).Update("model_limits", "other").Error)
	resp = get("/v1/ocr/"+id, token.Key)
	assert.Equal(t, 403, resp.StatusCode)
	resp.Body.Close()
	require.NoError(t, db.Model(&token).Update("model_limits", "public-ocr").Error)
	// Expired published output stays terminal after successful physical cleanup.
	oldLogDB, oldLogEnabled := model.LOG_DB, common.LogConsumeEnabled
	model.LOG_DB, common.LogConsumeEnabled = db, true
	t.Cleanup(func() { model.LOG_DB, common.LogConsumeEnabled = oldLogDB, oldLogEnabled })
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.OCRLogReceipt{}))
	require.NoError(t, billing.ProjectAuditLogs(context.Background(), db, 100))
	var receiptsBefore []model.OCRLogReceipt
	var logsBefore []model.Log
	require.NoError(t, db.Find(&receiptsBefore).Error)
	require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&logsBefore).Error)
	require.Len(t, logsBefore, 1)
	require.Equal(t, 100, logsBefore[0].Quota)
	require.NotEmpty(t, receiptsBefore)
	var settledBefore model.OCRJob
	require.NoError(t, db.First(&settledBefore, "id = ?", id).Error)
	require.Equal(t, "settled", settledBefore.BillingState)
	require.NoError(t, db.Model(&model.OCRResource{}).Where("job_id = ?", id).Update("expires_at", time.Now().Unix()-1).Error)
	beforeSends, beforePolls := sends.Load(), polls.Load()
	resp = get("/api/v2/ocr/jobs/"+id, token.Key)
	require.Equal(t, 410, resp.StatusCode)
	resp.Body.Close()
	require.NoError(t, svc.PurgeExpiredResources(context.Background(), 100))
	var remaining int64
	require.NoError(t, db.Model(&model.OCRResource{}).Where("job_id = ?", id).Count(&remaining).Error)
	require.Zero(t, remaining)
	for _, path := range []string{"/api/v2/ocr/jobs/" + id, "/v1/ocr/" + id} {
		resp = get(path, token.Key)
		assert.Equal(t, 410, resp.StatusCode)
		resp.Body.Close()
	}
	assert.Equal(t, beforeSends, sends.Load())
	assert.Equal(t, beforePolls, polls.Load())
	var settledAfter model.OCRJob
	require.NoError(t, db.First(&settledAfter, "id = ?", id).Error)
	assert.Equal(t, settledBefore, settledAfter)
	require.NoError(t, billing.ProjectAuditLogs(context.Background(), db, 100))
	var receiptsAfter []model.OCRLogReceipt
	var logsAfter []model.Log
	require.NoError(t, db.Find(&receiptsAfter).Error)
	require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&logsAfter).Error)
	assert.Equal(t, receiptsBefore, receiptsAfter)
	assert.Equal(t, logsBefore, logsAfter)
	var chargedUser model.User
	var chargedToken model.Token
	require.NoError(t, db.First(&chargedUser, u.Id).Error)
	require.NoError(t, db.First(&chargedToken, token.Id).Error)
	assert.Equal(t, 900, chargedUser.Quota)
	assert.Equal(t, 900, chargedToken.RemainQuota)
	// An intentional second create without idempotency creates a different paid job.
	resp = submit("/v1/ocr", token.Key)
	require.Equal(t, 202, resp.StatusCode)
	var created struct {
		ID           string `json:"id"`
		Asynchronous bool   `json:"asynchronous"`
	}
	require.NoError(t, common.DecodeJson(resp.Body, &created))
	resp.Body.Close()
	assert.NotEqual(t, id, created.ID)
	assert.True(t, created.Asynchronous)
	assert.EqualValues(t, 3, sends.Load())
	var balance model.User
	require.NoError(t, db.First(&balance, u.Id).Error)
	assert.Equal(t, 800, balance.Quota)
	// Stable keys repeat the same immutable creation without another send.
	idempotency = "stable-client-key"
	resp = submit("/v1/ocr", token.Key)
	require.Equal(t, 202, resp.StatusCode)
	var stable dto.OCRJobResponse
	require.NoError(t, common.DecodeJson(resp.Body, &stable))
	resp.Body.Close()
	stableSends := sends.Load()
	resp = submit("/v1/ocr", token.Key)
	require.Equal(t, 202, resp.StatusCode)
	var repeated dto.OCRJobResponse
	require.NoError(t, common.DecodeJson(resp.Body, &repeated))
	resp.Body.Close()
	assert.Equal(t, stable.ID, repeated.ID)
	assert.Equal(t, stableSends, sends.Load())
	idempotency = ""
	// Expired/wrong grants and bearer-as-grant do not expose resources.
	resp = get(resource.Path+"?token="+token.Key, "")
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()
	resp = get(strings.Replace(resource.RequestURI(), id, created.ID, 1), "")
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()
	// Expired capability is rejected by the public download route.
	require.NoError(t, db.Model(&model.OCRResourceGrant{}).Where("job_id = ?", id).Update("expires_at", 1).Error)
	resp = get(resource.RequestURI(), "")
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()
	// Declared media/async contracts reject before intent or funds.
	originalMetadata := route.Metadata
	for _, metadata := range []string{`{"async":false}`, `{"media_limits":{"max_pixels":1}}`, `{"media_limits":{"max_pages":0}}`} {
		require.NoError(t, db.Model(&route).Update("metadata", metadata).Error)
		before := sends.Load()
		resp = submit("/api/v2/ocr/jobs", token.Key)
		assert.Contains(t, []int{400, 403}, resp.StatusCode)
		resp.Body.Close()
		assert.Equal(t, before, sends.Load())
	}
	require.NoError(t, db.Model(&route).Update("metadata", originalMetadata).Error)
	// Disable the frozen target: status must not poll a newly resolved replacement.
	before = polls.Load()
	require.NoError(t, db.Model(&ch).Update("status", common.ChannelStatusManuallyDisabled).Error)
	resp = get("/v1/ocr/"+created.ID, token.Key)
	assert.Equal(t, 403, resp.StatusCode)
	resp.Body.Close()
	assert.Equal(t, before, polls.Load())
	require.NoError(t, db.Model(&ch).Update("status", common.ChannelStatusEnabled).Error)
	// Insufficient wallet fails before any supplier create.
	require.NoError(t, db.Model(&u).Update("quota", 1).Error)
	before = sends.Load()
	resp = submit("/v1/ocr", token.Key)
	assert.Equal(t, 402, resp.StatusCode)
	resp.Body.Close()
	assert.Equal(t, before, sends.Load())
	require.NoError(t, db.Model(&u).Update("quota", 800).Error)
	// The final available quota pays for creation but must not block owned reads.
	require.NoError(t, db.Model(&token).Updates(map[string]any{"remain_quota": 100, "status": common.TokenStatusEnabled}).Error)
	resp = submit("/v1/ocr", token.Key)
	require.Equal(t, 202, resp.StatusCode)
	var last dto.OCRJobResponse
	require.NoError(t, common.DecodeJson(resp.Body, &last))
	resp.Body.Close()
	resp = submit("/v1/ocr", token.Key)
	assert.Equal(t, 401, resp.StatusCode)
	resp.Body.Close()
	var exhausted model.Token
	require.NoError(t, db.First(&exhausted, token.Id).Error)
	assert.Equal(t, common.TokenStatusExhausted, exhausted.Status)
	resp = get("/v1/ocr/"+last.ID, token.Key)
	require.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
	for _, status := range []int{common.TokenStatusDisabled, common.TokenStatusExpired} {
		before := polls.Load()
		require.NoError(t, db.Model(&token).Update("status", status).Error)
		resp = get("/v1/ocr/"+last.ID, token.Key)
		assert.Equal(t, 401, resp.StatusCode)
		resp.Body.Close()
		assert.Equal(t, before, polls.Load())
	}
	require.NoError(t, db.Model(&token).Updates(map[string]any{"remain_quota": 1000, "status": common.TokenStatusEnabled}).Error)
	for _, change := range []map[string]any{
		{"group": "forbidden-group"}, {"expired_time": 1}, {"allow_ips": "192.0.2.0/24"}, {"purpose": "chat_embed", "bound_origin": "https://approved.example"},
	} {
		before := polls.Load()
		require.NoError(t, db.Model(&token).Updates(change).Error)
		resp = get("/v1/ocr/"+last.ID, token.Key)
		assert.Contains(t, []int{401, 403}, resp.StatusCode)
		resp.Body.Close()
		assert.Equal(t, before, polls.Load())
		require.NoError(t, db.Model(&token).Updates(map[string]any{"group": "", "expired_time": -1, "allow_ips": "", "purpose": "", "bound_origin": ""}).Error)
	}
	before = polls.Load()
	require.NoError(t, db.Delete(&token).Error)
	resp = get("/v1/ocr/"+last.ID, token.Key)
	assert.Equal(t, 401, resp.StatusCode)
	resp.Body.Close()
	assert.Equal(t, before, polls.Load())
	require.NoError(t, db.Unscoped().Model(&model.Token{}).Where("id = ?", token.Id).Update("deleted_at", nil).Error)
	// Unknown acceptance has one send, held quota, no worker replay/refund.
	mode.Store(1)
	before = sends.Load()
	resp = submit("/v1/ocr", token.Key)
	assert.Equal(t, 502, resp.StatusCode)
	resp.Body.Close()
	assert.Equal(t, before+1, sends.Load())
	var unknown model.OCRJob
	require.NoError(t, db.Where("submission_state = ?", "uncertain").First(&unknown).Error)
	_, err = runtime.RunBatch(context.Background(), "", 32)
	require.NoError(t, err)
	require.NoError(t, db.First(&unknown, "id = ?", unknown.ID).Error)
	assert.Equal(t, "uncertain", unknown.SubmissionState)
	assert.Empty(t, unknown.Decision)
	assert.Equal(t, before+1, sends.Load())
	// All cooled is 429 and does not create another financial intent.
	mode.Store(2)
	resp = submit("/v1/ocr", token.Key)
	resp.Body.Close()
	before = sends.Load()
	var jobCount int64
	require.NoError(t, db.Model(&model.OCRJob{}).Count(&jobCount).Error)
	resp = submit("/v1/ocr", token.Key)
	assert.Equal(t, 429, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
	resp.Body.Close()
	assert.Equal(t, before, sends.Load())
	var afterCount int64
	require.NoError(t, db.Model(&model.OCRJob{}).Count(&afterCount).Error)
	assert.Equal(t, jobCount, afterCount)
	// Declared oversized HTTP body is rejected before multipart parsing/auth.
	req := httptest.NewRequest("POST", "/v1/ocr", strings.NewReader("x"))
	req.ContentLength = ocr.MaxInputBytes + (65 << 10)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)

}
