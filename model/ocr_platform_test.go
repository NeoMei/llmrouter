package model

import (
	"context"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPlatformOCRNeverCreatesLocalMoneyOperations(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	a.PlatformCallID = "platform-ocr-call"
	a.PlatformPrice = &metering.Price{Version: "metered_v2", Operation: "ocr", Policy: "success_request", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "5", Denominator: "1"}}}}
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	snapshot, err := s.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Operations)
	require.NoError(t, s.db.Model(&OCRJob{}).Where("id = ?", j.ID).Updates(map[string]any{"submission_state": "submitted", "provider_job_id": "supplier-job", "provider_state": "done"}).Error)
	final, err := s.Finish(ctx, j.ID, "success")
	require.NoError(t, err)
	assert.Equal(t, "platform_pending", final.BillingState)
	_, err = s.Finish(ctx, j.ID, "success")
	require.NoError(t, err)
	_, err = s.Finish(ctx, j.ID, "failure")
	require.ErrorIs(t, err, ErrOCRConflict)
	ocrBalances(t, s, a, 1000, 500, 0)
	var u User
	require.NoError(t, s.db.First(&u, a.UserID).Error)
	assert.Zero(t, u.RequestCount)
	var count int64
	require.NoError(t, s.db.Model(&OCRAuditEvent{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestPlatformOCRFailedAcknowledgementHasNoLocalMoney(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	a.PlatformCallID = "failed-platform"
	a.PlatformPrice = &metering.Price{Version: "metered_v2", Operation: "ocr", Policy: "success_request", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "5", Denominator: "1"}}}}
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&OCRJob{}).Where("id = ?", j.ID).Updates(map[string]any{"submission_state": "submitted", "provider_state": "failed", "provider_job_id": "supplier"}).Error)
	_, err = s.Finish(ctx, j.ID, "failure")
	require.NoError(t, err)
	j, err = s.CompletePlatformOCR(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, "refunded", j.BillingState)
	_, err = s.CompletePlatformOCR(ctx, j.ID)
	require.NoError(t, err)
	ocrBalances(t, s, a, 1000, 500, 0)
	snap, err := s.Inspect(ctx, j.ID)
	require.NoError(t, err)
	assert.Empty(t, snap.Operations)
}
