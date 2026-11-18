package platformbilling

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
)

const productMediaTTL = 24 * time.Hour
const productMediaMaxBytes = 24 << 20

type productMediaResult struct {
	CallID      string `gorm:"primaryKey;size:128"`
	ContentType string `gorm:"size:64"`
	SHA256      string `gorm:"size:64"`
	ExpiresAt   int64  `gorm:"index"`
	Sealed      []byte
}

func (productMediaResult) TableName() string { return "platform_product_media_results" }

func (a *Adapter) productMediaCipher() (cipher.AEAD, error) {
	// Separate the encryption key from its transport use. All instances
	// serving this journal must retain the same configured billing key.
	key := sha256.Sum256([]byte("llmrouter-product-media-result-v1\x00" + a.config.ServiceToken))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func productMediaType(wire, ct string) bool {
	switch wire {
	case "bailian.audio.speech.create":
		return ct == "audio/mpeg" || ct == "audio/wav"
	case "multimodal.embeddings.create", "dashscope.audio.transcriptions.create", "images.generations.create", "gemini.images.generate", "dashscope.images.generate", "images.edits.create":
		return ct == "application/json"
	}
	return false
}

func (r productMediaResult) binding(row Call) ([]byte, error) {
	return common.Marshal([]any{"product-media-result-v1", row.CallID, row.TaskID, row.CreditScope, row.ActorID, row.AccountID,
		row.OriginProductID, row.InstanceID, row.CredentialID, row.CredentialVersion, row.Digest,
		row.WireOperation, row.NativeQuoteJSON, r.ContentType, r.SHA256, r.ExpiresAt})
}

// CaptureProductMediaResult records certified output and measured settlement
// intent in the same transaction. It never exposes a response before settlement.
func (s *Session) CaptureProductMediaResult(ctx context.Context, u metering.Usage, ct string, body []byte) error {
	if !s.ProductCredit() || s.call.ProviderTaskID != "" || !productMediaType(s.call.WireOperation, ct) ||
		len(body) == 0 || len(body) > productMediaMaxBytes || s.adapter.productCreditMetering == nil || u.State != "confirmed" {
		return ErrInvalid
	}
	quota, err := s.adapter.productCreditMetering(s.call.NativePriceSnapshot, u)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	artifact := productMediaResult{CallID: s.call.CallID, ContentType: ct, SHA256: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(productMediaTTL).Unix()}
	aead, err := s.adapter.productMediaCipher()
	if err != nil {
		return err
	}
	aad, err := artifact.binding(s.call)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	artifact.Sealed = aead.Seal(nonce, nonce, body, aad)
	return s.settleNative(ctx, quota, &Usage{Metered: &u}, &artifact)
}

// ReadProductMediaResult matches a live origin proof and immutable key to the
// original call before decrypting. Receipt verification uses only service authority.
func (a *Adapter) ReadProductMediaResult(ctx context.Context, credential, task, callID string) ([]byte, string, string, error) {
	if !HasProductAuthorization(ctx) {
		return nil, "", "", ErrInvalid
	}
	row, err := a.Task(ctx, credential, task, callID)
	if err != nil || row.CreditScope != ProductCreditScope || row.Status != "SETTLED" || row.EvidenceConflict || row.ProviderTaskID != "" {
		return nil, "", "", ErrInvalid
	}
	if err = a.verifyNativeTaskSettlement(ctx, row); err != nil {
		return nil, "", "", err
	}
	var artifact productMediaResult
	if a.db.WithContext(ctx).First(&artifact, "call_id = ? AND expires_at > ?", row.CallID, time.Now().Unix()).Error != nil ||
		!productMediaType(row.WireOperation, artifact.ContentType) || len(artifact.Sealed) > productMediaMaxBytes+128 {
		return nil, "", "", ErrInvalid
	}
	aead, err := a.productMediaCipher()
	if err != nil || len(artifact.Sealed) < aead.NonceSize() {
		return nil, "", "", ErrInvalid
	}
	aad, err := artifact.binding(row)
	if err != nil {
		return nil, "", "", err
	}
	plain, err := aead.Open(nil, artifact.Sealed[:aead.NonceSize()], artifact.Sealed[aead.NonceSize():], aad)
	if err != nil {
		return nil, "", "", ErrInvalid
	}
	digest := sha256.Sum256(plain)
	current, err := a.Task(ctx, credential, task, callID)
	if err != nil || current.Status != "SETTLED" || current.EvidenceConflict || current.Digest != row.Digest || current.Intent != row.Intent ||
		current.NativeQuoteJSON != row.NativeQuoteJSON || current.Charged != row.Charged || artifact.SHA256 != hex.EncodeToString(digest[:]) {
		return nil, "", "", ErrInvalid
	}
	return plain, artifact.ContentType, artifact.SHA256, nil
}
