package platformbilling

import (
	"crypto/rand"
	"io"

	"github.com/QuantumNous/new-api/common"
)

// knowledgeSnapshotAAD binds ciphertext to the immutable request identity and
// certified producer policy. A database row cannot donate its body to another
// call, actor, payer or producer.
func knowledgeSnapshotAAD(row Call, policy KnowledgePolicy) ([]byte, error) {
	return common.Marshal([]any{"platform-knowledge-input-v1", row.CallID, row.ActorID, row.AccountID, row.OriginProductID, row.InstanceID, row.TaskID, row.Model, row.Digest, row.WireOperation, policy})
}

func (a *Adapter) sealKnowledgeSnapshot(row *Call) error {
	if a == nil || a.knowledgeResults == nil || row == nil {
		return ErrInvalid
	}
	var f frozenKnowledge
	if common.UnmarshalUnique([]byte(row.MediaSnapshot), &f) != nil || (f.Policy.Version != "knowledge_ali_text_v1" && row.WireOperation != "rerank.create") || len(f.Body) == 0 || len(f.BodyCiphertext) != 0 || f.BodyKeyID != "" {
		return ErrInvalid
	}
	aad, err := knowledgeSnapshotAAD(*row, f.Policy)
	if err != nil {
		return ErrInvalid
	}
	v := a.knowledgeResults
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return ErrPending
	}
	f.BodyCiphertext = v.aead.Seal(nonce, nonce, f.Body, aad)
	f.BodyKeyID = v.active
	f.Body = nil
	data, err := common.Marshal(f)
	if err != nil {
		return ErrInvalid
	}
	row.MediaSnapshot = string(data)
	return nil
}

func (a *Adapter) knowledgeSnapshot(row Call) (frozenKnowledge, error) {
	var f frozenKnowledge
	if common.UnmarshalUnique([]byte(row.MediaSnapshot), &f) != nil {
		return f, ErrInvalid
	}
	if len(f.BodyCiphertext) == 0 && f.BodyKeyID == "" {
		// Plaintext is accepted only for legacy/non-Ali callers and isolated
		// fixtures without the production vault; new Ali rows are always sealed.
		if (f.Policy.Version == "knowledge_ali_text_v1" || row.WireOperation == "rerank.create") && a != nil && a.knowledgeResults != nil {
			return frozenKnowledge{}, ErrInvalid
		}
		return f, nil
	}
	if a == nil || a.knowledgeResults == nil || len(f.Body) != 0 || !knowledgeKeyID.MatchString(f.BodyKeyID) {
		return frozenKnowledge{}, ErrInvalid
	}
	aead := a.knowledgeResults.keys[f.BodyKeyID]
	if aead == nil || len(f.BodyCiphertext) < aead.NonceSize() {
		return frozenKnowledge{}, ErrInvalid
	}
	aad, err := knowledgeSnapshotAAD(row, f.Policy)
	if err != nil {
		return frozenKnowledge{}, ErrInvalid
	}
	f.Body, err = aead.Open(nil, f.BodyCiphertext[:aead.NonceSize()], f.BodyCiphertext[aead.NonceSize():], aad)
	if err != nil || len(f.Body) == 0 {
		return frozenKnowledge{}, ErrInvalid
	}
	return f, nil
}
