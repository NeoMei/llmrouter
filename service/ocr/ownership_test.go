package ocr

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/crypto"
	"github.com/QuantumNous/new-api/relay/channel/gateway"
	base "github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOCRURLAdmissionUsesOwnedBytes(t *testing.T) {
	var svc *Service
	var supplier *httptest.Server
	document := pdfFixture(2)
	supplier = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/source" {
			_, _ = w.Write(document)
			return
		}
		var body struct {
			FileURL  string          `json:"fileUrl"`
			Model    string          `json:"model"`
			Optional map[string]bool `json:"optionalPayload"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &body))
		assert.Equal(t, "PaddleOCR-VL-1.6", body.Model)
		assert.NotContains(t, body.FileURL, supplier.URL)
		assert.Equal(t, document, openGrant(t, svc, body.FileURL))
		assert.Equal(t, false, body.Optional["useLayoutDetection"])
		_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"url-job"}}`))
	}))
	defer supplier.Close()
	var req CreateRequest
	svc, req = serviceFixture(t, supplier)
	req.Input = Input{FileURL: supplier.URL + "/source", OptionalPayload: []byte(`{"useLayoutDetection":false}`)}
	_, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	checkMoney(t, svc, req, 900, 100, 0)
	var count int64
	require.NoError(t, svc.db.Model(&model.OCRResource{}).Where("kind = ?", "input").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
func TestOCROwnerRemovalAndGatewayRefresh(t *testing.T) {
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("Authorization"))
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"owned"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"state":"running","extractProgress":{"totalPages":2,"extractedPages":1}}}`))
	}))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	ctx := context.Background()
	j, err := svc.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer A"}, headers)
	require.NoError(t, svc.db.Model(&model.Channel{}).Where("id = ?", req.Admission.ChannelID).Update("key", "replacement\nB").Error)
	_, err = svc.Status(ctx, j.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.Error(t, err)
	assert.Len(t, headers, 1)
	require.NoError(t, svc.db.AutoMigrate(&model.ProviderProfile{}, &model.ProviderKeyPool{}, &model.UserProviderKey{}))
	aes, err := crypto.NewAESGCM([]byte(strings.Repeat("x", 32)))
	require.NoError(t, err)
	svc.keys = gateway.NewKeySelector(svc.db, aes)
	profile := model.ProviderProfile{Name: uuid.NewString(), Status: "active"}
	require.NoError(t, svc.db.Create(&profile).Error)
	encrypted, err := aes.Encrypt([]byte("original"))
	require.NoError(t, err)
	key := model.ProviderKeyPool{ProfileId: profile.Id, Status: "active", EncryptedKey: encrypted}
	require.NoError(t, svc.db.Create(&key).Error)
	require.NoError(t, svc.db.Model(&model.Channel{}).Where("id = ?", req.Admission.ChannelID).Update("type", constant.ChannelTypeGateway).Error)
	req.Admission.IdempotencyKey = uuid.NewString()
	req.Target.ProfileID = profile.Id
	created, err := svc.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "Bearer original", headers[len(headers)-1])
	refreshed, err := aes.Encrypt([]byte("refreshed"))
	require.NoError(t, err)
	require.NoError(t, svc.db.Model(&key).Update("encrypted_key", refreshed).Error)
	progress, err := svc.Status(ctx, created.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.NoError(t, err)
	assert.Equal(t, "running", progress.State)
	assert.Equal(t, 1, progress.ExtractProgress.ExtractedPages)
	assert.Equal(t, "Bearer refreshed", headers[len(headers)-1])
	require.NoError(t, svc.db.Delete(&key).Error)
	before := len(headers)
	_, err = svc.Status(ctx, created.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.Error(t, err)
	assert.Len(t, headers, before)
	byok := model.UserProviderKey{UserId: req.Admission.UserID, ProfileId: profile.Id, Status: "active", EncryptedKey: encrypted}
	require.NoError(t, svc.db.Create(&byok).Error)
	req.Admission.IdempotencyKey = uuid.NewString()
	_, err = svc.Create(ctx, req)
	require.ErrorContains(t, err, "platform fallback forbidden")
	assert.Len(t, headers, before)
}
func TestOCRConcurrentDeliveryAndDeliveryFailureCharged(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%v", malformed), func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/ocr/jobs":
					_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"owned"}}`))
				case "/api/v2/ocr/jobs/owned":
					fmt.Fprintf(w, `{"code":0,"data":{"state":"done","extractProgress":{"totalPages":2,"extractedPages":2},"resultUrl":{"jsonUrl":%q}}}`, server.URL+"/result")
				case "/result":
					if malformed {
						_, _ = w.Write([]byte("invalid"))
						return
					}
					_, _ = w.Write([]byte(`{"result":{"layoutParsingResults":[{"markdown":{"text":"one"}},{"markdown":{"text":"two"}}]}}`))
				}
			}))
			defer server.Close()
			svc, req := serviceFixture(t, server)
			ctx := context.Background()
			j, err := svc.Create(ctx, req)
			require.NoError(t, err)
			var wg sync.WaitGroup
			responses := make([]JobStatus, 3)
			errs := make([]error, 3)
			for i := range responses {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					responses[i], errs[i] = svc.Status(ctx, j.JobID, req.Admission.UserID, req.Admission.TokenID)
				}(i)
			}
			wg.Wait()
			for i, err := range errs {
				if malformed {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					assert.Equal(t, responses[0].ResultURL.JSONURL, responses[i].ResultURL.JSONURL)
					assert.NotEmpty(t, openGrant(t, svc, responses[i].ResultURL.JSONURL))
				}
			}
			checkMoney(t, svc, req, 900, 100, 1)
			var count int64
			require.NoError(t, svc.db.Model(&model.OCRResource{}).Count(&count).Error)
			if malformed {
				assert.Zero(t, count)
			} else {
				assert.EqualValues(t, 1, count)
			}
		})
	}
}
func TestOCRProtectedDownloadRejectsRedirectAndBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oversize" {
			w.Header().Set("Content-Length", "99999999")
			return
		}
		http.Redirect(w, r, "http://127.0.0.1/secret", 302)
	}))
	defer server.Close()
	svc, _ := serviceFixture(t, server)
	_, err := svc.fetch(context.Background(), server.URL+"/oversize", 10)
	require.Error(t, err)
	setting := system_setting.GetFetchSetting()
	old := *setting
	t.Cleanup(func() { *setting = old })
	setting.EnableSSRFProtection = true
	setting.AllowPrivateIp = false
	setting.AllowedPorts = []string{"80", "443"}
	setting.ApplyIPFilterForDomain = true
	// Actual production protected client rejects private endpoints before dialing.
	base.InitHttpClient()
	svc.fetchClient = base.GetSSRFProtectedHTTPClient()
	_, err = svc.fetch(context.Background(), "http://127.0.0.1/secret", 10)
	require.Error(t, err)
	// Inject only the first round trip, preserving production redirect validator.
	client := *svc.fetchClient
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1/secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	svc.fetchClient = &client
	_, err = svc.fetch(context.Background(), "http://public.example/document", 10)
	require.Error(t, err)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOCRTransportTimeoutNeverReplayed(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := h.Hijack()
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	}))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	_, err := svc.Create(context.Background(), req)
	require.Error(t, err)
	_, err = svc.Create(context.Background(), req)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load())
	checkMoney(t, svc, req, 900, 100, 0)
}

func TestOCRImagePixelOverflowAndGrantRevocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP call") }))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	bomb := pngFixture(t)
	binary.BigEndian.PutUint32(bomb[16:20], 0xffffffff)
	binary.BigEndian.PutUint32(bomb[20:24], 0xffffffff)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	_, err := svc.validateInput(context.Background(), Input{File: bomb})
	require.Error(t, err)
	job, err := svc.billing.CreateIntent(context.Background(), req.Admission)
	require.NoError(t, err)
	r, err := svc.storeResource(context.Background(), job.ID, "input", "application/pdf", pdfFixture(2), time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	require.NoError(t, svc.billing.PublishOCRInput(context.Background(), r))
	token := svc.grant(r, r.ExpiresAt)
	require.NoError(t, svc.db.Model(&model.OCRResource{}).Where("id = ?", r.ID).Update("revoked", true).Error)
	_, _, err = svc.OpenGrantedResource(context.Background(), job.ID, r.ID, token)
	require.ErrorIs(t, err, model.ErrOCRGrant)
}

func TestOCRConcurrentIdempotentCreateAndNoRepeatBudget(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint(refuse), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if refuse {
					w.WriteHeader(429)
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"only-once"}}`))
			}))
			defer server.Close()
			svc, req := serviceFixture(t, server)
			ctx := context.Background()
			if refuse {
				_, err := svc.Create(ctx, req)
				require.Error(t, err)
				assert.EqualValues(t, 2, calls.Load())
				_, err = svc.Create(ctx, req)
				require.NoError(t, err)
				assert.EqualValues(t, 2, calls.Load())
				checkMoney(t, svc, req, 1000, 0, 0)
				return
			}
			var wg sync.WaitGroup
			states := make([]JobStatus, 2)
			errs := make([]error, 2)
			for i := range states {
				wg.Add(1)
				go func(i int) { defer wg.Done(); states[i], errs[i] = svc.Create(ctx, req) }(i)
			}
			wg.Wait()
			for _, err := range errs {
				require.NoError(t, err)
			}
			assert.Equal(t, states[0].JobID, states[1].JobID)
			assert.EqualValues(t, 1, calls.Load())
			checkMoney(t, svc, req, 900, 100, 0)
		})
	}
}

func TestOCRRenewableGrantProjectionAfterRestartAndRetention(t *testing.T) {
	var server *httptest.Server
	img := pngFixture(t)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/ocr/jobs":
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"owned"}}`))
		case "/api/v2/ocr/jobs/owned":
			fmt.Fprintf(w, `{"code":0,"data":{"state":"done","extractProgress":{"totalPages":2,"extractedPages":2},"resultUrl":{"jsonUrl":%q}}}`, server.URL+"/result")
		case "/result":
			fmt.Fprintf(w, `{"result":{"layoutParsingResults":[{"markdown":{"text":"![](pic.png)","images":{"pic.png":%q}}},{"markdown":{"text":"two"}}]}}`, server.URL+"/image")
		case "/image":
			_, _ = w.Write(img)
		}
	}))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	ctx := context.Background()
	clock := time.Now()
	svc.now = func() time.Time { return clock }
	j, err := svc.Create(ctx, req)
	require.NoError(t, err)
	done, err := svc.Status(ctx, j.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.NoError(t, err)
	oldURL := done.ResultURL.JSONURL
	oldJSON := openGrant(t, svc, oldURL)
	clock = clock.Add(2 * time.Hour)
	restarted, err := NewService(svc.db, svc.billing, Config{PublicOrigin: svc.publicOrigin, GrantSecret: svc.grantSecret, Objects: svc.objects, SupplierHTTP: server.Client(), FetchHTTP: server.Client(), Now: func() time.Time { return clock }})
	require.NoError(t, err)
	u, err := url.Parse(oldURL)
	require.NoError(t, err)
	parts := strings.Split(u.Path, "/")
	_, _, err = restarted.OpenGrantedResource(ctx, j.JobID, parts[6], u.Query().Get("token"))
	require.ErrorIs(t, err, model.ErrOCRGrant)
	fresh, err := restarted.Status(ctx, j.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.NoError(t, err)
	assert.NotEqual(t, oldURL, fresh.ResultURL.JSONURL)
	freshJSON := openGrant(t, restarted, fresh.ResultURL.JSONURL)
	assert.NotEqual(t, oldJSON, freshJSON)
	var page struct {
		Result struct {
			Pages []struct {
				Markdown struct {
					Images map[string]string `json:"images"`
				} `json:"markdown"`
			} `json:"layoutParsingResults"`
		} `json:"result"`
	}
	require.NoError(t, common.Unmarshal(freshJSON, &page))
	assert.Equal(t, img, openGrant(t, restarted, page.Result.Pages[0].Markdown.Images["pic.png"]))
	// Repeated status in one issuance bucket neither grows rows nor revokes URLs.
	again, err := restarted.Status(ctx, j.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.NoError(t, err)
	assert.Equal(t, fresh.ResultURL.JSONURL, again.ResultURL.JSONURL)
	var grants int64
	require.NoError(t, svc.db.Model(&model.OCRResourceGrant{}).Count(&grants).Error)
	assert.EqualValues(t, 2, grants)
	checkMoney(t, svc, req, 900, 100, 1)
	clock = clock.Add(8 * 24 * time.Hour)
	_, err = restarted.Status(ctx, j.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.ErrorIs(t, err, model.ErrOCRGrant)
	require.NoError(t, restarted.PurgeExpiredResources(ctx, 100))
	var resources int64
	require.NoError(t, svc.db.Model(&model.OCRResource{}).Count(&resources).Error)
	assert.Zero(t, resources)
}
