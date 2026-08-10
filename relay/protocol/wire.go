package protocol

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/service/modelcatalog"
)

// Decision records the chosen wire protocol and how it was selected.
type Decision struct {
	Wire       modelcatalog.WireProtocol
	Source     string
	ObservedAt time.Time
}

// ResolveInput drives explicit-first wire resolution.
// Requested is the ordered preference list (usually from the route target).
// Declared lists protocols the channel declares it can speak for this operation.
// Observed records the result of optional capability probes.
type ResolveInput struct {
	Requested []modelcatalog.WireProtocol
	Declared  map[modelcatalog.WireProtocol]bool
	Observed  map[modelcatalog.WireProtocol]Observation
}

// Observation records a probe outcome.
type Observation struct {
	Healthy    bool
	ObservedAt time.Time
	Reason     string
}

// Resolve selects the first requested wire that is declared and, if probed,
// currently healthy. It prefers explicit route preferences, then observed
// capability state, then declared-only defaults. Management configuration
// always outranks observed state because it is intentionally authored.
func Resolve(in ResolveInput) (Decision, error) {
	for _, wire := range in.Requested {
		if !in.Declared[wire] {
			continue
		}
		observation, observed := in.Observed[wire]
		if observed && !observation.Healthy {
			continue
		}
		source := "declared"
		if observed {
			source = "probe"
		}
		observedAt := time.Time{}
		if observed {
			observedAt = observation.ObservedAt
		}
		return Decision{Wire: wire, Source: source, ObservedAt: observedAt}, nil
	}
	return Decision{}, errors.New("no declared healthy wire protocol")
}
