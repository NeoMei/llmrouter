package ocr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOCRPublicContractBeforeMoney(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("rejected contract reached supplier") }))
	defer server.Close()
	s, req := serviceFixture(t, server)
	no := false
	one := 1
	small := int64(1)
	for _, caps := range []dto.ModelCapabilities{
		{Async: &no}, {Operations: []string{"chat.completions.create"}}, {InputModalities: []string{"image"}}, {OutputModalities: []string{"audio"}},
		{MediaLimits: &dto.MediaLimits{MaxPages: &one}}, {MediaLimits: &dto.MediaLimits{MaxRequestBytes: &small}}, {MediaLimits: &dto.MediaLimits{Formats: []string{"png"}}},
	} {
		req.Capabilities = caps
		_, err := s.Create(context.Background(), req)
		require.Error(t, err)
		var count int64
		require.NoError(t, s.db.Model(&model.OCRJob{}).Count(&count).Error)
		assert.Zero(t, count)
		checkMoney(t, s, req, 1000, 0, 0)
	}
	req.Input.File = pngFixture(t)
	req.Capabilities = dto.ModelCapabilities{MediaLimits: &dto.MediaLimits{MaxPixels: &small}}
	_, err := s.Create(context.Background(), req)
	require.Error(t, err)
}

func TestOCRWorkerOnlyRecoversAcceptedAndTerminalJobs(t *testing.T) {
	var calls int
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"owned"}}`))
		} else {
			_, _ = w.Write([]byte(`{"code":0,"data":{"state":"failed"}}`))
		}
	}))
	defer supplier.Close()
	s, req := serviceFixture(t, supplier)
	accepted, err := s.Create(context.Background(), req)
	require.NoError(t, err)
	// Independent services after restart share the durable job and exact billing.
	restarted, err := NewService(s.db, s.billing, Config{PublicOrigin: s.publicOrigin, GrantSecret: s.grantSecret, Objects: s.objects, SupplierHTTP: supplier.Client(), FetchHTTP: supplier.Client()})
	require.NoError(t, err)
	runtime := &Runtime{DB: s.db, Billing: s.billing, Service: restarted}
	_, err = runtime.RunBatch(context.Background(), "", 32)
	require.NoError(t, err)
	snapshot, err := s.billing.Inspect(context.Background(), accepted.JobID)
	require.NoError(t, err)
	assert.Equal(t, "refunded", snapshot.Job.BillingState)
	_, err = runtime.RunBatch(context.Background(), "", 32)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	checkMoney(t, s, req, 1000, 0, 0)
	// No provider interaction for unknown/submitting/retryable-refusal rows.
	for _, state := range []string{"uncertain", "submitting", "rejected"} {
		j := snapshot.Job
		j.ID = state
		j.IdempotencyKey = state
		j.SubmissionState = state
		j.ProviderJobID = ""
		j.Decision = ""
		j.BillingState = "reserved"
		j.ResultResourceID = ""
		j.RetryRefusal = true
		require.NoError(t, s.db.Create(&j).Error)
	}
	_, err = runtime.RunBatch(context.Background(), "", 32)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runtime.RunBatch(ctx, "", 32)
	require.ErrorIs(t, err, context.Canceled)
	runtime.Start()
	runtime.Start()
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
	runtime.Start()
}

func TestOCRRuntimeConfigurationIsExplicit(t *testing.T) {
	t.Setenv("OCR_PUBLIC_ORIGIN", "")
	t.Setenv("OCR_GRANT_SECRET", "")
	r, err := RuntimeFromEnv(nil)
	require.NoError(t, err)
	assert.Nil(t, r)
	t.Setenv("OCR_PUBLIC_ORIGIN", "https://owned.example")
	r, err = RuntimeFromEnv(nil)
	require.Error(t, err)
	assert.Nil(t, r)
}

func TestOCRExpiredDeliveryStillSettlesWithoutFetching(t *testing.T) {
	calls := 0
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"owned"}}`))
		} else {
			_, _ = w.Write([]byte(`{"code":0,"data":{"state":"done","extractProgress":{"totalPages":2,"extractedPages":2},"resultUrl":{"jsonUrl":"https://must-not-fetch.example/result"}}}`))
		}
	}))
	defer supplier.Close()
	s, req := serviceFixture(t, supplier)
	job, err := s.Create(context.Background(), req)
	require.NoError(t, err)
	now := s.now()
	s.now = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	runtime := &Runtime{DB: s.db, Billing: s.billing, Service: s}
	_, err = runtime.RunBatch(context.Background(), "", 32)
	require.NoError(t, err)
	snapshot, err := s.billing.Inspect(context.Background(), job.JobID)
	require.NoError(t, err)
	assert.Equal(t, "settled", snapshot.Job.BillingState)
	assert.Empty(t, snapshot.Job.ResultResourceID)
	assert.Equal(t, 2, calls)
	_, err = runtime.RunBatch(context.Background(), "", 32)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	_, err = s.Status(context.Background(), job.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.ErrorIs(t, err, model.ErrOCRGrant)
	assert.Equal(t, 2, calls)
	checkMoney(t, s, req, 900, 100, 1)
}

type boundedCleanupStore struct {
	objectstore.Store
	bounded bool
}

func (s *boundedCleanupStore) Delete(ctx context.Context, key string) error {
	_, s.bounded = ctx.Deadline()
	return s.Store.Delete(ctx, key)
}
func TestOCRFailedDeliveryCleanupHasDeadline(t *testing.T) {
	var supplier *httptest.Server
	img := pngFixture(t)
	supplier = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/ocr/jobs":
			_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"owned"}}`))
		case "/api/v2/ocr/jobs/owned":
			fmt.Fprintf(w, `{"code":0,"data":{"state":"done","resultUrl":{"jsonUrl":%q}}}`, supplier.URL+"/result")
		case "/result":
			fmt.Fprintf(w, "{\"result\":{\"layoutParsingResults\":[{\"markdown\":{\"images\":{\"p.png\":%q}}}]}}\ninvalid", supplier.URL+"/image")
		case "/image":
			_, _ = w.Write(img)
		}
	}))
	defer supplier.Close()
	s, req := serviceFixture(t, supplier)
	store := &boundedCleanupStore{Store: s.objects}
	s.objects = store
	j, err := s.Create(context.Background(), req)
	require.NoError(t, err)
	_, err = s.Status(context.Background(), j.JobID, req.Admission.UserID, req.Admission.TokenID)
	require.Error(t, err)
	assert.True(t, store.bounded)
}
