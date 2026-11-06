package platformbilling

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// IsModelCredential distinguishes centrally verified model authority from any
// local dashboard or relay key. Prefix recognition alone grants no authority.
func IsModelCredential(credential string) bool {
	return len(credential) <= 1024 && (strings.HasPrefix(credential, "uh_model_") || strings.HasPrefix(credential, "uh_key_"))
}

func (a *Adapter) validateModelCredential(credential, task, model string, grant Claims) error {
	if grant.ModelScope != "" && grant.ModelScope != "selected" && !grant.NativePricing() {
		return ErrInvalid
	}
	if grant.ID == "" || grant.Audience != "llmrouter" || grant.ActorID == "" || grant.AccountID == "" || grant.OriginProductID == "" || (model != "" && !grant.NativePricing() && !slices.Contains(grant.Models, model)) {
		return ErrInvalid
	}
	if strings.HasPrefix(credential, "uh_key_") {
		if grant.CredentialKind != "api_key" || grant.InstanceID != a.config.InstanceID || (!grant.ExpiresAt.IsZero() && !grant.ExpiresAt.After(time.Now())) || grant.TaskID != "" {
			return ErrInvalid
		}
		return nil
	}
	// Old task credentials without an instance belong only to the default
	// deployment. They keep their original exact task and expiration checks.
	instance := grant.InstanceID
	if instance == "" {
		instance = "default"
	}
	if !strings.HasPrefix(credential, "uh_model_") || (grant.CredentialKind != "" && grant.CredentialKind != "task" && grant.CredentialKind != "model_task") || instance != a.config.InstanceID || grant.TaskID != task || !grant.ExpiresAt.After(time.Now()) {
		return ErrInvalid
	}
	return nil
}

// NativePricing is explicit delegated catalog authority, never a wildcard in
// the legacy model list. Identity, membership and revocation remain central.
func (grant Claims) NativePricing() bool {
	return grant.ModelScope == "llmrouter_available" && grant.PricingSource == "llmrouter_native" && grant.CredentialVersion > 0 && grant.CredentialKind == "api_key" && grant.OriginProductID == "superwagie" && grant.Asset == "CNY/6" && grant.FundingMode == "prepaid_cash"
}

func (a *Adapter) NativePrincipal() (int, string) {
	if a == nil {
		return 0, ""
	}
	return a.config.PrincipalID, a.config.NativeGroup
}

// ManagedModels returns only the current credential's explicit model grant.
// Empty-model verification still enforces central membership and revocation.
func (a *Adapter) ManagedModels(ctx context.Context, credential string) ([]string, error) {
	grant, err := a.ManagedModelGrant(ctx, credential)
	if err != nil {
		return nil, err
	}
	return grant.Models, nil
}

// ManagedModelGrant retains verified operation authority for credential-scoped
// discovery. Models remain normalized exactly as in ManagedModels.
func (a *Adapter) ManagedModelGrant(ctx context.Context, credential string) (Claims, error) {
	if !a.Enabled() || !strings.HasPrefix(credential, "uh_key_") || !IsModelCredential(credential) {
		return Claims{}, ErrInvalid
	}
	var grant Claims
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", map[string]string{"token": credential, "model": "", "instance_id": a.config.InstanceID}, &grant); err != nil {
		return Claims{}, err
	}
	if err := a.validateModelCredential(credential, "", "", grant); err != nil {
		return Claims{}, err
	}
	models := make([]string, 0, len(grant.Models))
	for _, model := range grant.Models {
		if strings.TrimSpace(model) != model || model == "" || len(model) > 255 || model == "*" {
			return Claims{}, ErrInvalid
		}
		if !slices.Contains(models, model) {
			models = append(models, model)
		}
	}
	slices.Sort(models)
	grant.Models = models
	return grant, nil
}

// ManagedTask verifies the durable key before looking up a persisted job. The
// original task is derived only from a call owned by the same immutable key,
// actor, payer, origin and deployment; caller task headers cannot choose it.
func (a *Adapter) ManagedTask(ctx context.Context, credential, id string) (Call, error) {
	if !a.Enabled() || !strings.HasPrefix(credential, "uh_key_") || !IsModelCredential(credential) || id == "" || len(id) > 255 || strings.TrimSpace(id) != id {
		return Call{}, ErrInvalid
	}
	var grant Claims
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", map[string]string{"token": credential, "model": "", "purpose": "receipt", "instance_id": a.config.InstanceID}, &grant); err != nil {
		return Call{}, err
	}
	if err := a.validateModelCredential(credential, "", "", grant); err != nil {
		return Call{}, err
	}
	idScope := a.db.Where("call_id = ? OR provider_task_id = ?", id, id)
	// SDKs receive public job IDs, while the platform journal stores supplier
	// task IDs. Resolve the public binding within the already verified key scope.
	if a.db.Migrator().HasTable("tasks") {
		publicTasks := a.db.Table("tasks").Select("platform_call_id").Where("task_id = ? AND funding_owner = ?", id, "platform")
		idScope = idScope.Or("call_id IN (?)", publicTasks)
	}
	if a.db.Migrator().HasTable("ocr_jobs") {
		publicJobs := a.db.Table("ocr_jobs").Select("platform_call_id").Where("id = ? AND funding = ?", id, "platform")
		idScope = idScope.Or("call_id IN (?)", publicJobs)
	}
	var rows []Call
	err := a.db.WithContext(ctx).Where("credential_id = ? AND actor_id = ? AND account_id = ? AND origin_product_id = ? AND instance_id = ?", grant.ID, grant.ActorID, grant.AccountID, grant.OriginProductID, a.config.InstanceID).Where(idScope).Limit(2).Find(&rows).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return Call{}, ErrInvalid
		}
		return Call{}, err
	}
	if len(rows) != 1 {
		return Call{}, ErrInvalid
	}
	row := rows[0]
	if a.validateModelCredential(credential, row.TaskID, row.Model, grant) != nil {
		return Call{}, ErrInvalid
	}
	return row, nil
}

// ManagedRequestBody applies deployment-owned output bounds when SDKs omit
// them. Existing task credentials retain their explicit-bounds contract.
func (a *Adapter) ManagedRequestBody(operation string, body []byte, callIDs ...string) ([]byte, error) {
	if operation != "chat" && operation != "responses" && operation != "claude.messages.create" && operation != "gemini.generate" {
		return body, nil
	}
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || request == nil {
		return nil, ErrInvalid
	}
	if operation == "gemini.generate" {
		generation, ok := request["generationConfig"].(map[string]any)
		if !ok {
			if _, exists := request["generationConfig"]; exists {
				return nil, ErrInvalid
			}
			generation = map[string]any{}
			request["generationConfig"] = generation
		}
		if _, exists := generation["maxOutputTokens"]; exists {
			return body, nil
		}
		if _, exists := generation["max_output_tokens"]; exists {
			return body, nil
		}
		generation["maxOutputTokens"] = a.config.MaxOutputTokens
		return common.Marshal(request)
	}
	field := "max_output_tokens"
	if operation == "chat" || operation == "claude.messages.create" {
		field = "max_tokens"
		if _, present := request["max_completion_tokens"]; present {
			return body, nil
		}
	}
	if _, present := request[field]; present {
		return body, nil
	}
	output := a.config.MaxOutputTokens
	model, _ := request["model"].(string)
	wire := legacyWireOperation(operation)
	p, preserveBytes := a.config.TextInputPolicies[wire][model]
	if preserveBytes {
		output = min(output, p.MaxOutputTokens)
	}
	if len(callIDs) == 1 && stableID.MatchString(callIDs[0]) && a.db != nil {
		var old Call
		err := a.db.First(&old, "call_id = ?", callIDs[0]).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPending
		}
		oldWire := old.WireOperation
		if oldWire == "" && old.Operation == "" {
			oldWire = "chat.completions.create"
		} else if oldWire == "" {
			oldWire = legacyWireOperation(old.Operation)
		}
		if err == nil && old.Model == model && oldWire == wire {
			// Normalization grants no authority: Admit/Receipt still authenticate
			// and compare the original scope. Preserve historical serialization,
			// or the new call's frozen output, before that duplicate comparison.
			preserveBytes = old.TextInputSnapshot != ""
			output = a.config.MaxOutputTokens
			if old.ReservationOutput > 0 && old.ReservationOutput <= 32768 {
				output = int(old.ReservationOutput)
			} else if preserveBytes {
				return nil, ErrInvalid
			}
		}
	}
	if preserveBytes {
		// Keep every original byte when adding the server-owned default so the
		// full-body reservation cannot discard whitespace or JSON escape bytes.
		end := bytes.LastIndexByte(body, '}')
		if end < 0 {
			return nil, ErrInvalid
		}
		addition := `,"` + field + `":` + strconv.Itoa(output)
		out := make([]byte, 0, len(body)+len(addition))
		out = append(out, body[:end]...)
		out = append(out, addition...)
		out = append(out, body[end:]...)
		return out, nil
	}
	request[field] = output
	return common.Marshal(request)
}
