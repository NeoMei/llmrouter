package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

type Key struct {
	ChannelType int       `json:"channel_type"`
	Protocol    Protocol  `json:"protocol"`
	Operation   Operation `json:"operation"`
}

type Decision struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

type Declaration struct {
	Key      Key
	Decision Decision
}

type Registry interface {
	Resolve(channelType int, protocol Protocol, operation Operation) Decision
	Supports(channelType int, protocol Protocol, operation Operation) bool
	Snapshot() map[Key]Decision
	Ready() error
}

type StaticRegistry struct {
	channelTypes []int
	operations   []Operation
	decisions    map[Key]Decision
}

func NewRegistry(channelTypes []int, operations []Operation, declarations []Declaration) (*StaticRegistry, error) {
	registry := &StaticRegistry{
		channelTypes: append([]int(nil), channelTypes...),
		operations:   append([]Operation(nil), operations...),
		decisions:    make(map[Key]Decision, len(channelTypes)*len(operations)),
	}
	sort.Ints(registry.channelTypes)
	for _, channelType := range registry.channelTypes {
		for _, operation := range registry.operations {
			key := Key{ChannelType: channelType, Protocol: ProtocolForOperation(operation), Operation: operation}
			registry.decisions[key] = Decision{Reason: "upstream capability is not declared"}
		}
	}

	declared := make(map[Key]struct{}, len(declarations))
	for _, declaration := range declarations {
		if _, ok := declared[declaration.Key]; ok {
			return nil, fmt.Errorf("duplicate capability declaration: %+v", declaration.Key)
		}
		declared[declaration.Key] = struct{}{}
		if _, ok := registry.decisions[declaration.Key]; !ok {
			return nil, fmt.Errorf("capability declaration is outside the registry matrix: %+v", declaration.Key)
		}
		declaration.Decision.Reason = strings.TrimSpace(declaration.Decision.Reason)
		if !declaration.Decision.Supported && declaration.Decision.Reason == "" {
			return nil, fmt.Errorf("unsupported capability requires a reason: %+v", declaration.Key)
		}
		registry.decisions[declaration.Key] = declaration.Decision
	}
	if err := registry.Ready(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *StaticRegistry) Resolve(channelType int, protocol Protocol, operation Operation) Decision {
	if decision, ok := r.decisions[Key{ChannelType: channelType, Protocol: protocol, Operation: operation}]; ok {
		return decision
	}
	return Decision{Reason: "channel type, protocol, or operation is not registered"}
}

func (r *StaticRegistry) Supports(channelType int, protocol Protocol, operation Operation) bool {
	return r.Resolve(channelType, protocol, operation).Supported
}

func (r *StaticRegistry) Snapshot() map[Key]Decision {
	snapshot := make(map[Key]Decision, len(r.decisions))
	for key, decision := range r.decisions {
		snapshot[key] = decision
	}
	return snapshot
}

func (r *StaticRegistry) Ready() error {
	if len(r.channelTypes) == 0 || len(r.operations) == 0 {
		return fmt.Errorf("capability registry matrix is empty")
	}
	for _, channelType := range r.channelTypes {
		for _, operation := range r.operations {
			key := Key{ChannelType: channelType, Protocol: ProtocolForOperation(operation), Operation: operation}
			decision, ok := r.decisions[key]
			if !ok {
				return fmt.Errorf("capability registry is missing %+v", key)
			}
			if !decision.Supported && strings.TrimSpace(decision.Reason) == "" {
				return fmt.Errorf("unsupported capability has no reason: %+v", key)
			}
		}
	}
	return nil
}

func SnapshotVersion(registry Registry) string {
	snapshot := registry.Snapshot()
	keys := make([]Key, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ChannelType != keys[j].ChannelType {
			return keys[i].ChannelType < keys[j].ChannelType
		}
		if keys[i].Protocol != keys[j].Protocol {
			return keys[i].Protocol < keys[j].Protocol
		}
		return keys[i].Operation < keys[j].Operation
	})
	hash := sha256.New()
	for _, key := range keys {
		decision := snapshot[key]
		_, _ = fmt.Fprintf(hash, "%d\x00%s\x00%s\x00%t\x00%s\n", key.ChannelType, key.Protocol, key.Operation, decision.Supported, decision.Reason)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
