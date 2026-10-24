package accountpool

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const lifecycleKey = string(constant.ContextKeyAccountPoolLifecycle)

// Lifecycle is request-local. Selection is prepared before send; Sent is a value
// copy taken immediately before the transport, never reconstructed from key indexes.
type Lifecycle struct {
	// ResolvedRoute is set only after controller catalog admission. Its model is already authoritative.
	ResolvedRoute              bool
	NativeAllStatuses          bool
	NativeAllStatusesRequested bool
	NativeRequested            bool
	NativePending              bool
	DB                         *gorm.DB
	Candidates                 []model.UpstreamAccountRef
	Random                     bool
	Sends                      int
	MaxSends                   int
	ResourceRejected           bool
	RetryChannel               *model.Channel
	TaskBound                  bool
	Selected                   *model.UpstreamAccountSelection
	Sent                       *model.UpstreamAccountSelection
	Owner                      *model.UpstreamAccountRef
	Scope                      model.UpstreamAccountScope
	Excluded                   map[string]bool
	Veto                       bool
	Unsafe                     bool
	ResponsesBackground        bool
	Uncertain                  bool
	UnsafeResponse             bool
	PersistenceError           error
}

func State(c *gin.Context) *Lifecycle {
	if value, ok := c.Get(lifecycleKey); ok {
		if state, ok := value.(*Lifecycle); ok {
			return state
		}
	}
	state := &Lifecycle{Excluded: make(map[string]bool)}
	c.Set(lifecycleKey, state)
	return state
}

func Prepare(c *gin.Context, db *gorm.DB, selected model.UpstreamAccountSelection, scope model.UpstreamAccountScope) {
	state := State(c)
	state.DB, state.Selected, state.Scope = db, &selected, scope
}

func ResetSelection(c *gin.Context) {
	state := State(c)
	state.Selected = nil
	state.Owner = nil
	state.Candidates = nil
	state.Random = false
}

// BeginSend is called only at the actual transport boundary. A member may never
// be sent twice in the same client request, including random-mode selection.
func BeginSend(c *gin.Context) error {
	state := State(c)
	if state.MaxSends > 0 && state.Sends >= state.MaxSends {
		return errors.New("upstream send budget exhausted")
	}
	if c.Request.Context().Err() != nil {
		return c.Request.Context().Err()
	}
	if state.Selected != nil {
		if err := ValidateResources(c); err != nil {
			state.ResourceRejected = true
			return err
		}
	}
	state.Veto, state.Uncertain, state.UnsafeResponse = false, false, false
	state.NativeAllStatuses = state.NativeAllStatusesRequested
	state.NativeAllStatusesRequested = false
	state.NativePending = state.NativeRequested
	state.NativeRequested = false
	state.Sent = nil
	_, operation, operationErr := capability.ResolveRequest(c.Request.Method, c.Request.URL.Path)
	state.Unsafe = operation == capability.OperationResponseCreate && state.ResponsesBackground
	switch operation {
	case capability.OperationAudioSpeech:
		state.Unsafe = state.NativePending
	case capability.OperationImageGeneration, capability.OperationImageEdit, capability.OperationImageVariation,
		capability.OperationVideoCreate, capability.OperationVideoRemix,
		capability.OperationMidjourneySubmit, capability.OperationSunoSubmit:
		state.Unsafe = true
	}
	if operationErr != nil && c.Request.Method == http.MethodPost {
		// Older custom submit/task routes are conservatively unsafe; known
		// read/poll operations use their canonical operation above.
		path := c.Request.URL.Path
		state.Unsafe = strings.Contains(path, "/submit") || strings.Contains(path, "/tasks")
	}
	if state.Selected == nil {
		state.Sends++
		return nil
	}
	selected := *state.Selected
	if state.Excluded[selected.Ref.MemberID] {
		return errors.New("upstream account already attempted")
	}
	state.Excluded[selected.Ref.MemberID] = true
	state.Sent = &selected
	state.Sends++
	return nil
}

// Observe runs before provider helpers can return on non-2xx. Error bodies are
// bounded and restored byte-for-byte for their original adapter/error handler.
func Observe(c *gin.Context, response *http.Response, transportErr error) error {
	state := State(c)
	if transportErr != nil || response == nil {
		state.Uncertain = state.Unsafe
		return nil
	}
	state.Veto = strings.EqualFold(strings.TrimSpace(response.Header.Get("X-Should-Retry")), "false")
	state.Uncertain = state.Unsafe && response.StatusCode >= 500
	state.UnsafeResponse = state.Unsafe && response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusTooManyRequests
	if state.NativePending && (state.NativeAllStatuses || response.StatusCode >= 200 && response.StatusCode < 300) {
		return nil
	}
	state.NativePending = false
	if state.Sent == nil {
		return nil
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
	defer cancel()
	var err error
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		c.Header("Retry-After", "")
		err = model.RecordUpstreamAccountSuccess(ctx, state.DB, *state.Sent, state.Scope, now)
	} else {
		input := FailureInput{StatusCode: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}
		if response.Body != nil && response.StatusCode >= 400 {
			prefix, readErr := io.ReadAll(io.LimitReader(response.Body, 8193))
			response.Body = &restoredBody{Reader: io.MultiReader(bytes.NewReader(prefix), response.Body), closer: response.Body}
			if readErr == nil && c.GetInt("channel_type") == constant.ChannelTypeAli {
				input, _ = AliFailure(response.StatusCode, response.Header.Get("Retry-After"), prefix)
			}
			if readErr == nil && len(prefix) <= 8192 && openAIAccountErrors(c.GetInt("channel_type")) {
				var envelope struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if common.Unmarshal(prefix, &envelope) == nil {
					switch envelope.Error.Code {
					case "invalid_api_key":
						if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
							input.Refusal = RefusalInvalidCredential
						}
					case "insufficient_quota":
						input.Refusal = RefusalQuotaExhausted
					}
				}
			}
		}
		decision := ClassifyFailure(input, now)
		if !decision.Failure.RetryAt.IsZero() {
			SetUnavailableHeader(c, &model.UpstreamAccountUnavailable{RetryAt: decision.Failure.RetryAt})
		}
		if decision.Failure.Kind != "" {
			scope := state.Scope
			if decision.SharedQuota {
				scope = model.UpstreamAccountScope{QuotaGroup: state.Sent.Ref.QuotaGroup}
			}
			err = model.RecordUpstreamAccountFailure(ctx, state.DB, *state.Sent, scope, decision.Failure, now)
		}
	}
	state.PersistenceError = err
	return err
}

type restoredBody struct {
	io.Reader
	closer io.Closer
}

func (body *restoredBody) Close() error { return body.closer.Close() }

func RetryAllowed(c *gin.Context) bool {
	state := State(c)
	return (c.Request == nil || c.Request.Context().Err() == nil) && (c.Writer == nil || !c.Writer.Written()) && c.GetString("key_source") != "byok" && !state.Veto && !state.Uncertain && !state.UnsafeResponse && state.PersistenceError == nil && !state.ResourceRejected && (state.MaxSends == 0 || state.Sends < state.MaxSends)
}

func SetUnavailableHeader(c *gin.Context, err error) {
	var unavailable *model.UpstreamAccountUnavailable
	if errors.As(err, &unavailable) && !unavailable.RetryAt.IsZero() {
		seconds := int64(time.Until(unavailable.RetryAt).Seconds()) + 1
		c.Header("Retry-After", strconv.FormatInt(max(seconds, 1), 10))
	}
}

// SelectChannel preserves raw adapter credentials (including AppID+Token).
func SelectChannel(c *gin.Context, channel *model.Channel, modelName string) (string, int, error) {
	state := State(c)
	if !state.ResolvedRoute {
		mapping := map[string]string{}
		if raw := channel.GetModelMapping(); raw != "" && raw != "{}" {
			if err := common.UnmarshalJsonStr(raw, &mapping); err != nil {
				return "", 0, errors.New("unmarshal_model_mapping_failed")
			}
		}
		var err error
		modelName, err = common.ResolveModelMapping(modelName, mapping)
		if err != nil {
			return "", 0, err
		}
	}
	scope := RequestScope(c, modelName)
	refs, keys, err := model.ChannelAccountRefs(c.Request.Context(), model.DB, channel)
	if err != nil {
		return "", 0, err
	}
	if state.Selected != nil && !state.Excluded[state.Selected.Ref.MemberID] && state.Scope == scope {
		for _, ref := range refs {
			if ref.MemberID == state.Selected.Ref.MemberID {
				return keys[ref.KeyIndex], ref.KeyIndex, nil
			}
		}
	}
	ResetSelection(c)
	state.DB, state.Candidates, state.Scope, state.Random = model.DB, refs, scope, channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeRandom
	selected, err := selectEligible(c)
	if err != nil {
		SetUnavailableHeader(c, err)
		return "", 0, err
	}
	Prepare(c, model.DB, selected, scope)
	return keys[selected.KeyIndex], selected.KeyIndex, nil
}

// RequestScope makes provider endpoint aliases share one operation health scope.
func RequestScope(c *gin.Context, modelName string) model.UpstreamAccountScope {
	_, operation, err := capability.ResolveRequest(c.Request.Method, c.Request.URL.Path)
	if err != nil {
		return model.UpstreamAccountScope{Operation: c.Request.URL.Path, Model: modelName}
	}
	return model.UpstreamAccountScope{Operation: string(operation), Model: modelName}
}

func selectEligible(c *gin.Context) (model.UpstreamAccountSelection, error) {
	state := State(c)
	if !state.Random {
		return model.SelectUpstreamAccount(c.Request.Context(), state.DB, state.Candidates, state.Scope, state.Excluded, time.Now())
	}
	var earliest time.Time
	for _, ref := range state.Candidates {
		selected, err := model.SelectUpstreamAccount(c.Request.Context(), state.DB, []model.UpstreamAccountRef{ref}, state.Scope, state.Excluded, time.Now())
		if err == nil {
			return selected, nil
		}
		var unavailable *model.UpstreamAccountUnavailable
		if !errors.As(err, &unavailable) {
			return selected, err
		}
		if !unavailable.RetryAt.IsZero() && (earliest.IsZero() || unavailable.RetryAt.Before(earliest)) {
			earliest = unavailable.RetryAt
		}
	}
	return model.UpstreamAccountSelection{}, &model.UpstreamAccountUnavailable{RetryAt: earliest}
}

// PrepareNext admits a healthy, untried member of the same authorized pool.
// The next adapter reuses this selection; no network send is spent probing health.
func PrepareNext(c *gin.Context) (bool, error) {
	state := State(c)
	if state.Sent == nil || state.Selected == nil || state.Sent.Ref.MemberID != state.Selected.Ref.MemberID || len(state.Candidates) == 0 {
		return false, nil
	}
	selected, err := selectEligible(c)
	if err != nil {
		var unavailable *model.UpstreamAccountUnavailable
		if errors.As(err, &unavailable) {
			SetUnavailableHeader(c, err)
			return false, nil
		}
		state.PersistenceError = err
		return false, err
	}
	state.Selected = &selected
	return true, nil
}

// Resource ownership cannot be inferred from an opaque upstream ID. Until an
// owning resource binding is available, pooled requests containing such IDs are
// rejected. This is deliberately stricter than legacy single-account relay.
func ValidateResources(c *gin.Context) error {
	if State(c).Selected == nil {
		return nil
	}
	if !strings.Contains(c.Request.Header.Get("Content-Type"), "json") {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	body, err := storage.Bytes()
	if err != nil {
		return err
	}
	var value any
	if err := common.Unmarshal(body, &value); err != nil {
		return err
	}
	if preparedValue, exists := c.Get(string(constant.ContextKeyResponsesPreparedRequest)); exists {
		if prepared, ok := preparedValue.(*dto.OpenAIResponsesRequest); ok && prepared != nil && prepared.PreviousResponseID == "" {
			encoded, err := common.Marshal(prepared)
			if err != nil {
				return err
			}
			var expanded any
			if err := common.Unmarshal(encoded, &expanded); err != nil {
				return err
			}
			if containsOwnedResource(expanded) {
				return errors.New("account-bound resource requires its original upstream account")
			}
			// Only a successfully scoped local expansion may replace the parent ID.
			if object, ok := value.(map[string]any); ok {
				delete(object, "previous_response_id")
			}
		}
	}
	if containsOwnedResource(value) {
		return errors.New("account-bound resource requires its original upstream account")
	}
	return nil
}

func containsOwnedResource(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		if value["type"] == "item_reference" {
			return true
		}
		for key, item := range value {
			switch key {
			case "previous_response_id", "file_id", "file_uri", "fileUri", "encrypted_content", "video_id", "task_id", "continue_clip_id":
				if text, ok := item.(string); ok && text != "" {
					return true
				}
			}
			// Client task IDs are local continuity identities, not provider resources.
			if key != "client_metadata" && containsOwnedResource(item) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if containsOwnedResource(item) {
				return true
			}
		}
	}
	return false
}

// Only the known OpenAI-compatible credential-error contract is interpreted
// here. Other adapters must add a protocol-specific parser; e.g. invalid_token
// can describe a resource/session token and is not a credential diagnosis.
func openAIAccountErrors(channelType int) bool {
	switch channelType {
	case constant.ChannelTypeOpenAI, constant.ChannelTypeAzure, constant.ChannelTypeGateway,
		constant.ChannelTypeDeepSeek, constant.ChannelTypeMoonshot, constant.ChannelTypeSiliconFlow:
		return true
	}
	return false
}

// ValidateOutboundResources checks the final adapter body too: administrator
// parameter overrides and conversions must not reintroduce account-bound IDs
// after request-side continuity checks. Responses background mode is taken
// from this final body before BeginSend, so asynchronous work uses the existing
// execution-uncertainty/no-replay policy even on an otherwise synchronous path.
func ValidateOutboundResources(c *gin.Context, request *http.Request) error {
	state := State(c)
	state.ResponsesBackground = false
	_, operation, _ := capability.ResolveRequest(c.Request.Method, c.Request.URL.Path)
	inspectResponses := operation == capability.OperationResponseCreate
	if (state.Selected == nil && !inspectResponses) || (!strings.Contains(request.Header.Get("Content-Type"), "json") && !inspectResponses) || request.Body == nil {
		return nil
	}
	if request.GetBody == nil {
		return errors.New("validated JSON transport requires a replayable body for resource and asynchronous admission")
	}
	body, err := request.GetBody()
	if err != nil {
		return err
	}
	defer body.Close()
	var value any
	if err := common.DecodeJson(body, &value); err != nil {
		return err
	}
	if inspectResponses {
		if object, ok := value.(map[string]any); ok {
			if background, exists := object["background"]; exists && background != nil {
				flag, ok := background.(bool)
				if !ok {
					return errors.New("background must be a boolean")
				}
				state.ResponsesBackground = flag
			}
		}
	}
	if state.Selected != nil && containsOwnedResource(value) {
		return errors.New("final upstream body contains an unbound account resource")
	}
	return nil
}
