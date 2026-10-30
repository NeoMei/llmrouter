package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"time"
)

// Field order is the canonical JSON receipt wire order. This transport evidence
// is authenticated by the trusted gateway response, not a transferable bearer.
type KnowledgeReceipt struct {
	Version                int                 `json:"version"`
	Issuer                 string              `json:"issuer"`
	Audience               string              `json:"audience"`
	OriginProductID        string              `json:"origin_product_id"`
	InstanceID             string              `json:"instance_id"`
	ActorID                string              `json:"actor_id"`
	AccountID              string              `json:"account_id"`
	TaskID                 string              `json:"task_id"`
	CallID                 string              `json:"call_id"`
	AttemptID              uint                `json:"attempt_id"`
	Status                 string              `json:"status"`
	ProducerCredentialID   string              `json:"producer_credential_id"`
	AuthorizedCredentialID string              `json:"authorized_credential_id"`
	Model                  string              `json:"model"`
	WireOperation          string              `json:"wire_operation"`
	PolicyDigest           string              `json:"policy_digest"`
	RouteGeneration        string              `json:"route_generation"`
	EmbeddingSpace         *dto.EmbeddingSpace `json:"embedding_space,omitempty"`
	ContentSHA256          string              `json:"content_sha256"`
	IssuedAt               int64               `json:"issued_at"`
	ExpiresAt              int64               `json:"expires_at"`
}
type KnowledgeResultEvidence struct {
	RawBytes []byte
	Receipt  KnowledgeReceipt
	Header   string
}

func (a *Adapter) KnowledgeResultWithEvidence(ctx context.Context, actor, credential, task, id string) (KnowledgeResultEvidence, error) {
	if a == nil || !a.config.EnableKnowledgeResultRead || a.knowledgeResults == nil {
		return KnowledgeResultEvidence{}, ErrInvalid
	}
	var receipt KnowledgeReceipt
	raw, err := a.knowledgeResults.authorizedResultWithEvidence(ctx, a, actor, credential, task, id, &receipt)
	if err != nil {
		return KnowledgeResultEvidence{}, err
	}
	if receipt.Version == 0 {
		return KnowledgeResultEvidence{RawBytes: raw}, nil
	}
	encoded, err := common.Marshal(receipt)
	if err != nil {
		return KnowledgeResultEvidence{}, ErrInvalid
	}
	header := base64.RawURLEncoding.EncodeToString(encoded)
	if len(header) > 4096 || receipt.ExpiresAt <= time.Now().Unix() || ctx.Err() != nil {
		return KnowledgeResultEvidence{}, ErrPending
	}
	return KnowledgeResultEvidence{RawBytes: raw, Receipt: receipt, Header: header}, nil
}
func historicalKnowledgeReceipt(row Call, attempt Attempt, f frozenKnowledge, reader knowledgeReadGrant, raw []byte, resultExpiry, now int64) (KnowledgeReceipt, error) {
	b := f.Binding
	if b == nil || !ApprovedKnowledgePair(row.Model, row.WireOperation) || !opaqueKnowledgeID(b.PolicyDigest) || !opaqueKnowledgeID(b.RouteGeneration) || b.InstanceID != row.InstanceID || b.OriginProductID != row.OriginProductID || b.Model != row.Model || b.WireOperation != row.WireOperation || b.ChannelID != attempt.ChannelID || attempt.CallID != row.CallID || row.CredentialID == "" || reader.ID == "" {
		return KnowledgeReceipt{}, ErrInvalid
	}
	expiry := min(resultExpiry/1000, reader.ActorExpiresAt, reader.ExpiresAt.Unix(), now+30)
	if expiry <= now {
		return KnowledgeReceipt{}, ErrPending
	}
	sum := sha256.Sum256(raw)
	return KnowledgeReceipt{Version: 1, Issuer: "llmrouter-knowledge-v1", Audience: row.OriginProductID, OriginProductID: row.OriginProductID, InstanceID: row.InstanceID, ActorID: row.ActorID, AccountID: row.AccountID, TaskID: row.TaskID, CallID: row.CallID, AttemptID: attempt.ID, Status: "SETTLED", ProducerCredentialID: row.CredentialID, AuthorizedCredentialID: reader.ID, Model: row.Model, WireOperation: row.WireOperation, PolicyDigest: b.PolicyDigest, RouteGeneration: b.RouteGeneration, EmbeddingSpace: f.Policy.Space, ContentSHA256: hex.EncodeToString(sum[:]), IssuedAt: now, ExpiresAt: expiry}, nil
}
