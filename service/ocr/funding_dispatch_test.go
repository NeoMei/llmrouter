package ocr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOCRDoesNotSubmitWhenCentralDispatchFenceFails(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"supplier-job"}}`))
	}))
	defer server.Close()
	svc, req := serviceFixture(t, server)
	ctx := context.Background()
	input, err := svc.validateInput(ctx, req.Input)
	require.NoError(t, err)
	job, err := svc.billing.CreateIntent(ctx, req.Admission)
	require.NoError(t, err)
	execution := model.OCRExecution{JobsURL: req.Target.JobsURL, Model: req.Target.Model, InputDigest: input.digest, InputPages: input.pages, MaxAttempts: common.RetryTimes + 1}
	require.NoError(t, svc.billing.ConfigureOCRSubmission(ctx, job.ID, execution))
	_, err = svc.billing.Reserve(ctx, job.ID)
	require.NoError(t, err)
	// Simulate a journal that requires central funding while the owned DB lacks
	// a central account; the dispatch fence must fail closed before the claim.
	require.NoError(t, svc.db.Model(&model.OCRJob{}).Where("id = ?", job.ID).Update("funding", "unihub").Error)
	require.NoError(t, svc.db.Create(&model.OCROperation{ID: uuid.NewString(), JobID: job.ID, Kind: "unihub", Phase: "reserve", Amount: job.Quota, OwnerID: job.UserID, State: "applied"}).Error)
	_, err = svc.Create(ctx, req)
	require.ErrorIs(t, err, model.ErrProductFundingConflict)
	assert.Zero(t, sends.Load())
	snap, err := svc.billing.Inspect(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "unclaimed", snap.Job.SubmissionState)
	assert.Equal(t, "reserved", snap.Job.BillingState)
	assert.Zero(t, snap.Job.SubmitAttempts)
}
