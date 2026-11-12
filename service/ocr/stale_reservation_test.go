package ocr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A submission the supplier never answered keeps the reservation. Nothing else
// picks the job up: it has no provider job id and no decision, so the stale sweep
// is the only path that gives the quota back.
func TestOCRTimedOutSubmissionIsReleasedAndNeverCharged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	var job model.OCRJob
	require.NoError(t, svc.db.Where("user_id = ?", req.Admission.UserID).First(&job).Error)

	require.Equal(t, "reserved", job.BillingState)
	require.Empty(t, job.ProviderJobID)
	require.Empty(t, job.Decision)

	// Before the grace period the sweep must leave it alone: the request may
	// still be in flight at the supplier.
	released, ok, err := svc.ReleaseStaleSubmission(context.Background(), job.ID)
	require.NoError(t, err)
	assert.False(t, ok, "released inside the grace period: %+v", released)
	require.Equal(t, "reserved", released.BillingState)

	// Age the job under the clock the service reads, so the grace period is real
	// rather than simulated by a raw SQL update.
	svc.now = func() time.Time { return time.Now().Add(staleSubmissionGrace + time.Minute) }

	released, ok, err = svc.ReleaseStaleSubmission(context.Background(), job.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, []string{"refunded", "settled"}, released.BillingState,
		"the wallet and the product path name the release differently: %+v", released)
	assert.Equal(t, "failure", released.Decision)

	// The money is back: nothing was charged.
	checkMoney(t, svc, req, 1000, 0, 0)

	// Releasing again is a no-op, never a second refund.
	again, ok, err := svc.ReleaseStaleSubmission(context.Background(), job.ID)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, released.BillingState, again.BillingState)
}

// A job the supplier accepted keeps its reservation: only the provider can say
// what happened, so the sweep must not free money it may still be owed.
func TestOCRSubmittedJobIsNotReleasedByStaleSweep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	svc, req := serviceFixture(t, server)

	var job model.OCRJob
	require.NoError(t, svc.db.Create(&model.OCRJob{
		ID: "accepted-job", UserID: req.Admission.UserID, TokenID: req.Admission.TokenID,
		IdempotencyKey: "accepted", Model: req.Target.Model, Quota: 100,
		BillingState: "reserved", SubmissionState: "submitted", ProviderJobID: "provider-1",
		Funding: "wallet", CreatedAt: svc.now().Add(-time.Hour).Unix(),
	}).Error)
	require.NoError(t, svc.db.Where("id = ?", "accepted-job").First(&job).Error)

	released, ok, err := svc.ReleaseStaleSubmission(context.Background(), "accepted-job")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "reserved", released.BillingState)
}

// The owner can abandon a job the supplier never accepted, without waiting for
// the sweep; a job the supplier took must not be cancellable this way.
func TestOCROwnerCancelReleasesOnlyUnacceptedJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	svc, req := serviceFixture(t, server)

	var job model.OCRJob
	require.NoError(t, svc.db.Create(&model.OCRJob{
		ID: "unaccepted", UserID: req.Admission.UserID, TokenID: req.Admission.TokenID,
		IdempotencyKey: "unaccepted", Model: req.Target.Model, Quota: 100,
		BillingState: "reserved", SubmissionState: "unclaimed", Funding: "wallet",
	}).Error)
	_, err := svc.Cancel(context.Background(), "unaccepted", req.Admission.UserID, req.Admission.TokenID)
	require.NoError(t, err)
	require.NoError(t, svc.db.Where("id = ?", "unaccepted").First(&job).Error)
	assert.Equal(t, "cancel", job.Decision)
	assert.Contains(t, []string{"refunded", "settled"}, job.BillingState)

	// A different owner cannot cancel it.
	_, err = svc.Cancel(context.Background(), "unaccepted", req.Admission.UserID+1, req.Admission.TokenID)
	assert.ErrorIs(t, err, model.ErrOCRConflict)
	require.NoError(t, svc.db.Where("id = ?", "unaccepted").First(&job).Error)
}
