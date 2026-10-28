package router

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	ci "github.com/QuantumNous/new-api/internal/platformbilling/continuityindex"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/model_setting"
)

// Install once, before serving. Current target admission deliberately uses the
// same catalog resolver and principal/group as managed relay, never client JSON.
func installNativeHistoryResolver(adapter *platformbilling.Adapter) {
	adapter.SetNativeHistoryResolver(func(ctx context.Context, product, public, wire string, want ci.Target) (ci.Target, string, error) {
		unavailable := platformbilling.ErrNativeHistory
		user, group := adapter.Principal()
		var channel model.Channel
		if model.DB.WithContext(ctx).First(&channel, want.ChannelID).Error != nil || channel.Status != common.ChannelStatusEnabled || channel.Type != constant.ChannelTypeOpenAI || !slices.Contains(channel.GetGroups(), group) {
			return ci.Target{}, "", unavailable
		}
		organization := ""
		if channel.OpenAIOrganization != nil {
			organization = *channel.OpenAIOrganization
		}
		namespace, e := platformbilling.NativeHistoryNamespace(strings.TrimRight(channel.GetBaseURL(), "/")+"/v1/responses", "")
		if e != nil || namespace != want.Namespace || organization != want.APIOrganization || channel.GetSetting().SystemPrompt != "" || channel.GetSetting().PassThroughBodyEnabled || len(channel.GetHeaderOverride()) > 0 || len(channel.GetParamOverride()) > 0 || model_setting.GetGlobalSettings().PassThroughRequestEnabled {
			return ci.Target{}, "", unavailable
		}
		var routes []model.ClientModelRoute
		var targets []model.ClientModelTarget
		if model.DB.WithContext(ctx).Where("enabled = ?", true).Find(&routes).Error != nil || model.DB.WithContext(ctx).Where("enabled = ?", true).Find(&targets).Error != nil {
			return ci.Target{}, "", unavailable
		}
		upstream := ""
		recognized, e := modelcatalog.RecognizesPublicModel(ctx, model.DB, public)
		if e != nil {
			return ci.Target{}, "", unavailable
		}
		if recognized {
			catalog, e := modelcatalog.AssembleManagedRuntimeCatalog(ctx, modelcatalog.RuntimeContext{UserID: user, Groups: []string{group}, Client: product}, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), deploymentCatalogAuthorizer{user, group, product})
			if e != nil {
				return ci.Target{}, "", unavailable
			}
			candidates, e := catalog.ResolveCandidates(ctx, user, []string{group}, public, product)
			if e != nil {
				return ci.Target{}, "", unavailable
			}
			for _, candidate := range candidates {
				if candidate.ChannelID == channel.Id && slices.Contains(candidate.Capabilities.Operations, wire) {
					bound := adapter.NativeHistoryInputBound(public, wire)
					if bound < 1 || (candidate.ContextWindow > 0 && candidate.ContextWindow < bound) || (candidate.ContextWindow == 0 && candidate.RequiresKnownContext) {
						continue
					}
					if upstream != "" && upstream != candidate.UpstreamModel {
						return ci.Target{}, "", unavailable
					}
					upstream = candidate.UpstreamModel
				}
			}
		} else {
			var ability model.Ability
			if model.DB.WithContext(ctx).Where("channel_id = ? AND model = ? AND enabled = ?", channel.Id, public, true).Where(map[string]any{"group": group}).First(&ability).Error != nil {
				return ci.Target{}, "", unavailable
			}
			mapping := map[string]string{}
			if raw := channel.GetModelMapping(); raw != "" {
				if common.UnmarshalJsonStr(raw, &mapping) != nil {
					return ci.Target{}, "", unavailable
				}
			}
			upstream, e = common.ResolveModelMapping(public, mapping)
			if e != nil {
				return ci.Target{}, "", unavailable
			}
		}
		if upstream == "" {
			return ci.Target{}, "", unavailable
		}
		if wire == "responses.compact" && !adapter.CompactTarget(public, channel.Id, channel.Type, upstream, 0) {
			return ci.Target{}, "", unavailable
		}
		refs, _, e := model.ChannelAccountRefs(ctx, model.DB, &channel)
		if e != nil {
			return ci.Target{}, "", unavailable
		}
		for _, ref := range refs {
			if want.MemberID != "" && (ref.MemberID != want.MemberID || ref.PoolID != want.PoolID) {
				continue
			}
			if model.CheckUpstreamAccounts(ctx, model.DB, []model.UpstreamAccountRef{ref}, model.UpstreamAccountScope{Operation: wire, Model: upstream}, time.Now()) != nil {
				continue
			}
			got := want
			got.MemberID = ref.MemberID
			got.PoolID = ref.PoolID
			return got, upstream, nil
		}
		return ci.Target{}, "", unavailable
	})
}
