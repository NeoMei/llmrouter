package controller

import (
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/gin-gonic/gin"
)

type capabilityView struct {
	ChannelType int                  `json:"channel_type"`
	ChannelName string               `json:"channel_name"`
	Protocol    capability.Protocol  `json:"protocol"`
	Operation   capability.Operation `json:"operation"`
	Supported   bool                 `json:"supported"`
	Reason      string               `json:"reason,omitempty"`
}

func GetCapabilities(c *gin.Context) {
	registry := capability.DefaultRegistry()
	snapshot := registry.Snapshot()
	views := make([]capabilityView, 0, len(snapshot))
	for key, decision := range snapshot {
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
	common.ApiSuccess(c, gin.H{
		"version":      capability.SnapshotVersion(registry),
		"capabilities": views,
	})
}
