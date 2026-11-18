package router

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func isProductKnowledgeControlPath(path string) bool {
	if path == "/platform/v1/knowledge/route" {
		return true
	}
	result, present := strings.CutPrefix(path, "/platform/v1/knowledge/results/")
	return present && result != "" && !strings.Contains(result, "/")
}

func productKnowledgeTarget(request *http.Request) (bool, error) {
	switch request.URL.Path {
	case "/platform/v1/rerank", "/platform/api/v1/services/rerank/text-rerank/text-rerank",
		"/platform/v1/multimodal/embeddings", "/platform/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding":
		return true, nil
	case "/platform/v1/embeddings":
		// Match the managed native entry's body bound so other embedding
		// models retain their ordinary protocol and body limits.
		const limit = 64 << 20
		if request.Body == nil {
			return false, platformbilling.ErrInvalid
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
		request.Body.Close()
		if err != nil || len(body) > limit {
			return false, platformbilling.ErrInvalid
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var input struct {
			Model string `json:"model"`
		}
		if common.UnmarshalUnique(body, &input) != nil {
			return false, platformbilling.ErrInvalid
		}
		return platformbilling.ApprovedKnowledgePair(input.Model, "embeddings.create"), nil
	}
	return false, nil
}

func reserveProductKnowledge(ctx context.Context, session *platformbilling.Session, adapter *platformbilling.Adapter) error {
	binding, upstream, limit, ok := session.ProductKnowledgeBound()
	if !ok {
		return platformbilling.ErrInvalid
	}
	_, group := adapter.Principal()
	price, ok := productKnowledgePrice(binding.Model, session.ProductBillingOperation())
	if !ok {
		return platformbilling.ErrInvalid
	}
	bound := metering.Usage{Version: "metered_v2", Operation: price.Tariff.Operation, State: "confirmed", Source: "request_bound",
		Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: strconv.FormatInt(limit, 10)}}}}
	native := model.NativeAdmission{ProductPrice: &price, Model: binding.Model, ChannelID: binding.ChannelID, Group: group,
		GroupRatio: "1", QuotaPerUnit: price.MicrocreditsPerUSD(), Price: price.Tariff, Reservation: bound}
	maximum, err := model.NativeProductCreditAmount(native, bound, true)
	if err != nil {
		return err
	}
	snapshot, err := common.Marshal(struct {
		Native model.NativeAdmission `json:"native"`
	}{native})
	if err != nil {
		return err
	}
	return service.ReserveManagedNativePrice(ctx, session, upstream, group, binding.RouteID, binding.ChannelID, maximum, snapshot)
}

func productKnowledgePrice(name, operation string) (billing_setting.ProductCreditPrice, bool) {
	price, ok := billing_setting.ResolveProductCreditPrice(name, operation, strconv.FormatFloat(operation_setting.USDExchangeRate, 'g', -1, 64))
	return price, ok && price.Tariff.Policy == "provider_reported_v1" && len(price.Tariff.Terms) == 1 &&
		price.Tariff.Terms[0].Key == "input" && price.Tariff.Terms[0].Unit == "token"
}

// Governed routes use their own certified ability group. Publish an
// executable product option only when the requested product's pin resolves
// and the exact customer tariff used by admission is available.
func projectProductKnowledgeAvailability(ctx context.Context, adapter *platformbilling.Adapter, product string, routes []model.ClientModelRoute, models map[string]ProviderCatalogModel) {
	for _, pair := range []struct{ name, wire string }{{"text-embedding-v4", "embeddings.create"}, {"qwen3-rerank", "rerank.create"}} {
		current, exists := models[pair.name]
		binding, bindErr := adapter.ManagedKnowledgeBinding(ctx, product, pair.name, pair.wire)
		if bindErr != nil {
			if exists {
				for i := range current.CapabilityMetadata.Operations {
					op := &current.CapabilityMetadata.Operations[i]
					if op.ID == pair.wire {
						op.Available, op.Executable, op.DisabledReason = false, false, "certified_knowledge_pin_unavailable"
						op.PublicTariff = nil
					}
				}
				current.Probe = slices.ContainsFunc(current.CapabilityMetadata.Operations, func(op platformbilling.CatalogOperation) bool { return op.Executable })
				models[pair.name] = current
			}
			continue
		}
		if !exists {
			i := slices.IndexFunc(routes, func(route model.ClientModelRoute) bool { return route.Id == binding.RouteID })
			if i == -1 {
				continue
			}
			route := routes[i]
			var capabilities dto.ModelCapabilities
			if common.UnmarshalUnique([]byte(route.Metadata), &capabilities) != nil {
				continue
			}
			capabilities.Operations = []string{pair.wire}
			current = projectProviderModel(platformbilling.CatalogModel{ID: pair.name, Name: route.DisplayName, Capabilities: capabilities})
		}
		for i := range current.CapabilityMetadata.Operations {
			op := &current.CapabilityMetadata.Operations[i]
			if op.ID != pair.wire {
				continue
			}
			price, ok := productKnowledgePrice(pair.name, op.BillingOperation)
			if !ok {
				op.PublicTariff = nil
				op.Available, op.Executable, op.DisabledReason = false, false, "product_credit_price_unavailable"
			} else {
				op.PublicTariff = publicProductCreditTariff(price)
				op.Available, op.Executable, op.DisabledReason = true, true, ""
				op.Policy = "provider_reported_v1"
			}
		}
		current.Probe = slices.ContainsFunc(current.CapabilityMetadata.Operations, func(op platformbilling.CatalogOperation) bool { return op.Executable })
		models[pair.name] = current
	}
}

func productMultimodalTarget(adapter *platformbilling.Adapter, name string, target modelcatalog.ResolvedModel, channel model.Channel) bool {
	const wire = "multimodal.embeddings.create"
	setting := channel.GetSetting()
	return channel.Type == constant.ChannelTypeAli && !channel.ChannelInfo.IsMultiKey &&
		slices.Contains(target.Capabilities.Operations, wire) && setting.SystemPrompt == "" && !setting.PassThroughBodyEnabled &&
		!model_setting.GetGlobalSettings().PassThroughRequestEnabled && len(channel.GetParamOverride()) == 0 && len(channel.GetHeaderOverride()) == 0 &&
		adapter.KnowledgeTarget(name, wire, channel.Id, channel.Type, target.UpstreamModel, channel.Key, channel.GetBaseURL())
}
