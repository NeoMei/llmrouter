package platformbilling

// Private, disabled result-read foundation. It is not registered as a route.
// An active task credential proves capability scope but does not alone prove
// a currently logged-in user or present account membership for that actor.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

type knowledgeSettledReceipt struct {
	RemoteCall
	TaskID       string `json:"task_id"`
	ServiceID    string `json:"service_id"`
	Usage        *Usage `json:"usage"`
	FinalDigest  string `json:"final_digest"`
	ProviderCost string `json:"provider_cost"`
}

// This read-only client rejects redirect and duplicate-key authority replies.
func readKnowledgeAuthority(ctx context.Context, c *client, method, path string, in, out any) error {
	if c == nil || c.http == nil {
		return ErrPending
	}
	var body []byte
	var err error
	if in != nil {
		body, err = common.Marshal(in)
		if err != nil {
			return ErrInvalid
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(body))
	if err != nil {
		return ErrPending
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	h := *c.http
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := h.Do(request)
	if err != nil {
		return ErrPending
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || response.StatusCode < 200 || response.StatusCode >= 300 || common.UnmarshalUnique(data, out) != nil {
		return ErrPending
	}
	return nil
}

// authorizedResult verifies a task capability, an authoritative UniHub call,
// and the original v2 result binding. It never dispatches or settles a call.
func (v *knowledgeResultVault) authorizedResult(ctx context.Context, a *Adapter, credential, task, id string) ([]byte, error) {
	if v == nil || a == nil || a.db == nil || !a.Enabled() || a.client == nil || !stableID.MatchString(id) || !stableID.MatchString(task) || !strings.HasPrefix(credential, "uh_model_") || !IsModelCredential(credential) {
		return nil, ErrInvalid
	}
	left, err := v.db.DB()
	if err != nil {
		return nil, ErrPending
	}
	right, err := a.db.DB()
	if err != nil || left != right {
		return nil, ErrInvalid
	}
	var original Call
	if err := v.db.WithContext(ctx).First(&original, "call_id = ?", id).Error; err != nil {
		return nil, ErrInvalid
	}
	if original.TaskID != task || original.Status != "SETTLED" || original.EvidenceConflict || !((original.Operation == "embedding" && original.WireOperation == "embeddings.create") || (original.Operation == "rerank" && original.WireOperation == "rerank.create")) {
		return nil, ErrInvalid
	}
	verify := func() (Claims, error) {
		var grant Claims
		if err := readKnowledgeAuthority(ctx, a.client, http.MethodPost, "/internal/v1/model-credentials/verify", map[string]string{"token": credential, "model": original.Model, "wire_operation": original.WireOperation, "purpose": "result_read", "instance_id": a.config.InstanceID}, &grant); err != nil {
			return Claims{}, err
		}
		instance := original.InstanceID
		if instance == "" {
			instance = "default"
		}
		if a.validateModelCredential(credential, task, original.Model, grant) != nil || instance != a.config.InstanceID || original.CallID != id || grant.ActorID != original.ActorID || grant.AccountID != original.AccountID || grant.OriginProductID != original.OriginProductID || !allowsWire(grant, original.Model, original.WireOperation) {
			return Claims{}, ErrInvalid
		}
		return grant, nil
	}
	first, err := verify()
	if err != nil {
		return nil, ErrInvalid
	}
	var remote knowledgeSettledReceipt
	if err = readKnowledgeAuthority(ctx, a.client, http.MethodGet, "/internal/v1/calls/"+id, nil, &remote); err != nil {
		return nil, ErrPending
	}
	if remote.Status != "SETTLED" || remote.ServiceID != "llmrouter" || remote.TaskID != task || original.FundingSnapshot == "" || remote.fundingSnapshot() != original.FundingSnapshot || !remote.RemoteCall.matches(original) || remote.Charged != original.Charged || remote.Usage == nil {
		return nil, ErrInvalid
	}
	charged, ok := new(big.Int).SetString(remote.Charged, 10)
	if !ok || charged.Sign() < 0 || charged.String() != remote.Charged {
		return nil, ErrInvalid
	}
	var evidence metering.Usage
	var intent struct {
		Usage        Usage  `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}
	if common.UnmarshalUnique([]byte(original.Evidence), &evidence) != nil || common.UnmarshalUnique([]byte(original.Intent), &intent) != nil || intent.Usage.Metered == nil || remote.Usage.Metered == nil || !reflect.DeepEqual(*intent.Usage.Metered, evidence) || !reflect.DeepEqual(*remote.Usage, intent.Usage) || remote.ProviderCost != intent.ProviderCost {
		return nil, ErrInvalid
	}
	digestInput, err := common.Marshal(struct {
		CallID       string `json:"call_id"`
		Usage        *Usage `json:"usage"`
		ProviderCost string `json:"provider_cost"`
	}{id, &intent.Usage, intent.ProviderCost})
	if err != nil {
		return nil, ErrInvalid
	}
	digest := sha256.Sum256(digestInput)
	if remote.FinalDigest != hex.EncodeToString(digest[:]) {
		return nil, ErrInvalid
	}
	second, err := verify()
	if err != nil || !reflect.DeepEqual(first, second) {
		return nil, ErrInvalid
	}
	var initial knowledgeResultRow
	if err := v.db.WithContext(ctx).Select("call_id", "binding_version", "attempt_id").First(&initial, "call_id = ?", id).Error; err != nil || initial.BindingVersion != 2 || initial.AttemptID == 0 {
		return nil, ErrPending
	}
	var output []byte
	var resultExpires int64
	err = v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Attempt{}).Where("id = ? AND call_id = ?", initial.AttemptID, id).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return ErrPending
		}
		if err := tx.Model(&Call{}).Where("call_id = ?", id).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return ErrPending
		}
		var row Call
		if err := tx.First(&row, "call_id = ?", id).Error; err != nil {
			return ErrPending
		}
		if row.Status != "SETTLED" || row.EvidenceConflict || !sameKnowledgeCallBinding(row, original) || row.Evidence != original.Evidence || row.Intent != original.Intent || row.Charged != original.Charged {
			return ErrPending
		}
		var stored knowledgeResultRow
		if err := tx.First(&stored, "call_id = ?", id).Error; err != nil {
			return ErrPending
		}
		if stored.BindingVersion != 2 || stored.AttemptID != initial.AttemptID || stored.ExpiresAt <= v.now().UnixMilli() || len(stored.Ciphertext) < v.aead.NonceSize() {
			return ErrPending
		}
		if !second.ExpiresAt.After(time.Now()) || ctx.Err() != nil {
			return ErrPending
		}
		resultExpires = stored.ExpiresAt
		var attempt Attempt
		if err := tx.First(&attempt, "id = ? AND call_id = ?", stored.AttemptID, id).Error; err != nil || attempt.Status != "DISPATCHED" {
			return ErrPending
		}
		var frozen frozenKnowledge
		if common.UnmarshalUnique([]byte(row.MediaSnapshot), &frozen) != nil || frozen.Policy.ChannelID != attempt.ChannelID {
			return ErrInvalid
		}
		aad, err := knowledgeResultStoredAAD(row, stored)
		if err != nil {
			return ErrInvalid
		}
		output, err = v.aead.Open(nil, stored.Ciphertext[:v.aead.NonceSize()], stored.Ciphertext[v.aead.NonceSize():], aad)
		if err != nil {
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil || !second.ExpiresAt.After(time.Now()) || resultExpires <= v.now().UnixMilli() {
		return nil, ErrPending
	}
	return output, nil
}
