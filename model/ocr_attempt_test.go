package model

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOCRRefusalRearmSameReservation(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureOCRSubmission(ctx, j.ID, OCRExecution{JobsURL: "https://supplier.example/api/v2/ocr/jobs", Model: a.Model, InputDigest: "digest", MaxAttempts: 2}))
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	first := ocrTestSelection(a.ChannelID)
	c, err := s.ClaimSubmission(ctx, j.ID, first)
	require.NoError(t, err)
	require.NoError(t, s.RecordOCRRefusal(ctx, c, 429, true))
	_, err = s.ClaimSubmission(ctx, j.ID, first)
	require.ErrorIs(t, err, ErrOCRConflict)
	second := first
	second.Ref.MemberID = uuid.NewString()
	second.Sequence++
	c2, err := s.ClaimSubmission(ctx, j.ID, second)
	require.NoError(t, err)
	require.ErrorIs(t, s.RecordSubmission(ctx, c, "late", false), ErrOCRConflict)
	require.NoError(t, s.RecordSubmission(ctx, c2, "accepted", false))
	_, err = s.Finish(ctx, j.ID, "success")
	require.NoError(t, err)
	ocrBalances(t, s, a, 900, 400, 100)
	view, err := s.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, view.Job.SubmitAttempts)
	require.ErrorIs(t, s.RecordOCRRefusal(ctx, c2, 429, true), ErrOCRConflict)
}

func TestOCRDurableBudgetAndUncertainNeverRearm(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown=%v", unknown), func(t *testing.T) {
			s, a := ocrFixture(t)
			ctx := context.Background()
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			execution := OCRExecution{JobsURL: "https://supplier.example/api/v2/ocr/jobs", Model: a.Model, InputDigest: "digest", MaxAttempts: 2}
			require.NoError(t, s.ConfigureOCRSubmission(ctx, j.ID, execution))
			_, err = s.Reserve(ctx, j.ID)
			require.NoError(t, err)
			first, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
			require.NoError(t, err)
			require.ErrorIs(t, s.RecordOCRRefusal(ctx, first, 503, true), ErrOCRConflict)
			if unknown {
				require.NoError(t, s.RecordSubmission(ctx, first, "", true))
			} else {
				require.NoError(t, s.RecordOCRRefusal(ctx, first, 429, true))
			}
			restarted := NewOCRBillingStore(s.db, nil)
			same, err := restarted.CreateIntent(ctx, a)
			require.NoError(t, err)
			assert.Equal(t, j.ID, same.ID)
			require.NoError(t, restarted.ConfigureOCRSubmission(ctx, j.ID, execution))
			execution.MaxAttempts = 3
			require.ErrorIs(t, restarted.ConfigureOCRSubmission(ctx, j.ID, execution), ErrOCRConflict)
			selection := ocrTestSelection(a.ChannelID)
			selection.Ref.MemberID = uuid.NewString()
			next, err := restarted.ClaimSubmission(ctx, j.ID, selection)
			if unknown {
				require.ErrorIs(t, err, ErrOCRConflict)
				_, err = restarted.Finish(ctx, j.ID, "failure")
				require.ErrorIs(t, err, ErrOCRConflict)
			} else {
				require.NoError(t, err)
				require.NoError(t, restarted.RecordOCRRefusal(ctx, next, 429, true))
				_, err = restarted.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
				require.ErrorIs(t, err, ErrOCRConflict)
				_, err = restarted.Finish(ctx, j.ID, "failure")
				require.NoError(t, err)
				ocrBalances(t, s, a, 1000, 500, 0)
			}
		})
	}
}

func TestOCRLocalAbandonCannotRefundConcurrentClaim(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureOCRSubmission(ctx, j.ID, OCRExecution{JobsURL: "https://supplier.example/api/v2/ocr/jobs", Model: a.Model, InputDigest: "digest", MaxAttempts: 2}))
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	first, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
	require.NoError(t, err)
	require.NoError(t, s.RecordOCRRefusal(ctx, first, 429, true))
	next := ocrTestSelection(a.ChannelID)
	next.Ref.MemberID = uuid.NewString()
	claimed, err := s.ClaimSubmission(ctx, j.ID, next)
	require.NoError(t, err)
	_, err = s.AbandonOCRSubmission(ctx, j.ID)
	require.ErrorIs(t, err, ErrOCRConflict)
	ocrBalances(t, s, a, 900, 400, 100)
	require.NoError(t, s.RecordSubmission(ctx, claimed, "owned", false))
	_, err = s.AbandonOCRSubmission(ctx, j.ID)
	require.ErrorIs(t, err, ErrOCRConflict)
}
