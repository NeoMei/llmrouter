package controller

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var OCRRuntime *ocr.Runtime // initialized before router construction; closed before DB

type OCRHandler struct{ Runtime *ocr.Runtime }

// ocrRequestBody counts bytes as the capped wire body is consumed. Counting
// below the multipart parser includes its buffered read-ahead exactly once.
// Embedding ReadCloser preserves the underlying request body's Close behavior.
type ocrRequestBody struct {
	io.ReadCloser
	size int64
}

func (body *ocrRequestBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	body.size += int64(n)
	return n, err
}

// OCRBodyLimit precedes rate-limit/body parsing, including multipart uploads.
func OCRBodyLimit(c *gin.Context) {
	const limit = ocr.MaxInputBytes + (64 << 10)
	if c.Request.ContentLength > limit {
		c.AbortWithStatusJSON(400, gin.H{"error": "OCR request byte limit exceeded"})
		return
	}
	body := &ocrRequestBody{ReadCloser: http.MaxBytesReader(c.Writer, c.Request.Body, limit)}
	c.Request.Body = body
	c.Set("ocr_request_body", body)
	defer func() {
		if c.Request.MultipartForm != nil {
			_ = c.Request.MultipartForm.RemoveAll()
		}
	}()
	c.Next()
}

func (h *OCRHandler) available(c *gin.Context) bool {
	if h == nil || h.Runtime == nil {
		c.JSON(503, gin.H{"error": "OCR is not configured"})
		return false
	}
	return true
}

func (h *OCRHandler) Create(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var in dto.OCRCreateRequest
	var media ocr.Input
	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
			h.fail(c, 400, "invalid OCR multipart request")
			return
		}
		form := c.Request.MultipartForm
		for k, v := range form.Value {
			if (k != "model" && k != "optionalPayload") || len(v) != 1 {
				h.fail(c, 400, "unsupported or repeated OCR field")
				return
			}
		}
		if len(form.File) != 1 || len(form.File["file"]) != 1 {
			h.fail(c, 400, "one OCR file required")
			return
		}
		in.Model = c.PostForm("model")
		in.OptionalPayload = []byte(c.PostForm("optionalPayload"))
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			h.fail(c, 400, "OCR file required")
			return
		}
		media.File, err = io.ReadAll(io.LimitReader(file, ocr.MaxInputBytes+1))
		file.Close()
		if err != nil || len(media.File) > ocr.MaxInputBytes {
			h.fail(c, 400, "OCR file byte limit exceeded")
			return
		}
		media.Filename = header.Filename
	} else if strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			h.fail(c, 400, "invalid OCR JSON request")
			return
		}
		if err = common.UnmarshalUnique(body, &in); err != nil {
			h.fail(c, 400, "invalid OCR JSON request")
			return
		}
		decoder := common.NewJsonDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&in); err != nil {
			h.fail(c, 400, "unsupported OCR JSON field")
			return
		}
		media.FileURL = in.FileURL
	} else {
		h.fail(c, 400, "OCR requires multipart or JSON")
		return
	}
	// Multipart parsing stops at its closing boundary. Drain any remaining
	// epilogue through the same capped reader before authorization, URL fetch,
	// account selection or money. No second body buffer/media parser is needed.
	value, ok := c.Get("ocr_request_body")
	body, valid := value.(*ocrRequestBody)
	if !ok || !valid {
		h.fail(c, 400, "OCR bounded request body required")
		return
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		h.fail(c, 400, "OCR request byte limit exceeded")
		return
	}
	media.OptionalPayload = in.OptionalPayload
	in.Model = strings.TrimSpace(in.Model)
	if in.Model == "" || len(in.Model) > 128 {
		h.fail(c, 400, "OCR model required")
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if value, platform := c.Get("platform_billing"); platform {
		if session, ok := value.(*platformbilling.Session); ok {
			key = session.CallID()
		}
	}
	if key == "" {
		key = uuid.NewString()
	}
	if len(key) > 128 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
		h.fail(c, 400, "invalid idempotency key")
		return
	}
	candidates, user, token, err := h.candidates(c, in.Model)
	if err != nil {
		h.fail(c, 403, "OCR model access denied")
		return
	}
	var selected modelcatalog.ResolvedModel
	var target ocr.Target
	var selectionErr error
	existed := false
	for _, candidate := range candidates {
		if !slices.Contains(candidate.PreferredWires, modelcatalog.WirePaddleOCR) {
			continue
		}
		if candidate.Capabilities.Operations != nil && !slices.Contains(candidate.Capabilities.Operations, string(capability.OperationOCRCreate)) {
			continue
		}
		if limits := candidate.Capabilities.MediaLimits; limits != nil && limits.MaxRequestBytes != nil && body.size > *limits.MaxRequestBytes {
			h.fail(c, 400, "OCR model request byte limit exceeded")
			return
		}
		t, e := h.target(c, candidate)
		if e != nil {
			selectionErr = e
			continue
		}
		// A supplied stable key recovers its immutable job even if the pool is now
		// cooled. A new job must choose a healthy authorized target before money.
		var old model.OCRJob
		e = h.Runtime.DB.WithContext(c.Request.Context()).Where("user_id = ? AND idempotency_key = ?", user.Id, key).First(&old).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			h.fail(c, 503, "OCR admission unavailable")
			return
		}
		if e == nil {
			existed = true
			if old.TokenID != token.Id || old.ChannelID != candidate.ChannelID || old.Model != candidate.BillingModel || old.BillingGroup != candidate.SelectedGroup {
				continue
			}
		} else if e = h.Runtime.Service.CheckTarget(c.Request.Context(), user.Id, candidate.ChannelID, t, candidate.BillingModel); e != nil {
			selectionErr = e
			continue
		}
		selected, target = candidate, t
		break
	}
	if selected.ChannelID == 0 {
		if existed {
			h.fail(c, 409, "OCR immutable request conflict")
			return
		}
		h.serviceError(c, selectionErr, 503)
		return
	}
	admission := ocr.AuthorizedAdmission{IdempotencyKey: key, UserID: user.Id, TokenID: token.Id, ChannelID: selected.ChannelID, Model: selected.BillingModel, UserGroup: user.Group, UsingGroup: selected.SelectedGroup, BillingPreference: user.GetSetting().BillingPreference}
	if value, platform := c.Get("platform_billing"); platform {
		session, ok := value.(*platformbilling.Session)
		if !ok {
			h.fail(c, 400, "platform billing missing")
			return
		}
		price, ok := session.MeteredPrice()
		if !ok || price.Operation != "ocr" {
			h.fail(c, 400, "platform OCR tariff required")
			return
		}
		admission.PlatformCallID = session.CallID()
		admission.PlatformPrice = &price
		admission.Dispatch = func(ctx context.Context) error { return session.Dispatch(ctx, selected.ChannelID) }
		admission.BindTask = session.BindTask
	}
	status, err := h.Runtime.Service.Create(c.Request.Context(), ocr.CreateRequest{Admission: admission, RequestedModel: in.Model, Target: target, Capabilities: selected.Capabilities, Input: media, DisableRetry: strings.EqualFold(c.GetHeader("X-Should-Retry"), "false")})
	if err != nil {
		h.serviceError(c, err, 502)
		return
	}
	if value, platform := c.Get("platform_billing"); platform {
		session := value.(*platformbilling.Session)
		c.JSON(202, gin.H{"id": session.CallID(), "job_id": status.JobID, "state": status.State, "status_url": "/platform/v1/ocr/jobs/" + session.CallID()})
		return
	}
	h.respond(c, status, in.Model, true)
}

func (h *OCRHandler) Status(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var job model.OCRJob
	if err := h.Runtime.DB.WithContext(c.Request.Context()).Where("id = ? AND user_id = ? AND token_id = ? AND funding <> 'platform'", c.Param("id"), c.GetInt("id"), c.GetInt("token_id")).First(&job).Error; err != nil {
		h.fail(c, 404, "OCR job not found")
		return
	}
	var execution model.OCRExecution
	if err := common.UnmarshalJsonStr(job.Execution, &execution); err != nil {
		h.fail(c, 503, "OCR job unavailable")
		return
	}
	name := execution.RequestedModel
	if name == "" {
		name = job.Model
	}
	candidates, _, _, err := h.candidates(c, name)
	if err != nil {
		h.fail(c, 403, "OCR model access denied")
		return
	}
	allowed := false
	for _, candidate := range candidates {
		if candidate.ChannelID != job.ChannelID || candidate.BillingModel != job.Model || candidate.UpstreamModel != execution.Model || candidate.SelectedGroup != job.BillingGroup || !slices.Contains(candidate.PreferredWires, modelcatalog.WirePaddleOCR) {
			continue
		}
		if candidate.Capabilities.Operations != nil && !slices.Contains(candidate.Capabilities.Operations, string(capability.OperationOCRRetrieve)) {
			continue
		}
		target, e := h.target(c, candidate)
		if e == nil && target.JobsURL == execution.JobsURL && target.ProfileID == execution.ProfileID {
			allowed = true
			break
		}
	}
	if !allowed {
		h.fail(c, 403, "OCR job authorization changed")
		return
	}
	status, err := h.Runtime.Service.Status(c.Request.Context(), job.ID, job.UserID, job.TokenID)
	if err != nil {
		h.serviceError(c, err, 502)
		return
	}
	h.respond(c, status, name, false)
}

// candidates rechecks the actual user/token records as well as TokenAuth. No
// client-provided group/channel/upstream URL can become routing authority.
func (h *OCRHandler) candidates(c *gin.Context, name string) ([]modelcatalog.ResolvedModel, model.User, model.Token, error) {
	ctx := c.Request.Context()
	db := h.Runtime.DB.WithContext(ctx)
	var user model.User
	var token model.Token
	denied := errors.New("OCR authorization denied")
	if err := db.First(&user, c.GetInt("id")).Error; err != nil {
		return nil, user, token, err
	}
	group := user.Group
	if _, platform := c.Get("platform_billing"); platform {
		if user.Status != common.UserStatusEnabled {
			return nil, user, token, denied
		}
		group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
		if group == "" || group == "auto" {
			return nil, user, token, denied
		}
	} else {
		if err := db.First(&token, c.GetInt("token_id")).Error; err != nil {
			return nil, user, token, err
		}
		if user.Status != common.UserStatusEnabled || token.UserId != user.Id || (token.Status != common.TokenStatusEnabled && !(c.Request.Method == http.MethodGet && token.Status == common.TokenStatusExhausted)) || token.ExpiredTime != -1 && token.ExpiredTime < time.Now().Unix() {
			return nil, user, token, denied
		}
		if token.ModelLimitsEnabled && !token.GetModelLimitsMap()[name] {
			return nil, user, token, denied
		}
		group = user.Group
		if token.Group != "" {
			if _, ok := service.GetUserUsableGroups(user.Group)[token.Group]; !ok {
				return nil, user, token, denied
			}
			group = token.Group
		}
	}
	groups := []string{group}
	if group == "auto" {
		groups = service.GetUserAutoGroup(user.Group)
	} else if !ratio_setting.ContainsGroupRatio(group) {
		return nil, user, token, denied
	}
	var routes []model.ClientModelRoute
	var targets []model.ClientModelTarget
	if err := db.Where("enabled = ?", true).Find(&routes).Error; err != nil {
		return nil, user, token, err
	}
	if err := db.Where("enabled = ?", true).Find(&targets).Error; err != nil {
		return nil, user, token, err
	}
	recognized, err := modelcatalog.RecognizesPublicModel(ctx, h.Runtime.DB, name)
	if err != nil {
		return nil, user, token, err
	}
	if recognized {
		catalog, err := modelcatalog.AssembleRuntimeCatalog(ctx, modelcatalog.RuntimeContext{UserID: user.Id, Groups: groups}, routes, targets, modelcatalog.NewGormRuntimeSources(h.Runtime.DB), requestCatalogAuthorizer{})
		if err != nil {
			return nil, user, token, err
		}
		candidates, err := catalog.ResolveCandidates(ctx, user.Id, groups, name, "")
		return candidates, user, token, err
	}
	// Legacy fixed consumer model names resolve exclusively through live abilities.
	var abilities []model.Ability
	if err := db.Where("enabled = ? AND model = ?", true, name).Order("priority DESC").Order("channel_id ASC").Find(&abilities).Error; err != nil {
		return nil, user, token, err
	}
	candidates := []modelcatalog.ResolvedModel{}
	for _, g := range groups {
		for _, a := range abilities {
			if a.Group != g {
				continue
			}
			var ch model.Channel
			if err := db.First(&ch, a.ChannelId).Error; err != nil {
				return nil, user, token, err
			}
			if ch.Status != common.ChannelStatusEnabled || !capability.DefaultRegistry().Supports(ch.Type, capability.ProtocolPaddleOCR, capability.OperationOCRCreate) {
				continue
			}
			upstream := name
			mapping := map[string]string{}
			if ch.GetModelMapping() != "" {
				if err := common.UnmarshalJsonStr(ch.GetModelMapping(), &mapping); err != nil {
					return nil, user, token, err
				}
				if mapping[name] != "" {
					upstream = mapping[name]
				}
			}
			candidates = append(candidates, modelcatalog.ResolvedModel{RequestedModel: name, UpstreamModel: upstream, BillingModel: name, ChannelID: ch.Id, SelectedGroup: g, PreferredWires: []modelcatalog.WireProtocol{modelcatalog.WirePaddleOCR}})
		}
	}
	if len(candidates) == 0 {
		return nil, user, token, denied
	}
	return candidates, user, token, nil
}

func (h *OCRHandler) target(c *gin.Context, candidate modelcatalog.ResolvedModel) (ocr.Target, error) {
	db := h.Runtime.DB.WithContext(c.Request.Context())
	var ch model.Channel
	if err := db.First(&ch, candidate.ChannelID).Error; err != nil {
		return ocr.Target{}, err
	}
	if ch.Status != common.ChannelStatusEnabled {
		return ocr.Target{}, errors.New("OCR channel disabled")
	}
	target := ocr.Target{Model: candidate.UpstreamModel}
	base := ch.GetBaseURL()
	switch ch.Type {
	case constant.ChannelTypePaddleOCR:
		if base == "" {
			base = constant.ChannelBaseURLs[ch.Type]
		}
	case constant.ChannelTypeGateway:
		var models []model.ProviderModel
		if err := db.Where("model_id = ? AND status = ?", candidate.UpstreamModel, "active").Limit(2).Find(&models).Error; err != nil {
			return target, err
		}
		if len(models) != 1 {
			return target, errors.New("OCR provider model ambiguous or missing")
		}
		var profile model.ProviderProfile
		if err := db.Where("id = ? AND status = ?", models[0].ProfileId, "active").First(&profile).Error; err != nil {
			return target, err
		}
		if profile.AdapterType != "paddleocr_jobs" || profile.AuthMode != "bearer" {
			return target, errors.New("OCR requires PaddleOCR bearer profile")
		}
		target.ProfileID = profile.Id
		base = profile.BaseURL
	default:
		return target, errors.New("OCR channel incompatible")
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/api/v2/ocr/jobs") {
		base += "/api/v2/ocr/jobs"
	}
	target.JobsURL = base
	return target, nil
}

func (h *OCRHandler) Resource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	body, info, err := h.Runtime.Service.OpenGrantedResource(c.Request.Context(), c.Param("id"), c.Param("resourceID"), c.Query("token"))
	if err != nil {
		h.fail(c, 404, "OCR resource unavailable")
		return
	}
	defer body.Close()
	switch info.ContentType {
	case "application/pdf", "application/x-ndjson", "image/png", "image/jpeg":
	default:
		h.fail(c, 404, "OCR resource unavailable")
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	c.DataFromReader(200, info.Size, info.ContentType, body, nil)
}
func (h *OCRHandler) fail(c *gin.Context, code int, message string) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/v2/ocr/") {
		c.JSON(code, gin.H{"code": code, "msg": message, "errorCode": code, "errorMsg": message})
		return
	}
	c.JSON(code, gin.H{"error": message})
}
func (h *OCRHandler) serviceError(c *gin.Context, err error, fallback int) {
	var unavailable *model.UpstreamAccountUnavailable
	var price *ocr.PriceError
	switch {
	case errors.As(err, &price):
		h.fail(c, 503, "OCR native request price unavailable")
	case errors.Is(err, ocr.ErrInvalidInput):
		h.fail(c, 400, "invalid OCR input or model media contract")
	case errors.Is(err, model.ErrOCRGrant):
		h.fail(c, 410, "OCR result expired")
	case errors.As(err, &unavailable):
		accountpool.SetUnavailableHeader(c, err)
		h.fail(c, 429, "OCR accounts temporarily unavailable")
	case errors.Is(err, model.ErrOCRInsufficient):
		h.fail(c, 402, "OCR quota insufficient")
	case errors.Is(err, model.ErrOCRConflict):
		h.fail(c, 409, "OCR immutable request conflict")
	default:
		h.fail(c, fallback, "OCR request could not be completed")
	}
}
func (h *OCRHandler) respond(c *gin.Context, status ocr.JobStatus, name string, create bool) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/v2/ocr/") {
		c.JSON(200, gin.H{"code": 0, "msg": "", "data": status})
		return
	}
	out := dto.OCRJobResponse{ID: status.JobID, Object: "ocr.job", Model: name, State: status.State, Asynchronous: true, TotalPages: status.ExtractProgress.TotalPages, ExtractedPages: status.ExtractProgress.ExtractedPages, ResultURL: status.ResultURL.JSONURL}
	if status.State == "done" {
		result, err := h.Runtime.Service.CanonicalResult(c.Request.Context(), status.JobID, c.GetInt("id"), c.GetInt("token_id"))
		if err != nil {
			h.fail(c, 502, "OCR result unavailable")
			return
		}
		out.Result = result
	}
	code := 200
	if create {
		code = 202
	}
	c.JSON(code, out)
}
