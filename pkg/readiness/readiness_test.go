package readiness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckerReportsNamedFailure(t *testing.T) {
	checker := New(100*time.Millisecond,
		Probe{Name: "database", Check: func(context.Context) error { return nil }},
		Probe{Name: "object_store", Check: func(context.Context) error { return errors.New("down") }},
	)

	result := checker.Status(context.Background())
	assert.False(t, result.Ready)
	assert.Equal(t, "ok", result.Components["database"])
	assert.Equal(t, "error", result.Components["object_store"])
	require.Error(t, checker.Check(context.Background()))
	assert.ErrorContains(t, checker.Check(context.Background()), "object_store")
}

func TestCheckerRejectsDuplicateAndEmptyProbes(t *testing.T) {
	_, err := NewChecked(time.Second,
		Probe{Name: "database", Check: func(context.Context) error { return nil }},
		Probe{Name: "database", Check: func(context.Context) error { return nil }},
	)
	require.ErrorContains(t, err, "duplicate")

	_, err = NewChecked(time.Second, Probe{Name: "", Check: func(context.Context) error { return nil }})
	require.ErrorContains(t, err, "name")
}

func TestCheckerAppliesAggregateTimeout(t *testing.T) {
	checker := New(10*time.Millisecond, Probe{
		Name: "slow",
		Check: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})

	result := checker.Status(context.Background())
	assert.False(t, result.Ready)
	assert.Equal(t, "error", result.Components["slow"])
}
