package platformbilling

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTechnicalUsageUsesVerifiedLogicalCallAcrossRetriesAndRecovery(t *testing.T) {
	failSettle := true
	a, db := fixture(t, &failSettle)
	ctx := context.Background()
	s, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(ctx, 1))
	s.Rejected()
	require.NoError(t, s.Dispatch(ctx, 2))
	s.observe(&dto.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 20, CacheWriteTokens: 10}, CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 15}})
	trusted := s.ConfirmedTokenUsage()
	require.NotNil(t, trusted)
	assert.Equal(t, 100, trusted.PromptTokens)
	assert.Equal(t, 50, trusted.CompletionTokens)
	created, err := s.RecordTelemetry(ctx, "routed-group", 1234, true)
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.RecordTelemetry(ctx, "wrong-group", 9999, false)
	require.NoError(t, err)
	assert.False(t, created)
	require.NoError(t, s.Finish(ctx))
	read := func() []TechnicalUsage {
		rows, readErr := ReadTechnicalUsage(ctx, db, TechnicalScope{ActorID: "actor", InstanceID: "default"}, time.Now().Unix()-60, time.Now().Unix()+60)
		require.NoError(t, readErr)
		return rows
	}
	rows := read()
	require.Len(t, rows, 1)
	assert.Equal(t, "SETTLE_PENDING", rows[0].Status)
	assert.Equal(t, "success", rows[0].Outcome)
	assert.Equal(t, "confirmed", rows[0].UsageState)
	assert.Equal(t, "account", rows[0].Scope.AccountID)
	assert.Equal(t, "cred", rows[0].Scope.CredentialID)
	assert.Equal(t, 2, rows[0].AttemptCount)
	assert.Equal(t, 2, rows[0].ChannelID)
	assert.Equal(t, "routed-group", rows[0].Group)
	assert.EqualValues(t, 1234, rows[0].LatencyMS)
	assert.True(t, rows[0].IsStream)
	assert.Equal(t, 100, rows[0].PromptTokens)
	assert.Equal(t, 50, rows[0].CompletionTokens)
	failSettle = false
	require.NoError(t, a.Recover(ctx))
	rows = read()
	require.Len(t, rows, 1)
	assert.Equal(t, "SETTLED", rows[0].Status)
	assert.Equal(t, 2, rows[0].AttemptCount)
}

func TestTechnicalUsageNeverExposesOtherActorInstanceOrUnconfirmedTokens(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail)
	ctx := context.Background()
	s, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","max_tokens":50}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(ctx, 1))
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: 5})
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: -1})
	assert.Nil(t, s.ConfirmedTokenUsage())
	require.NoError(t, s.Finish(ctx))
	now := time.Now()
	require.NoError(t, db.Create(&Call{CallID: "other-actor", ActorID: "other", AccountID: "account", FundingProductID: "llmrouter", InstanceID: "default", Model: "private", Status: "SETTLED", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&Call{CallID: "other-instance", ActorID: "actor", AccountID: "account", FundingProductID: "llmrouter", InstanceID: "private", Model: "private", Status: "SETTLED", CreatedAt: now}).Error)
	rows, err := ReadTechnicalUsage(ctx, db, TechnicalScope{ActorID: "actor", InstanceID: "default"}, now.Unix()-60, now.Unix()+60)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "call", rows[0].Scope.CallID)
	assert.Equal(t, "pending", rows[0].Outcome)
	assert.Equal(t, "missing", rows[0].UsageState)
	assert.Zero(t, rows[0].PromptTokens)
	assert.Zero(t, rows[0].CompletionTokens)
	_, err = ReadTechnicalUsage(ctx, db, TechnicalScope{InstanceID: "default"}, now.Unix()-60, now.Unix()+60)
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestTechnicalUsageTracksAsyncTerminalWithoutAnotherRequest(t *testing.T) {
	a, db, settles := meteredFixture(t, "speech")
	ctx := context.Background()
	s, err := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello","voice":"v"}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(ctx, 2))
	require.NoError(t, s.BindTask(ctx, "upstream-task"))
	read := func() []TechnicalUsage {
		rows, readErr := ReadTechnicalUsage(ctx, db, TechnicalScope{ActorID: "actor", InstanceID: "default"}, time.Now().Unix()-60, time.Now().Unix()+60)
		require.NoError(t, readErr)
		return rows
	}
	rows := read()
	require.Len(t, rows, 1)
	assert.Equal(t, "pending", rows[0].Outcome)
	require.NoError(t, a.ObserveTask(ctx, "call", "upstream-task", speechUsage("2")))
	rows = read()
	require.Len(t, rows, 1)
	assert.Equal(t, "success", rows[0].Outcome)
	assert.Equal(t, "confirmed", rows[0].UsageState)
	require.Len(t, rows[0].Measurements, 1)
	assert.Equal(t, "2", rows[0].Measurements[0].Quantity.Value)
	assert.Equal(t, "2", rows[0].Charged)
	assert.EqualValues(t, 1, settles.Load())
	assert.Equal(t, 1, rows[0].AttemptCount)
	assert.Zero(t, rows[0].PromptTokens)
	assert.Zero(t, rows[0].CompletionTokens)
}

func TestTechnicalUsageIncludesCompleteEndSecondWithoutNextSecond(t *testing.T) {
	fail := false
	_, db := fixture(t, &fail)
	second := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	for _, row := range []Call{
		{CallID: "end-second", ActorID: "actor", FundingProductID: "llmrouter", InstanceID: "default", Model: "current", CreatedAt: second.Add(500 * time.Millisecond)},
		{CallID: "next-second", ActorID: "actor", FundingProductID: "llmrouter", InstanceID: "default", Model: "later", CreatedAt: second.Add(time.Second)},
	} {
		require.NoError(t, db.Create(&row).Error)
	}
	rows, err := ReadTechnicalUsage(context.Background(), db, TechnicalScope{ActorID: "actor", InstanceID: "default"}, second.Unix(), second.Unix())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "end-second", rows[0].Scope.CallID)
}
