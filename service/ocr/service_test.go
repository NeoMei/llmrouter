package ocr

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
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
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func pngFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return b.Bytes()
}

// Generates a structurally valid page tree and xref. The parser must traverse
// actual PDF structure; a fake /Count in a comment is not admission evidence.
func pdfFixture(pages int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n% fixture\n")
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	kids := []string{}
	for i := 0; i < pages; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", i+3))
		objects = append(objects, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>")
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", pages, strings.Join(kids, " "))
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, start)
	return b.Bytes()
}
func serviceFixture(t *testing.T, supplier *httptest.Server) (*Service, CreateRequest) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "service.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.OCRJob{}, &model.OCROperation{}, &model.OCRAuditEvent{}, &model.OCRResource{}, &model.OCRResourceGrant{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	user := model.User{Username: uuid.NewString(), AffCode: uuid.NewString(), Quota: 1000, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: uuid.NewString(), RemainQuota: 1000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	ch := model.Channel{Type: constant.ChannelTypePaddleOCR, Name: "fixture", Key: "A\nB", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	require.NoError(t, db.Create(&ch).Error)
	oldPrice, oldRetry := ratio_setting.ModelPrice2JSONString(), common.RetryTimes
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"fixture-ocr":0.0002}`))
	common.RetryTimes = 1
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrice))
		common.RetryTimes = oldRetry
	})
	objects, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)
	billing := &Billing{model.NewOCRBillingStore(db, nil)}
	t.Cleanup(func() { require.NoError(t, billing.Close()) })
	svc, err := NewService(db, billing, Config{PublicOrigin: "https://owned.example", GrantSecret: bytes.Repeat([]byte{3}, 32), Objects: objects, SupplierHTTP: supplier.Client(), FetchHTTP: supplier.Client()})
	require.NoError(t, err)
	return svc, CreateRequest{Admission: AuthorizedAdmission{UserID: user.Id, TokenID: token.Id, ChannelID: ch.Id, IdempotencyKey: uuid.NewString(), Model: "fixture-ocr", UserGroup: "default", UsingGroup: "default", BillingPreference: "wallet_only"}, Target: Target{JobsURL: supplier.URL + "/api/v2/ocr/jobs", Model: "PaddleOCR-VL-1.6"}, Input: Input{File: pdfFixture(2), Filename: "fixture.pdf", OptionalPayload: []byte(`{"useTableRecognition":true}`)}}
}
func checkMoney(t *testing.T, s *Service, r CreateRequest, wallet, used, requests int) {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, s.db.First(&user, r.Admission.UserID).Error)
	require.NoError(t, s.db.First(&token, r.Admission.TokenID).Error)
	assert.Equal(t, wallet, user.Quota)
	assert.Equal(t, wallet, token.RemainQuota)
	assert.Equal(t, used, token.UsedQuota)
	assert.Equal(t, requests, user.RequestCount)
}
func openGrant(t *testing.T, s *Service, raw string) []byte {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	parts := strings.Split(u.Path, "/")
	require.Len(t, parts, 7)
	body, info, err := s.OpenGrantedResource(context.Background(), parts[4], parts[6], u.Query().Get("token"))
	require.NoError(t, err)
	defer body.Close()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), info.Size)
	return data
}
func TestOCRLifecycleOwnedResultsRefusalRotation(t *testing.T) {
	var submit, poll atomic.Int32
	var server *httptest.Server
	imageBytes := pngFixture(t)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/ocr/jobs":
			submit.Add(1)
			require.NoError(t, r.ParseMultipartForm(MaxInputBytes))
			assert.Equal(t, "PaddleOCR-VL-1.6", r.FormValue("model"))
			if r.Header.Get("Authorization") == "Bearer A" {
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"code":429,"msg":"fixture limit"}`))
				return
			}
			assert.Equal(t, "Bearer B", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"provider-owned"}}`))
		case "/api/v2/ocr/jobs/provider-owned":
			poll.Add(1)
			assert.Equal(t, "Bearer B", r.Header.Get("Authorization"))
			fmt.Fprintf(w, `{"code":0,"data":{"state":"done","extractProgress":{"totalPages":2,"extractedPages":2},"resultUrl":{"jsonUrl":%q}}}`, server.URL+"/results")
		case "/results":
			assert.Empty(t, r.Header.Get("Authorization"))
			fmt.Fprintf(w, `{"extension":{"n":9007199254740993},"result":{"layoutParsingResults":[{"markdown":{"text":"table formula ![](images/x.png)","images":{"images/x.png":%q}},"table_res_list":[{"html":"<table/>"}],"formula_res_list":[{"latex":"x^2"}]},{"markdown":{"text":"page two","images":{}},"unknown":{"retained":true}}]}}`+"\n", server.URL+"/image")
		case "/image":
			assert.Empty(t, r.Header.Get("Authorization"))
			_, _ = w.Write(imageBytes)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	ctx := context.Background()
	status, err := svc.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(2), submit.Load())
	assert.NotContains(t, status.JobID, "provider")
	checkMoney(t, svc, req, 900, 100, 0)
	again, err := svc.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, status.JobID, again.JobID)
	assert.Equal(t, int32(2), submit.Load())
	_, err = svc.Status(ctx, status.JobID, req.Admission.UserID+1, req.Admission.TokenID)
	require.Error(t, err)
	_, err = svc.Status(ctx, status.JobID, req.Admission.UserID, req.Admission.TokenID+1)
	require.Error(t, err)
	assert.Zero(t, poll.Load())
	// Native ownership survives key reorder, without rotating to A.
	require.NoError(t, svc.db.Model(&model.Channel{}).Where("id = ?", req.Admission.ChannelID).Update("key", "B\nA").Error)
	done, err := svc.Status(ctx, status.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.NoError(t, err)
	assert.Equal(t, "done", done.State)
	raw := openGrant(t, svc, done.ResultURL.JSONURL)
	assert.Contains(t, string(raw), `9007199254740993`)
	assert.Contains(t, string(raw), `"table_res_list"`)
	assert.Contains(t, string(raw), `"formula_res_list"`)
	assert.Contains(t, string(raw), `"retained":true`)
	var row struct {
		Result struct {
			Pages []struct {
				Markdown struct {
					Images map[string]string `json:"images"`
				} `json:"markdown"`
			} `json:"layoutParsingResults"`
		} `json:"result"`
	}
	require.NoError(t, common.Unmarshal(bytes.TrimSpace(raw), &row))
	require.Len(t, row.Result.Pages, 2)
	ownedImage := row.Result.Pages[0].Markdown.Images["images/x.png"]
	assert.Equal(t, imageBytes, openGrant(t, svc, ownedImage))
	assert.NotContains(t, string(raw), server.URL)
	for i := 0; i < 2; i++ {
		repeated, err := svc.Status(ctx, status.JobID, req.Admission.UserID, req.Admission.TokenID)
		require.NoError(t, err)
		assert.Equal(t, done.ResultURL.JSONURL, repeated.ResultURL.JSONURL)
	}
	checkMoney(t, svc, req, 900, 100, 1)
	assert.Equal(t, int32(1), poll.Load())
	// A new request skips A's durable cooldown and spends only B.
	req.Admission.IdempotencyKey = uuid.NewString()
	_, err = svc.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(3), submit.Load())
	restarted, err := NewService(svc.db, svc.billing, Config{PublicOrigin: svc.publicOrigin, GrantSecret: svc.grantSecret, Objects: svc.objects, SupplierHTTP: server.Client(), FetchHTTP: server.Client()})
	require.NoError(t, err)
	assert.Equal(t, raw, openGrant(t, restarted, done.ResultURL.JSONURL))
	u, err := url.Parse(done.ResultURL.JSONURL)
	require.NoError(t, err)
	parts := strings.Split(u.Path, "/")
	_, _, err = restarted.OpenGrantedResource(ctx, uuid.NewString(), parts[6], u.Query().Get("token"))
	require.ErrorIs(t, err, model.ErrOCRGrant)
	_, _, err = restarted.OpenGrantedResource(ctx, status.JobID, uuid.NewString(), u.Query().Get("token"))
	require.ErrorIs(t, err, model.ErrOCRGrant)
	require.NoError(t, svc.db.Model(&model.OCRResource{}).Where("id = ?", parts[6]).Update("expires_at", time.Now().Unix()-1).Error)
	_, _, err = restarted.OpenGrantedResource(ctx, status.JobID, parts[6], u.Query().Get("token"))
	require.ErrorIs(t, err, model.ErrOCRGrant)
}
func TestOCRUnknownCreateNoReplayAndFailedRefund(t *testing.T) {
	for _, mode := range []string{"unknown", "failed", "veto", "code"} {
		t.Run(mode, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					count.Add(1)
					switch mode {
					case "unknown":
						w.WriteHeader(503)
					case "veto":
						w.Header().Set("X-Should-Retry", "false")
						w.WriteHeader(429)
					case "code":
						_, _ = w.Write([]byte(`{"code":7,"data":{"jobId":"ignored"}}`))
					default:
						_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"failed-job"}}`))
					}
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"state":"failed"}}`))
			}))
			defer server.Close()
			svc, req := serviceFixture(t, server)
			ctx := context.Background()
			status, err := svc.Create(ctx, req)
			if mode == "failed" {
				require.NoError(t, err)
				for i := 0; i < 2; i++ {
					out, err := svc.Status(ctx, status.JobID, req.Admission.UserID, req.Admission.TokenID)
					require.NoError(t, err)
					assert.Equal(t, "failed", out.State)
				}
				checkMoney(t, svc, req, 1000, 0, 0)
			} else {
				require.Error(t, err)
				_, err = svc.Create(ctx, req)
				require.NoError(t, err)
				if mode == "veto" {
					checkMoney(t, svc, req, 1000, 0, 0)
				} else {
					checkMoney(t, svc, req, 900, 100, 0)
				}
			}
			assert.Equal(t, int32(1), count.Load())
		})
	}
}
func TestOCRAdmissionBoundsBeforeCharge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("supplier must not be called") }))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	for _, input := range []Input{{File: []byte("%PDF-1.7\n/Count 1")}, {File: pdfFixture(MaxPages + 1)}, {File: []byte("not-an-image")}, {File: pngFixture(t), OptionalPayload: []byte(`{"pageRanges":"1"}`)}, {File: pngFixture(t), OptionalPayload: []byte(`{"useTableRecognition":0}`)}, {File: bytes.Repeat([]byte("x"), MaxInputBytes+1)}, {FileURL: "file:///etc/passwd"}} {
		req.Input = input
		_, err := svc.Create(context.Background(), req)
		require.Error(t, err)
	}
	var jobs int64
	require.NoError(t, svc.db.Model(&model.OCRJob{}).Count(&jobs).Error)
	assert.Zero(t, jobs)
	checkMoney(t, svc, req, 1000, 0, 0)
	valid, err := svc.validateInput(context.Background(), Input{File: pdfFixture(2)})
	require.NoError(t, err)
	assert.Equal(t, 2, valid.pages)
}

func TestOCRPinnedConsumerOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(MaxInputBytes))
		assert.Contains(t, r.FormValue("optionalPayload"), `"temperature":0`)
		assert.Contains(t, r.FormValue("optionalPayload"), `"useOcrForImageBlock":false`)
		_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"pinned"}}`))
	}))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	req.Input.OptionalPayload = []byte(`{"markdownIgnoreLabels":["header","header_image","footer","footer_image","number","footnote","aside_text"],"useDocOrientationClassify":false,"useDocUnwarping":false,"useLayoutDetection":true,"useChartRecognition":false,"useSealRecognition":false,"useOcrForImageBlock":false,"mergeTables":true,"relevelTitles":true,"restructurePages":true,"layoutShapeMode":"auto","promptLabel":"ocr","layoutNms":true,"repetitionPenalty":1,"temperature":0,"topP":1,"minPixels":147384,"maxPixels":2822400}`)
	_, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
}
