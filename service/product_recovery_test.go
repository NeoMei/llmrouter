package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProductRecoveryStagesGiveLaterQueuesFreshDeadline(t *testing.T) {
	parent, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	var first context.Context
	var ran []int
	errs := runProductRecoveryStages(parent, 20*time.Millisecond, []func(context.Context) error{
		func(ctx context.Context) error { first = ctx; ran = append(ran, 0); <-ctx.Done(); return ctx.Err() },
		func(ctx context.Context) error { ran = append(ran, 1); return ctx.Err() },
		func(ctx context.Context) error { ran = append(ran, 2); return ctx.Err() },
		func(ctx context.Context) error { ran = append(ran, 3); return ctx.Err() },
	})
	require.ErrorIs(t, errs[0], context.DeadlineExceeded)
	require.NoError(t, errs[1])
	require.NoError(t, errs[2])
	require.NoError(t, errs[3])
	require.NoError(t, parent.Err())
	require.ErrorIs(t, first.Err(), context.DeadlineExceeded)
	require.Equal(t, []int{0, 1, 2, 3}, ran)
}

func TestProductRecoveryStagesCancelEveryCompletedChild(t *testing.T) {
	var children []context.Context
	stages := []func(context.Context) error{}
	for i := 0; i < 4; i++ {
		stages = append(stages, func(ctx context.Context) error { children = append(children, ctx); return nil })
	}
	errs := runProductRecoveryStages(context.Background(), time.Second, stages)
	for _, e := range errs {
		require.NoError(t, e)
	}
	for _, child := range children {
		require.ErrorIs(t, child.Err(), context.Canceled)
	}
}

func TestProductRecoveryStagesStopCancelsActiveAndSkipsRemaining(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	started := 0
	errs := runProductRecoveryStages(parent, time.Second, []func(context.Context) error{
		func(ctx context.Context) error { started++; stop(); <-ctx.Done(); return ctx.Err() },
		func(ctx context.Context) error { started++; return nil },
	})
	require.Equal(t, 1, started)
	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], context.Canceled)
}
