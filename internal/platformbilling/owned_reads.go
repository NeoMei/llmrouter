package platformbilling

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type OwnedReadClaims struct {
	CredentialKind         string    `json:"credential_kind"`
	Audience               string    `json:"audience"`
	CallID                 string    `json:"call_id"`
	RequestDigest          string    `json:"request_digest"`
	Model                  string    `json:"model"`
	WireOperation          string    `json:"wire_operation"`
	TaskID                 string    `json:"task_id"`
	CredentialID           string    `json:"credential_id"`
	OriginalCredentialKind string    `json:"original_credential_kind"`
	ActorID                string    `json:"actor_id"`
	AccountID              string    `json:"account_id"`
	OriginProductID        string    `json:"origin_product_id"`
	InstanceID             string    `json:"instance_id"`
	ClientID               string    `json:"client_id"`
	ExpiresAt              time.Time `json:"expires_at"`
}

func IsOwnedReadCredential(token string) bool {
	return len(token) == 72 && strings.HasPrefix(token, "uh_read_")
}

// OwnedTaskRead accepts no caller-supplied actor, model, key or provider ID.
// UniHub authenticates the original owner independently of model eligibility.
func (a *Adapter) OwnedTaskRead(ctx context.Context, token, callID string) (Call, error) {
	if !a.Enabled() || !IsOwnedReadCredential(token) || !stableID.MatchString(callID) {
		return Call{}, ErrInvalid
	}
	var row Call
	if err := a.db.WithContext(ctx).First(&row, "call_id = ?", callID).Error; err != nil {
		return Call{}, ErrInvalid
	}
	if !((row.Operation == "ocr" && row.WireOperation == "ocr.jobs.create") || (row.Operation == "video" && row.WireOperation == "videos.create")) || row.ProviderTaskID == "" {
		return Call{}, ErrInvalid
	}
	var claims OwnedReadClaims
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/call-reads/verify", "", map[string]string{"token": token, "call_id": callID, "instance_id": a.config.InstanceID}, &claims); err != nil {
		return Call{}, err
	}
	if claims.CredentialKind != "owned_call_read_v1" || claims.Audience != "llmrouter-read" || claims.ClientID == "" || !claims.ExpiresAt.After(time.Now()) || claims.ExpiresAt.After(time.Now().Add(5*time.Minute)) || claims.CallID != row.CallID || claims.RequestDigest != row.Digest || claims.Model != row.Model || claims.WireOperation != row.WireOperation || claims.TaskID != row.TaskID || claims.CredentialID != row.CredentialID || (row.CredentialKind != "" && claims.OriginalCredentialKind != row.CredentialKind) || claims.ActorID != row.ActorID || claims.AccountID != row.AccountID || claims.OriginProductID != row.OriginProductID || claims.InstanceID != row.InstanceID || row.InstanceID != a.config.InstanceID {
		return Call{}, ErrInvalid
	}
	// A provider binding without an actual durable dispatch attempt is not a job.
	var attempt Attempt
	if err := a.db.WithContext(ctx).Where("call_id = ?", row.CallID).Order("id DESC").First(&attempt).Error; err != nil || attempt.ChannelID < 1 {
		return Call{}, ErrInvalid
	}
	if claims.OriginalCredentialKind != "task" && claims.OriginalCredentialKind != "api_key" {
		return Call{}, ErrInvalid
	}
	if row.CredentialKind == "" {
		updated := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND (credential_kind = ? OR credential_kind IS NULL) AND digest = ? AND credential_id = ? AND actor_id = ? AND account_id = ? AND origin_product_id = ? AND instance_id = ? AND model = ? AND task_id = ? AND wire_operation = ? AND provider_task_id = ?", row.CallID, "", row.Digest, row.CredentialID, row.ActorID, row.AccountID, row.OriginProductID, row.InstanceID, row.Model, row.TaskID, row.WireOperation, row.ProviderTaskID).Update("credential_kind", claims.OriginalCredentialKind)
		if updated.Error != nil || updated.RowsAffected != 1 {
			return Call{}, ErrConflict
		}
		row.CredentialKind = claims.OriginalCredentialKind
	}
	return row, nil
}
