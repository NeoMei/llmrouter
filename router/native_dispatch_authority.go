package router

import (
	"context"
	"slices"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/modelcatalog"
)

// Authorization is checked again after reservation and immediately before the
// dispatch fence. Catalog snapshots never outlive key or route revocation.
func authorizeNativeDispatch(ctx context.Context, a *platformbilling.Adapter, grant platformbilling.Claims, name string, q platformbilling.NativeQuote) error {
	catalog, err := readProviderCatalog(ctx, a, grant)
	if err != nil {
		return err
	}
	advertisedName := name
	if q.RouteID != "0" {
		var route model.ClientModelRoute
		if model.DB.WithContext(ctx).Where("id = ? AND enabled = ?", q.RouteID, true).First(&route).Error != nil {
			return platformbilling.ErrInvalid
		}
		advertisedName = route.PublicId
	}
	allowed := false
	for _, m := range catalog.Models {
		if m.ID == advertisedName {
			for _, op := range m.CapabilityMetadata.Operations {
				if op.ID == q.WireOperation && op.Executable {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return platformbilling.ErrInvalid
	}
	channelID, err := strconv.Atoi(q.ChannelID)
	if err != nil {
		return platformbilling.ErrInvalid
	}
	var channel model.Channel
	if model.DB.WithContext(ctx).First(&channel, channelID).Error != nil || channel.Status != common.ChannelStatusEnabled {
		return platformbilling.ErrInvalid
	}
	user, group := a.NativePrincipal()
	if group != q.Group {
		return platformbilling.ErrConflict
	}
	if q.RouteID == "0" {
		var count int64
		err := model.DB.WithContext(ctx).Model(&model.Ability{}).Where(&model.Ability{Group: group, Model: name, ChannelId: channelID, Enabled: true}).Count(&count).Error
		if err != nil {
			return err
		}
		if count == 0 {
			return platformbilling.ErrInvalid
		}
		return nil
	}
	var routes []model.ClientModelRoute
	var targets []model.ClientModelTarget
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Find(&routes).Error; err != nil {
		return err
	}
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Find(&targets).Error; err != nil {
		return err
	}
	runtime := modelcatalog.RuntimeContext{UserID: user, Groups: []string{group}, Client: grant.OriginProductID}
	resolver, err := modelcatalog.AssembleRuntimeCatalog(ctx, runtime, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), deploymentCatalogAuthorizer{user, group, grant.OriginProductID})
	if err != nil {
		return err
	}
	candidates, err := resolver.ResolveCandidates(ctx, user, runtime.Groups, name, grant.OriginProductID)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if strconv.Itoa(candidate.RouteID) == q.RouteID && candidate.ChannelID == channelID && candidate.BillingModel == q.BillingModel && slices.Contains(candidate.Capabilities.Operations, q.WireOperation) {
			return nil
		}
	}
	return platformbilling.ErrInvalid
}
