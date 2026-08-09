package readiness

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Probe struct {
	Name  string
	Check func(context.Context) error
}

type Result struct {
	Ready      bool              `json:"ready"`
	Components map[string]string `json:"components"`
	errors     map[string]error
}

type Checker struct {
	timeout time.Duration
	probes  []Probe
}

func New(timeout time.Duration, probes ...Probe) *Checker {
	checker, err := NewChecked(timeout, probes...)
	if err != nil {
		panic(err)
	}
	return checker
}

func NewChecked(timeout time.Duration, probes ...Probe) (*Checker, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("readiness timeout must be positive")
	}

	seen := make(map[string]struct{}, len(probes))
	validated := make([]Probe, 0, len(probes))
	for _, probe := range probes {
		probe.Name = strings.TrimSpace(probe.Name)
		if probe.Name == "" {
			return nil, fmt.Errorf("readiness probe name is required")
		}
		if probe.Check == nil {
			return nil, fmt.Errorf("readiness probe %q has no check", probe.Name)
		}
		if _, ok := seen[probe.Name]; ok {
			return nil, fmt.Errorf("duplicate readiness probe %q", probe.Name)
		}
		seen[probe.Name] = struct{}{}
		validated = append(validated, probe)
	}
	return &Checker{timeout: timeout, probes: validated}, nil
}

func (c *Checker) Status(ctx context.Context) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	result := Result{
		Ready:      true,
		Components: make(map[string]string, len(c.probes)),
		errors:     make(map[string]error),
	}
	for _, probe := range c.probes {
		err := runProbe(ctx, probe.Check)
		if err == nil {
			result.Components[probe.Name] = "ok"
			continue
		}
		result.Ready = false
		result.Components[probe.Name] = "error"
		result.errors[probe.Name] = err
	}
	return result
}

func (c *Checker) Check(ctx context.Context) error {
	result := c.Status(ctx)
	if result.Ready {
		return nil
	}
	names := make([]string, 0, len(result.errors))
	for name := range result.errors {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("readiness failed: %s", strings.Join(names, ", "))
}

func runProbe(ctx context.Context, check func(context.Context) error) error {
	done := make(chan error, 1)
	go func() {
		done <- check(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
