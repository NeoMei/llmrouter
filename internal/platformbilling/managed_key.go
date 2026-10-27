package platformbilling

import (
	"context"
	"net/http"
	"slices"
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
	if grant.ID == "" || grant.Audience != "llmrouter" || grant.ActorID == "" || grant.AccountID == "" || grant.OriginProductID == "" || (model != "" && !slices.Contains(grant.Models, model)) {
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

// ManagedModels returns only the current credential's explicit model grant.
// Empty-model verification still enforces central membership and revocation.
func (a *Adapter) ManagedModels(ctx context.Context, credential string) ([]string, error) {
	if !a.Enabled() || !strings.HasPrefix(credential, "uh_key_") || !IsModelCredential(credential) {
		return nil, ErrInvalid
	}
	var grant Claims
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", map[string]string{"token": credential, "model": "", "instance_id": a.config.InstanceID}, &grant); err != nil {
		return nil, err
	}
	if err := a.validateModelCredential(credential, "", "", grant); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(grant.Models))
	for _, model := range grant.Models {
		if strings.TrimSpace(model) != model || model == "" || len(model) > 255 || model == "*" {
			return nil, ErrInvalid
		}
		if !slices.Contains(models, model) {
			models = append(models, model)
		}
	}
	slices.Sort(models)
	return models, nil
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
func (a *Adapter) ManagedRequestBody(operation string, body []byte) ([]byte, error) {
	if operation != "chat" && operation != "responses" {
		return body, nil
	}
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || request == nil {
		return nil, ErrInvalid
	}
	field := "max_output_tokens"
	if operation == "chat" {
		field = "max_tokens"
		if _, present := request["max_completion_tokens"]; present {
			return body, nil
		}
	}
	if _, present := request[field]; present {
		return body, nil
	}
	request[field] = a.config.MaxOutputTokens
	return common.Marshal(request)
}
