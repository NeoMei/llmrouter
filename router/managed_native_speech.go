package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/channel/nativespeech"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func managedBailianSpeechTariff(name string) (metering.Price, int, error) {
	price, priced := billing_setting.ResolveModelNativePrice(name, "speech")
	limit, bounded := billing_setting.ResolveModelNativeReservationLimit(name, "speech")
	quantity, err := strconv.Atoi(limit.Value)
	if !priced || !bounded || err != nil || limit.Scale != 0 || quantity < 1 || quantity > 1200 || price.Policy != "provider_reported_v1" || len(price.Terms) != 1 || price.Terms[0].Key != "characters" || price.Terms[0].Unit != "character" || metering.ValidatePrice(price) != nil {
		return metering.Price{}, 0, errors.New("native speech requires an explicit character tariff and reservation limit")
	}
	return price, quantity, nil
}

func managedBailianSpeechChannelSupported(candidate modelcatalog.ResolvedModel, channel model.Channel) bool {
	if !slices.Contains(candidate.Capabilities.Operations, "bailian.audio.speech.create") || channel.Status != common.ChannelStatusEnabled || channel.Type != constant.ChannelTypeAli || channel.ChannelInfo.IsMultiKey || len(channel.GetParamOverride()) != 0 || len(channel.GetHeaderOverride()) != 0 || channel.GetSetting().PassThroughBodyEnabled || channel.GetSetting().SystemPrompt != "" {
		return false
	}
	if limits := candidate.Capabilities.MediaLimits; limits != nil {
		if len(limits.Formats) > 0 && !slices.Contains(limits.Formats, "wav") {
			return false
		}
		if limits.MaxAudioSeconds != nil {
			seconds := *limits.MaxAudioSeconds
			if math.IsNaN(seconds) || seconds < 1 || seconds > 600 || seconds != math.Trunc(seconds) {
				return false
			}
		}
	}
	policy, err := nativespeech.PolicyForModel("bailian", candidate.UpstreamModel)
	if err != nil {
		return false
	}
	_, err = nativespeech.New(nativespeech.Target{Policy: policy, Origin: strings.TrimRight(channel.GetBaseURL(), "/"), Credential: channel.Key})
	return err == nil
}

func managedBailianSpeechTarget(ctx context.Context, adapter *platformbilling.Adapter, name string) (modelcatalog.ResolvedModel, model.Channel, error) {
	var routes []model.ClientModelRoute
	var targets []model.ClientModelTarget
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&routes).Error; err != nil {
		return modelcatalog.ResolvedModel{}, model.Channel{}, err
	}
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&targets).Error; err != nil {
		return modelcatalog.ResolvedModel{}, model.Channel{}, err
	}
	user, group := adapter.NativePrincipal()
	runtime := modelcatalog.RuntimeContext{UserID: user, Groups: []string{group}, Client: "superwagie"}
	catalog, err := modelcatalog.AssembleRuntimeCatalog(ctx, runtime, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), deploymentCatalogAuthorizer{user, group, runtime.Client})
	if err != nil {
		return modelcatalog.ResolvedModel{}, model.Channel{}, err
	}
	candidates, err := catalog.ResolveCandidates(ctx, user, runtime.Groups, name, runtime.Client)
	if err != nil {
		return modelcatalog.ResolvedModel{}, model.Channel{}, err
	}
	for _, candidate := range candidates {
		var channel model.Channel
		if err := model.DB.WithContext(ctx).First(&channel, candidate.ChannelID).Error; err != nil {
			return modelcatalog.ResolvedModel{}, model.Channel{}, err
		}
		if managedBailianSpeechChannelSupported(candidate, channel) {
			if _, _, err := managedBailianSpeechTariff(candidate.BillingModel); err == nil {
				return candidate, channel, nil
			}
		}
	}
	return modelcatalog.ResolvedModel{}, model.Channel{}, errors.New("native speech target unavailable")
}

func registerManagedNativeSpeech(r *gin.Engine, adapter *platformbilling.Adapter) {
	r.POST("/platform/v1/native/bailian/audio/speech", func(c *gin.Context) {
		if adapter != nil {
			id, group := adapter.NativePrincipal()
			common.SetContextKey(c, constant.ContextKeyUserId, id)
			common.SetContextKey(c, constant.ContextKeyUserGroup, group)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		}
		c.Next()
	}, middleware.DistributedRateLimit(), middleware.ModelRequestRateLimit(), func(c *gin.Context) {
		ct, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || ct != "application/json" || c.Request.URL.RawQuery != "" {
			c.AbortWithStatus(400)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
		var fields map[string]json.RawMessage
		var name string
		if err != nil || !utf8.Valid(raw) || common.UnmarshalUnique(raw, &fields) != nil || len(fields) != 2 || common.UnmarshalUnique(fields["model"], &name) != nil || name == "" || len(name) > 255 || strings.TrimSpace(name) != name {
			c.AbortWithStatus(400)
			return
		}
		request, err := nativespeech.DecodeRequest(fields["input"])
		if err != nil {
			c.AbortWithStatus(400)
			return
		}
		if adapter == nil || !adapter.Enabled() || model.DB == nil {
			c.AbortWithStatus(503)
			return
		}
		selected, channel, err := managedBailianSpeechTarget(c.Request.Context(), adapter, name)
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		policy, err := nativespeech.PolicyForModel("bailian", selected.UpstreamModel)
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		target := nativespeech.Target{Policy: policy, Origin: strings.TrimRight(channel.GetBaseURL(), "/"), Credential: channel.Key}
		if limits := selected.Capabilities.MediaLimits; limits != nil {
			if limits.MaxRequestBytes != nil && int64(len(raw)) > *limits.MaxRequestBytes {
				c.AbortWithStatus(400)
				return
			}
			if limits.MaxAudioSeconds != nil {
				// Shared target admission already validated an integer in [1,600].
				target.Limits.MaxAudioSeconds = int(*limits.MaxAudioSeconds)
			}
		}
		producer, err := newNativeSpeechProducer(target)
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		price, quantity, err := managedBailianSpeechTariff(selected.BillingModel)
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		// Bailian bills Han characters as two units and other scalars as one.
		// Two units per scalar is a conservative admission bound, never usage.
		// The producer independently enforces its 600-scalar input limit.
		if producer.Validate(request) != nil || 2*utf8.RuneCountInString(request.Text) > quantity {
			c.AbortWithStatus(400)
			return
		}
		ratio := ratio_setting.GetGroupRatio(selected.SelectedGroup)
		if override, ok := ratio_setting.GetGroupGroupRatio(selected.SelectedGroup, selected.SelectedGroup); ok {
			ratio = override
		}
		usage := metering.Usage{Version: "metered_v2", Operation: "speech", State: "incomplete", Source: "local_admission", Measurements: []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: strconv.Itoa(quantity)}}}}
		native := model.NativeAdmission{Model: selected.BillingModel, ChannelID: selected.ChannelID, Group: selected.SelectedGroup, GroupRatio: strconv.FormatFloat(ratio, 'g', -1, 64), QuotaPerUnit: strconv.FormatFloat(common.QuotaPerUnit, 'g', -1, 64), Price: price, Reservation: usage}
		quota, clamp, err := model.NativeQuota(native, usage, true)
		if err != nil || clamp != nil {
			c.AbortWithStatus(503)
			return
		}
		snapshot, err := common.Marshal(struct {
			Native model.NativeAdmission `json:"native"`
		}{native})
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		credential := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		session, err := adapter.AdmitNative(c.Request.Context(), credential, c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), platformbilling.WireOperation{ID: "bailian.audio.speech.create"}, name, raw)
		if err != nil {
			c.AbortWithStatus(platformbilling.HTTPStatus(err))
			return
		}
		c.Header("X-UniHub-Call-ID", session.CallID())
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if session.Finish(ctx) != nil {
				common.SysError("managed native speech decision pending")
			}
		}()
		c.Set("platform_billing", session)
		if err = session.BindNativeDispatchGuard(func(ctx context.Context) error {
			currentTarget, current, err := managedBailianSpeechTarget(ctx, adapter, name)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(currentTarget, selected) || current.Key != channel.Key || current.GetBaseURL() != channel.GetBaseURL() {
				return platformbilling.ErrConflict
			}
			return nil
		}); err != nil {
			c.AbortWithStatus(503)
			return
		}
		if err = service.ReserveManagedNativePrice(c.Request.Context(), session, selected.BillingModel, selected.SelectedGroup, selected.RouteID, selected.ChannelID, quota, snapshot); err != nil {
			c.AbortWithStatus(platformbilling.HTTPStatus(err))
			return
		}
		if err = session.Dispatch(c.Request.Context(), channel.Id); err != nil {
			c.AbortWithStatus(409)
			return
		}
		result, sendErr := producer.Send(c.Request.Context(), request)
		durable, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
		defer cancel()
		// Persist supplier evidence before money or delivery. Failed downloads and
		// missing usage stay UNKNOWN, with no second synthesis or invented charge.
		if err = session.RecordNativeAudioObservation(durable, result, false); err != nil {
			c.AbortWithStatus(502)
			return
		}
		if sendErr != nil || !result.Completed || c.Request.Context().Err() != nil {
			c.AbortWithStatus(502)
			return
		}
		usage = platformbilling.NativeSpeechMetering(price, result)
		usage.ProviderRequestID = result.RequestID
		actual, clamp, err := model.NativeQuota(native, usage, false)
		if err != nil || clamp != nil {
			c.AbortWithStatus(502)
			return
		}
		if err = session.SettleNative(durable, actual, &platformbilling.Usage{Metered: &usage}); err != nil {
			c.AbortWithStatus(503)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(200, result.ContentType, result.Audio)
	})
}
