package ocr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/paddleocr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOCRStatusRecoversInterruptedTerminalRefusal(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status, budget int
		retry          bool
	}{
		{"401-veto", 401, 2, false}, {"429-veto", 429, 2, false}, {"429-budget-exhausted", 429, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sends atomic.Int32
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); w.WriteHeader(tc.status) }))
			defer supplier.Close()
			svc, req := serviceFixture(t, supplier)
			ctx := context.Background()
			job, err := svc.billing.CreateIntent(ctx, req.Admission)
			require.NoError(t, err)
			require.NoError(t, svc.billing.ConfigureOCRSubmission(ctx, job.ID, model.OCRExecution{JobsURL: req.Target.JobsURL, Model: req.Target.Model, InputDigest: "fixture", MaxAttempts: tc.budget}))
			_, err = svc.billing.Reserve(ctx, job.ID)
			require.NoError(t, err)
			ch, err := svc.channel(ctx, req.Admission.ChannelID, 0)
			require.NoError(t, err)
			selection, key, owner, err := svc.selectAccount(ctx, ch, 0, req.Admission.Model, nil)
			require.NoError(t, err)
			claim, err := svc.billing.ClaimSubmission(ctx, job.ID, selection, owner)
			require.NoError(t, err)
			_, response, err := svc.supplier.Submit(ctx, req.Target.JobsURL, key, paddleocr.Input{Model: req.Target.Model, Filename: "fixture.pdf", File: req.Input.File, OptionalPayload: []byte(`{}`)})
			require.Error(t, err)
			require.Equal(t, tc.status, response.Status)
			require.NoError(t, svc.billing.RecordOCRRefusal(ctx, claim, response.Status, tc.retry))
			// Exact interruption boundary: refusal committed in the real DB, Finish has
			// never run. Restart with a fresh billing store; recover through GET only.
			before, err := svc.billing.Inspect(ctx, job.ID)
			require.NoError(t, err)
			require.Equal(t, "rejected", before.Job.SubmissionState)
			require.False(t, before.Job.RetryRefusal)
			require.Equal(t, "reserved", before.Job.BillingState)
			checkMoney(t, svc, req, 900, 100, 0)
			billing := &Billing{model.NewOCRBillingStore(svc.db, nil)}
			defer billing.Close()
			restarted, err := NewService(svc.db, billing, Config{PublicOrigin: svc.publicOrigin, GrantSecret: svc.grantSecret, Objects: svc.objects, SupplierHTTP: supplier.Client(), FetchHTTP: supplier.Client()})
			require.NoError(t, err)
			for i := 0; i < 2; i++ {
				status, err := restarted.Status(ctx, job.ID, req.Admission.UserID, req.Admission.TokenID)
				require.NoError(t, err)
				assert.Equal(t, "failed", status.State)
			}
			checkMoney(t, svc, req, 1000, 0, 0)
			after, err := billing.Inspect(ctx, job.ID)
			require.NoError(t, err)
			assert.Equal(t, "refunded", after.Job.BillingState)
			assert.Equal(t, "failure", after.Job.Decision)
			assert.Equal(t, before.Job.SubmitAttempts, after.Job.SubmitAttempts)
			assert.Equal(t, before.Job.Attempts, after.Job.Attempts)
			var refunds, events int64
			require.NoError(t, svc.db.Model(&model.OCROperation{}).Where("job_id = ? AND phase = ? AND state = ?", job.ID, "refund", "applied").Count(&refunds).Error)
			assert.EqualValues(t, 2, refunds)
			require.NoError(t, svc.db.Model(&model.OCRAuditEvent{}).Where("job_id = ? AND kind = ?", job.ID, "failure").Count(&events).Error)
			assert.EqualValues(t, 1, events)
			assert.EqualValues(t, 1, sends.Load())
		})
	}
}

func TestOCRStatusDoesNotRefundNonterminalSubmission(t *testing.T) {
	for _, state := range []string{"retryable-refusal", "submitting", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			var calls atomic.Int32
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer supplier.Close()
			svc, req := serviceFixture(t, supplier)
			ctx := context.Background()
			job, err := svc.billing.CreateIntent(ctx, req.Admission)
			require.NoError(t, err)
			require.NoError(t, svc.billing.ConfigureOCRSubmission(ctx, job.ID, model.OCRExecution{JobsURL: req.Target.JobsURL, Model: req.Target.Model, InputDigest: "fixture", MaxAttempts: 2}))
			_, err = svc.billing.Reserve(ctx, job.ID)
			require.NoError(t, err)
			ch, err := svc.channel(ctx, req.Admission.ChannelID, 0)
			require.NoError(t, err)
			selection, _, owner, err := svc.selectAccount(ctx, ch, 0, req.Admission.Model, nil)
			require.NoError(t, err)
			claim, err := svc.billing.ClaimSubmission(ctx, job.ID, selection, owner)
			require.NoError(t, err)
			switch state {
			case "retryable-refusal":
				require.NoError(t, svc.billing.RecordOCRRefusal(ctx, claim, 429, true))
			case "uncertain":
				require.NoError(t, svc.billing.RecordSubmission(ctx, claim, "", true))
			}
			before, err := svc.billing.Inspect(ctx, job.ID)
			require.NoError(t, err)
			billing := &Billing{model.NewOCRBillingStore(svc.db, nil)}
			defer billing.Close()
			restarted, err := NewService(svc.db, billing, Config{PublicOrigin: svc.publicOrigin, GrantSecret: svc.grantSecret, Objects: svc.objects, SupplierHTTP: supplier.Client(), FetchHTTP: supplier.Client()})
			require.NoError(t, err)
			for i := 0; i < 2; i++ {
				status, err := restarted.Status(ctx, job.ID, req.Admission.UserID, req.Admission.TokenID)
				require.NoError(t, err)
				assert.Equal(t, "pending", status.State, fmt.Sprintf("%s must stay nonterminal", state))
			}
			after, err := billing.Inspect(ctx, job.ID)
			require.NoError(t, err)
			assert.Equal(t, before.Job, after.Job)
			checkMoney(t, svc, req, 900, 100, 0)
			assert.Zero(t, calls.Load())
			var refunds int64
			require.NoError(t, svc.db.Model(&model.OCROperation{}).Where("job_id = ? AND phase = ?", job.ID, "refund").Count(&refunds).Error)
			assert.Zero(t, refunds)
		})
	}
}
