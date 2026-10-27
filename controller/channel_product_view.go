package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// productChannelView is an allowlist, not a credential denylist. Opaque config,
// URLs, overrides and provider error metadata can contain supplier credentials.
// Native operators retain their existing configuration response contract.
func productChannelView(c *gin.Context, ch *model.Channel) any {
	if _, product := authz.ProductPrincipal(c); !product || ch == nil {
		return ch
	}
	info := ch.ChannelInfo
	info.MultiKeyDisabledReason = nil
	info.MultiKeyDisabledTime = nil
	return gin.H{
		"id": ch.Id, "type": ch.Type, "key": "", "name": ch.Name, "test_model": ch.TestModel, "status": ch.Status,
		"models": ch.Models, "group": ch.Group, "model_mapping": ch.ModelMapping, "status_code_mapping": ch.StatusCodeMapping,
		"weight": ch.Weight, "priority": ch.Priority, "auto_ban": ch.AutoBan, "tag": ch.Tag, "channel_info": info,
		"created_time": ch.CreatedTime, "test_time": ch.TestTime, "response_time": ch.ResponseTime,
		"balance": ch.Balance, "balance_updated_time": ch.BalanceUpdatedTime, "used_quota": ch.UsedQuota,
		"configuration_redacted": true,
	}
}
func productChannelViews(c *gin.Context, channels []*model.Channel) []any {
	out := make([]any, 0, len(channels))
	for _, ch := range channels {
		out = append(out, productChannelView(c, ch))
	}
	return out
}

// Product forms receive opaque configuration without its contents. Merge only
// submitted object keys into the saved config; omitted keys never mean deletion.
func mergeProductChannelSettings(c *gin.Context, channel *PatchChannel, origin *model.Channel, request map[string]any) error {
	if _, product := authz.ProductPrincipal(c); !product {
		return nil
	}
	for _, field := range []string{"setting", "settings"} {
		if _, provided := request[field]; !provided {
			continue
		}
		raw, old := "", ""
		if field == "setting" {
			if channel.Setting != nil {
				raw = *channel.Setting
			}
			if origin.Setting != nil {
				old = *origin.Setting
			}
		} else {
			raw = channel.OtherSettings
			old = origin.OtherSettings
		}
		if raw == "" || raw == "null" {
			continue
		} // an explicit whole-field clear remains a sensitive change
		merged := map[string]any{}
		patch := map[string]any{}
		if old != "" && old != "null" {
			if err := common.Unmarshal([]byte(old), &merged); err != nil {
				return err
			}
		}
		if err := common.Unmarshal([]byte(raw), &patch); err != nil {
			return err
		}
		for key, value := range patch {
			merged[key] = value
		}
		encoded, err := common.Marshal(merged)
		if err != nil {
			return err
		}
		if field == "setting" {
			value := string(encoded)
			channel.Setting = &value
		} else {
			channel.OtherSettings = string(encoded)
		}
	}
	return nil
}
