package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
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
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

type managedQwenImageRequest struct {
	Model string `json:"model"`
	Input struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	} `json:"input"`
	Parameters struct {
		N            *int   `json:"n,omitempty"`
		Size         string `json:"size,omitempty"`
		Seed         *int64 `json:"seed,omitempty"`
		Watermark    *bool  `json:"watermark,omitempty"`
		PromptExtend *bool  `json:"prompt_extend,omitempty"`
	} `json:"parameters"`
}

type managedQwenImageResponse struct {
	RequestID string `json:"request_id,omitempty"`
	Output    *struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      *struct {
				Role    string `json:"role"`
				Content []struct {
					Image string `json:"image"`
				} `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"output"`
	Usage *struct {
		ImageCount *int `json:"image_count"`
		Width      *int `json:"width"`
		Height     *int `json:"height"`
	} `json:"usage"`
	Code  string          `json:"code,omitempty"`
	Error json.RawMessage `json:"error,omitempty"`
}

func parseManagedQwenImage(raw []byte) (managedQwenImageRequest, int, int, int, error) {
	var in managedQwenImageRequest
	dec := common.NewJsonDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) > 64<<10 || common.UnmarshalUnique(raw, &in) != nil || dec.Decode(&in) != nil || strings.TrimSpace(in.Model) == "" || len(in.Model) > 255 || len(in.Input.Messages) != 1 {
		return in, 0, 0, 0, platformbilling.ErrInvalid
	}
	msg := in.Input.Messages[0]
	if msg.Role != "user" || len(msg.Content) != 1 || !utf8.ValidString(msg.Content[0].Text) || len(msg.Content[0].Text) == 0 || len(msg.Content[0].Text) > 1300 {
		return in, 0, 0, 0, platformbilling.ErrInvalid
	}
	n, w, h := 1, 1024, 1024
	if in.Parameters.N != nil {
		n = *in.Parameters.N
	}
	if n < 1 || n > 6 || in.Parameters.Seed != nil && (*in.Parameters.Seed < 0 || *in.Parameters.Seed > 2147483647) {
		return in, 0, 0, 0, platformbilling.ErrInvalid
	}
	if in.Parameters.Size != "" {
		a, b, ok := strings.Cut(in.Parameters.Size, "*")
		var e1, e2 error
		w, e1 = strconv.Atoi(a)
		h, e2 = strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil || w < 16 || h < 16 || w > 4096 || h > 4096 || w%16 != 0 || h%16 != 0 || w*h < 512*512 || w*h > 2048*2048 {
			return in, 0, 0, 0, platformbilling.ErrInvalid
		}
	}
	in.Parameters.N = &n
	if in.Parameters.Size == "" {
		in.Parameters.Size = "1024*1024"
	}
	return in, n, w, h, nil
}

func managedQwenImageTarget(ctx context.Context, adapter *platformbilling.Adapter, name string) (modelcatalog.ResolvedModel, model.Channel, error) {
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
	catalog, err := modelcatalog.AssembleRuntimeCatalog(ctx, runtime, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), deploymentCatalogAuthorizer{user, group, "superwagie"})
	if err != nil {
		return modelcatalog.ResolvedModel{}, model.Channel{}, err
	}
	candidates, err := catalog.ResolveCandidates(ctx, user, runtime.Groups, name, "superwagie")
	if err != nil {
		return modelcatalog.ResolvedModel{}, model.Channel{}, err
	}
	for _, candidate := range candidates {
		if !slices.Contains(candidate.Capabilities.Operations, "dashscope.images.generate") {
			continue
		}
		var channel model.Channel
		if err := model.DB.WithContext(ctx).First(&channel, candidate.ChannelID).Error; err != nil {
			return modelcatalog.ResolvedModel{}, model.Channel{}, err
		}
		if !managedQwenImageChannelSupported(candidate, channel) {
			continue
		}
		return candidate, channel, nil
	}
	return modelcatalog.ResolvedModel{}, model.Channel{}, errors.New("native image target unavailable")
}

func managedQwenImagePrice(selected modelcatalog.ResolvedModel, n, w, h int) (model.NativeAdmission, int, error) {
	price, ok := billing_setting.ResolveModelNativePrice(selected.BillingModel, "image_generation")
	if !ok {
		value, exists := ratio_setting.GetModelPrice(selected.BillingModel, false)
		if !exists || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || billing_setting.GetBillingMode(selected.BillingModel) == billing_setting.BillingModeTieredExpr {
			return model.NativeAdmission{}, 0, errors.New("native image price unavailable")
		}
		rational, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
		if !ok {
			return model.NativeAdmission{}, 0, platformbilling.ErrInvalid
		}
		price = metering.Price{Version: "metered_v2", Operation: "image_generation", Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "images", Unit: "image", Price: metering.Rational{Numerator: rational.Num().String(), Denominator: rational.Denom().String()}}}}
	}
	ratio := ratio_setting.GetGroupRatio(selected.SelectedGroup)
	if override, ok := ratio_setting.GetGroupGroupRatio(selected.SelectedGroup, selected.SelectedGroup); ok {
		ratio = override
	}
	outputs := make([]service.NativeImageOutput, n)
	for i := range outputs {
		outputs[i] = service.NativeImageOutput{ID: strconv.Itoa(i), Width: w, Height: h}
	}
	usage := service.NativeImageUsage(price, outputs, true)
	usage.State = "incomplete"
	usage.Source = "local_admission"
	native := model.NativeAdmission{Model: selected.BillingModel, ChannelID: selected.ChannelID, Group: selected.SelectedGroup, GroupRatio: strconv.FormatFloat(ratio, 'g', -1, 64), QuotaPerUnit: strconv.FormatFloat(common.QuotaPerUnit, 'g', -1, 64), Price: price, Reservation: usage}
	quota, clamp, err := model.NativeQuota(native, usage, true)
	if clamp != nil {
		return native, 0, clamp
	}
	return native, quota, err
}

func observeManagedQwenImage(raw []byte, n int, price metering.Price) ([]byte, metering.Usage, error) {
	var result managedQwenImageResponse
	if len(raw) > 64<<10 || common.UnmarshalUnique(raw, &result) != nil || result.Code != "" || len(result.Error) > 0 && string(result.Error) != "null" || len(result.RequestID) > 256 || result.Output == nil || result.Usage == nil || result.Usage.ImageCount == nil || result.Usage.Width == nil || result.Usage.Height == nil || len(result.Output.Choices) != 1 {
		return nil, metering.Usage{}, platformbilling.ErrInvalid
	}
	count, w, h := *result.Usage.ImageCount, *result.Usage.Width, *result.Usage.Height
	if count < 0 || count > n || w < 1 || h < 1 || w > 4096 || h > 4096 || w*h < 512*512 || w*h > 2048*2048 {
		return nil, metering.Usage{}, platformbilling.ErrInvalid
	}
	choice := result.Output.Choices[0]
	if choice.FinishReason != "stop" || choice.Message == nil || choice.Message.Role != "assistant" || len(choice.Message.Content) != count {
		return nil, metering.Usage{}, platformbilling.ErrInvalid
	}
	outputs := make([]service.NativeImageOutput, 0, count)
	seen := map[string]bool{}
	for _, item := range choice.Message.Content {
		u, err := url.ParseRequestURI(item.Image)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || seen[item.Image] {
			return nil, metering.Usage{}, platformbilling.ErrInvalid
		}
		seen[item.Image] = true
		outputs = append(outputs, service.NativeImageOutput{ID: item.Image, Width: w, Height: h})
	}
	usage := service.NativeImageUsage(price, outputs, true)
	if usage.State != "confirmed" {
		return nil, usage, platformbilling.ErrInvalid
	}
	// Return only validated protocol fields. No supplier diagnostic or headers
	// become user-visible, and remote image URLs are never fetched with a key.
	result.Error = nil
	body, err := common.Marshal(result)
	return body, usage, err
}

type managedNativeImageState struct {
	request              managedQwenImageRequest
	count, width, height int
	session              *platformbilling.Session
}

func registerManagedNativeImage(r *gin.Engine, adapter *platformbilling.Adapter) {
	r.POST("/platform/v1/native/dashscope/images/generations", func(c *gin.Context) {
		raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
		if err != nil {
			c.AbortWithStatus(400)
			return
		}
		in, n, width, height, err := parseManagedQwenImage(raw)
		if err != nil {
			c.AbortWithStatus(400)
			return
		}
		if adapter == nil || !adapter.Enabled() || model.DB == nil {
			c.AbortWithStatus(503)
			return
		}
		credential := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		session, err := adapter.AdmitNative(c.Request.Context(), credential, c.GetHeader("X-UniHub-Task-ID"), c.GetHeader("Idempotency-Key"), platformbilling.WireOperation{ID: "dashscope.images.generate"}, in.Model, raw)
		if err != nil {
			c.AbortWithStatus(platformbilling.HTTPStatus(err))
			return
		}
		c.Header("X-UniHub-Call-ID", session.CallID())
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if session.Finish(ctx) != nil {
				common.SysError("managed native image decision pending")
			}
		}()
		id, group := adapter.NativePrincipal()
		common.SetContextKey(c, constant.ContextKeyUserId, id)
		common.SetContextKey(c, constant.ContextKeyUserGroup, group)
		common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		c.Set("managed_native_image", &managedNativeImageState{request: in, count: n, width: width, height: height, session: session})
		c.Set("platform_billing", session)
		c.Next()
	}, middleware.DistributedRateLimit(), middleware.ModelRequestRateLimit(), func(c *gin.Context) {
		state := c.MustGet("managed_native_image").(*managedNativeImageState)
		in, n, width, height, session := state.request, state.count, state.width, state.height, state.session
		selected, channel, err := managedQwenImageTarget(c.Request.Context(), adapter, in.Model)
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		native, quota, err := managedQwenImagePrice(selected, n, width, height)
		if err != nil {
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
		base := strings.TrimRight(channel.GetBaseURL(), "/")
		if base == "" {
			base = strings.TrimRight(constant.ChannelBaseURLs[channel.Type], "/")
		}
		target, err := url.Parse(base + "/api/v1/services/aigc/multimodal-generation/generation")
		if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
			c.AbortWithStatus(503)
			return
		}
		in.Model = selected.UpstreamModel
		body, err := common.Marshal(in)
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		// Use the existing protected producer transport, with redirects and cookies
		// disabled. The request carries only this route's supplier credential.
		transport := imageProducerHTTPClient()
		if transport == nil {
			c.AbortWithStatus(503)
			return
		}
		client := *transport
		client.Jar = nil
		client.Timeout = 120 * time.Second
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
		if err != nil {
			c.AbortWithStatus(503)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+channel.Key)
		if err = service.ReserveManagedNativePrice(c.Request.Context(), session, selected.BillingModel, selected.SelectedGroup, selected.RouteID, selected.ChannelID, quota, snapshot); err != nil {
			c.AbortWithStatus(platformbilling.HTTPStatus(err))
			return
		}
		if err = session.Dispatch(c.Request.Context(), channel.Id); err != nil {
			c.AbortWithStatus(409)
			return
		}
		response, err := client.Do(request)
		if err != nil {
			c.AbortWithStatus(502)
			return
		}
		defer response.Body.Close()
		result, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		if err != nil || response.StatusCode != 200 {
			c.AbortWithStatus(502)
			return
		}
		output, usage, err := observeManagedQwenImage(result, n, native.Price)
		if err != nil {
			c.AbortWithStatus(502)
			return
		}
		actual, clamp, err := model.NativeQuota(native, usage, false)
		if err != nil || clamp != nil {
			c.AbortWithStatus(502)
			return
		}
		durable, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
		defer cancel()
		if err = session.SettleNative(durable, actual, &platformbilling.Usage{Metered: &usage}); err != nil {
			c.AbortWithStatus(503)
			return
		}
		c.Header("X-UniHub-Call-ID", session.CallID())
		c.Header("Cache-Control", "no-store")
		c.Data(200, "application/json", output)
	})
}

// Match the producer's real channel contract when projecting executability.
func managedQwenImageChannelSupported(candidate modelcatalog.ResolvedModel, channel model.Channel) bool {
	return channel.Status == common.ChannelStatusEnabled && channel.Type == constant.ChannelTypeAli && !channel.ChannelInfo.IsMultiKey && channel.Key != "" && (candidate.UpstreamModel == "qwen-image-2.0" || candidate.UpstreamModel == "qwen-image-2.0-pro")
}
