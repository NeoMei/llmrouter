package router

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func serveProductCertified(c *gin.Context, adapter *platformbilling.Adapter, wire platformbilling.WireOperation, ct string, body []byte) {
	ctx := c.Request.Context()
	credential := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	session, target, err := adapter.AdmitProductCertifiedOperation(ctx, credential, c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), wire, ct, body)
	if err != nil {
		if writeProductAdmissionRefusal(c, err) {
			return
		}
		c.AbortWithStatusJSON(platformbilling.HTTPStatus(err), gin.H{"error": gin.H{"code": unifiedAdmissionCode(platformbilling.HTTPStatus(err))}})
		return
	}
	c.Header("X-UniHub-Call-ID", session.CallID())
	defer func() {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if session.Finish(work) != nil {
			common.SysError("product media decision pending")
		}
	}()
	principal, group := adapter.NativePrincipal()
	var channel model.Channel
	if model.DB.WithContext(ctx).First(&channel, target.ChannelID).Error != nil || channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.IsMultiKey {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	if !platformbilling.ProductCreditChannelSupported(wire.ID, channel.Type) {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	routeID, billingModel := 0, target.UpstreamModel
	recognized, err := modelcatalog.RecognizesPublicModel(ctx, model.DB, session.ProductModel())
	if err != nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	if recognized {
		var routes []model.ClientModelRoute
		var targets []model.ClientModelTarget
		if model.DB.WithContext(ctx).Where("enabled = ?", true).Find(&routes).Error != nil ||
			model.DB.WithContext(ctx).Where("enabled = ?", true).Find(&targets).Error != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		runtime := modelcatalog.RuntimeContext{UserID: principal, Groups: []string{group}, Client: session.NativeOriginProduct()}
		resolver, err := modelcatalog.AssembleManagedRuntimeCatalog(ctx, runtime, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB),
			deploymentCatalogAuthorizer{principal, group, runtime.Client})
		if err != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		candidates, err := resolver.ResolveCandidates(ctx, principal, runtime.Groups, session.ProductModel(), runtime.Client)
		if err != nil {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		for _, candidate := range candidates {
			if candidate.ChannelID == target.ChannelID && candidate.UpstreamModel == target.UpstreamModel && slices.Contains(candidate.Capabilities.Operations, wire.ID) {
				routeID, billingModel = candidate.RouteID, candidate.BillingModel
				break
			}
		}
		if routeID == 0 {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}
	common.SetContextKey(c, constant.ContextKeyUserId, principal)
	common.SetContextKey(c, constant.ContextKeyUserGroup, group)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, session.ProductModel())
	price, ok := billing_setting.ResolveProductCreditPrice(session.ProductModel(), session.ProductBillingOperation(), strconv.FormatFloat(operation_setting.USDExchangeRate, 'g', -1, 64))
	if !ok {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "product_credit_price_unavailable"}})
		return
	}
	receipt := metering.Usage{Version: "metered_v2", Operation: price.Tariff.Operation, State: "confirmed", Source: "request_bound"}
	native := model.NativeAdmission{ProductPrice: &price, Model: session.ProductModel(), ChannelID: channel.Id, Group: group, GroupRatio: "1",
		QuotaPerUnit: price.MicrocreditsPerUSD(), Price: price.Tariff}
	quota := int64(-1)
	for _, active := range price.Tariff.Terms {
		if target.MeasurementKey != "" && active.Key != target.MeasurementKey {
			continue
		}
		receipt.Measurements = nil
		for _, term := range price.Tariff.Terms {
			if price.Tariff.Operation == "video" && term.Key != active.Key {
				continue
			}
			quantity := "0"
			if term.Key == active.Key {
				quantity = strconv.Itoa(target.Quantity)
			}
			receipt.Measurements = append(receipt.Measurements, metering.Measurement{Key: term.Key, Unit: term.Unit, ProviderUnit: term.ProviderUnit, Quantity: metering.Quantity{Value: quantity}})
		}
		estimate, err := model.NativeProductCreditAmount(native, receipt, true)
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		if estimate > quota {
			quota = estimate
			native.Reservation = receipt
		}
	}
	if quota < 0 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	// The existing provider certificate is stricter than general catalog
	// ability selection. Revalidate it at the durable dispatch fence.
	if err = session.BindNativeDispatchGuard(func(ctx context.Context) error {
		var current model.Channel
		if model.DB.WithContext(ctx).First(&current, channel.Id).Error != nil || current.Status != common.ChannelStatusEnabled ||
			current.ChannelInfo.IsMultiKey || current.Type != channel.Type || current.Key != channel.Key || current.GetBaseURL() != channel.GetBaseURL() ||
			current.GetModelMapping() != channel.GetModelMapping() {
			return platformbilling.ErrConflict
		}
		switch wire.ID {
		case "videos.create":
			return session.ValidateVideoTarget(current.Id, current.Type, target.UpstreamModel, current.Key, current.GetBaseURL())
		case "dashscope.audio.transcriptions.create":
			return session.ValidateAudioTarget(current.Id, current.Type, target.UpstreamModel, current.Key, current.GetBaseURL())
		default:
			if !adapter.ImageTarget(session.ProductModel(), wire.ID, current.Id, current.Type, target.UpstreamModel, current.Key, current.GetBaseURL()) {
				return platformbilling.ErrConflict
			}
			return nil
		}
	}); err != nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	snapshot, err := common.Marshal(struct {
		Native model.NativeAdmission `json:"native"`
	}{native})
	if err != nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	if err = service.ReserveManagedNativePrice(ctx, session, billingModel, group, routeID, target.ChannelID, quota, snapshot); err != nil {
		if writeProductAdmissionRefusal(c, err) {
			return
		}
		status := platformbilling.HTTPStatus(err)
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": unifiedAdmissionCode(status)}})
		return
	}
	if wire.ID == "videos.create" {
		var relayBody map[string]any
		if common.UnmarshalUnique(body, &relayBody) != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		// The certified HTTP shape takes integer seconds. The existing task
		// adaptor uses the same bounded value in its internal string DTO.
		relayBody["seconds"] = strconv.Itoa(target.Quantity)
		body, err = common.Marshal(relayBody)
		if err != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
	}
	c.Set("platform_billing", session)
	c.Request.Header = http.Header{"Content-Type": []string{ct}}
	c.Request.Body, c.Request.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	switch wire.ID {
	case "videos.create":
		c.Request.URL.Path = "/v1/videos"
		if setup := middleware.SetupContextForSelectedChannel(c, &channel, session.ProductModel()); setup != nil {
			c.AbortWithStatus(setup.StatusCode)
			return
		}
		controller.RelayTask(c)
	case "dashscope.audio.transcriptions.create":
		relayNativeQwenASR(c, adapter)
	default:
		relayNativeImage(c, adapter)
	}
}
