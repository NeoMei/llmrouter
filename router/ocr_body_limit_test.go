package router

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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

func TestOCRPublicFullRequestByteAdmission(t *testing.T) {
	enableNativeCompatibility(t)
	gin.SetMode(gin.TestMode)
	var sends, fetches, chunkedRequests atomic.Int32
	var img bytes.Buffer
	require.NoError(t, png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image":
			fetches.Add(1)
			_, _ = w.Write(img.Bytes())
		case "/api/v2/ocr/jobs":
			sends.Add(1)
			fmt.Fprintf(w, `{"code":0,"data":{"jobId":"owned-%d"}}`, sends.Load())
		default:
			t.Errorf("unexpected supplier request %s", r.URL.Path)
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
	engine.Use(func(c *gin.Context) {
		if c.GetHeader("X-Fixture-Unknown-Length") == "true" {
			assert.Equal(t, int64(-1), c.Request.ContentLength)
			assert.Contains(t, c.Request.TransferEncoding, "chunked")
			chunkedRequests.Add(1)
		}
		c.Next()
	})
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

	var multipartBody bytes.Buffer
	mw := multipart.NewWriter(&multipartBody)
	require.NoError(t, mw.SetBoundary("ocr-fixture-boundary"))
	require.NoError(t, mw.WriteField("model", "public-ocr"))
	require.NoError(t, mw.WriteField("optionalPayload", `{"temperature":0,"markdownIgnoreLabels":["header","footer"]}`))
	file, err := mw.CreateFormFile("file", "tiny.png")
	require.NoError(t, err)
	_, err = file.Write(img.Bytes())
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	multipartBytes := multipartBody.Bytes()
	jsonBytes, err := common.Marshal(dto.OCRCreateRequest{Model: "public-ocr", FileURL: supplier.URL + "/image", OptionalPayload: []byte(`{"temperature":0}`)})
	require.NoError(t, err)
	// Bytes after closing boundary must count even when the multipart parser
	// stops early. >4KiB defeats its normal buffered read-ahead.
	withEpilogue := append(append([]byte(nil), multipartBytes...), bytes.Repeat([]byte("x"), 8192)...)
	overGlobal := append(append([]byte(nil), multipartBytes...), bytes.Repeat([]byte("x"), ocr.MaxInputBytes+(64<<10))...)
	for _, tc := range []struct {
		name, contentType string
		body              []byte
		limit             int
		unknown           bool
		want              int
	}{
		{"multipart_overhead_and_options", mw.FormDataContentType(), multipartBytes, img.Len() + 1, false, 400},
		{"multipart_optional_payload_bytes", mw.FormDataContentType(), multipartBytes, len(multipartBytes) - len(`{"temperature":0,"markdownIgnoreLabels":["header","footer"]}`), false, 400},
		{"json_wrapper", "application/json", jsonBytes, len(jsonBytes) - 1, false, 400},
		{"chunked_multipart_epilogue", mw.FormDataContentType(), withEpilogue, len(multipartBytes), true, 400},
		{"chunked_global_cap_epilogue", mw.FormDataContentType(), overGlobal, len(overGlobal) + 1, true, 400},
		{"multipart_exact_boundary", mw.FormDataContentType(), multipartBytes, len(multipartBytes), false, 202},
		{"chunked_epilogue_exact_boundary", mw.FormDataContentType(), withEpilogue, len(withEpilogue), true, 202},
		{"json_exact_boundary", "application/json", jsonBytes, len(jsonBytes), true, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Greater(t, tc.limit, img.Len(), "media alone is valid under model byte limit")
			require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("id = ?", route.Id).Update("metadata", fmt.Sprintf(`{"media_limits":{"max_request_bytes":%d}}`, tc.limit)).Error)
			var beforeCount int64
			require.NoError(t, db.Model(&model.OCRJob{}).Count(&beforeCount).Error)
			var beforeUser model.User
			var beforeToken model.Token
			require.NoError(t, db.First(&beforeUser, u.Id).Error)
			require.NoError(t, db.First(&beforeToken, token.Id).Error)
			beforeSends, beforeFetches, beforeChunked := sends.Load(), fetches.Load(), chunkedRequests.Load()
			var beforeMembers, beforeResources, beforeOperations int64
			require.NoError(t, db.Model(&model.UpstreamAccountMember{}).Count(&beforeMembers).Error)
			require.NoError(t, db.Model(&model.OCRResource{}).Count(&beforeResources).Error)
			require.NoError(t, db.Model(&model.OCROperation{}).Count(&beforeOperations).Error)
			req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ocr", bytes.NewReader(tc.body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Authorization", "Bearer "+token.Key)
			if tc.unknown {
				req.Header.Set("X-Fixture-Unknown-Length", "true")
				req.Body = io.NopCloser(bytes.NewReader(tc.body))
				req.ContentLength = -1
				req.GetBody = nil
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			response, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.StatusCode, string(response))
			if tc.unknown {
				assert.Equal(t, beforeChunked+1, chunkedRequests.Load(), "real server received chunked body")
			}
			var afterCount int64
			require.NoError(t, db.Model(&model.OCRJob{}).Count(&afterCount).Error)
			var afterUser model.User
			var afterToken model.Token
			require.NoError(t, db.First(&afterUser, u.Id).Error)
			require.NoError(t, db.First(&afterToken, token.Id).Error)
			if tc.want == 400 {
				var afterMembers, afterResources, afterOperations int64
				require.NoError(t, db.Model(&model.UpstreamAccountMember{}).Count(&afterMembers).Error)
				require.NoError(t, db.Model(&model.OCRResource{}).Count(&afterResources).Error)
				require.NoError(t, db.Model(&model.OCROperation{}).Count(&afterOperations).Error)
				assert.Equal(t, beforeMembers, afterMembers, "no account selection membership mutation")
				assert.Equal(t, beforeResources, afterResources, "no input object publication")
				assert.Equal(t, beforeOperations, afterOperations, "no financial operation intent")
				assert.Equal(t, beforeCount, afterCount)
				assert.Equal(t, beforeUser.Quota, afterUser.Quota)
				assert.Equal(t, beforeToken.RemainQuota, afterToken.RemainQuota)
				assert.Equal(t, beforeSends, sends.Load())
				assert.Equal(t, beforeFetches, fetches.Load(), "oversize body rejected before media fetch")
			} else {
				assert.Equal(t, beforeCount+1, afterCount)
				assert.Equal(t, beforeSends+1, sends.Load())
				assert.Equal(t, beforeUser.Quota-100, afterUser.Quota)
			}
		})
	}
}
