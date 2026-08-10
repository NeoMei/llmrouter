package main

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/capability"
)

type capabilityView struct {
	ChannelType int                  `json:"channel_type"`
	ChannelName string               `json:"channel_name"`
	Protocol    capability.Protocol  `json:"protocol"`
	Operation   capability.Operation `json:"operation"`
	Supported   bool                 `json:"supported"`
	Reason      string               `json:"reason,omitempty"`
}

func main() {
	registry := capability.DefaultRegistry()
	views := make([]capabilityView, 0, len(registry.Snapshot()))
	for key, decision := range registry.Snapshot() {
		views = append(views, capabilityView{
			ChannelType: key.ChannelType,
			ChannelName: constant.GetChannelTypeName(key.ChannelType),
			Protocol:    key.Protocol,
			Operation:   key.Operation,
			Supported:   decision.Supported,
			Reason:      decision.Reason,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].ChannelType != views[j].ChannelType {
			return views[i].ChannelType < views[j].ChannelType
		}
		if views[i].Protocol != views[j].Protocol {
			return views[i].Protocol < views[j].Protocol
		}
		return views[i].Operation < views[j].Operation
	})
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"version":      capability.SnapshotVersion(registry),
		"capabilities": views,
	}); err != nil {
		panic(err)
	}
}
