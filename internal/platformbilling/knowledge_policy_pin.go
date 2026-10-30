package platformbilling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/knowledgebinding"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ManagedBinding struct {
	PolicyDigest    string              `json:"policy_digest"`
	RouteGeneration string              `json:"route_generation"`
	RouteID         int                 `json:"route_id"`
	TargetID        int                 `json:"target_id"`
	ChannelID       int                 `json:"channel_id"`
	InstanceID      string              `json:"instance_id"`
	OriginProductID string              `json:"origin_product_id"`
	Model           string              `json:"model"`
	WireOperation   string              `json:"wire_operation"`
	EmbeddingSpace  *dto.EmbeddingSpace `json:"embedding_space,omitempty"`
}

func ApprovedKnowledgePair(name, wire string) bool {
	return name == "unihub-text-embedding-v4" && wire == "embeddings.create" || name == "qwen3-rerank" && wire == "rerank.create"
}
func opaqueKnowledgeID(value string) bool {
	b, e := hex.DecodeString(value)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == value
}
func cloneKnowledgePolicies(source map[string]map[string]KnowledgePolicy) map[string]map[string]KnowledgePolicy {
	if source == nil {
		return nil
	}
	raw, _ := common.Marshal(source)
	var out map[string]map[string]KnowledgePolicy
	_ = common.Unmarshal(raw, &out)
	return out
}

func (a *Adapter) ResolveManagedBinding(tx *gorm.DB, product, name, wire, expected string) (ManagedBinding, error) {
	if a == nil || tx == nil || a.bindingResolver == nil {
		return ManagedBinding{}, ErrPending
	}
	b, fingerprint, err := a.bindingResolver(tx, product, name, wire)
	if err != nil {
		return b, err
	}
	var active knowledgebinding.KnowledgeActiveBinding
	if err = knowledgePinLockedRead(tx).Where("instance_id = ? AND origin_product_id = ? AND model = ? AND wire_operation = ?", b.InstanceID, product, name, wire).First(&active).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return b, ErrConflict
		}
		return b, ErrPending
	}
	if !opaqueKnowledgeID(active.ActivePin) || (expected != "" && expected != active.ActivePin) || active.RouteID != b.RouteID || active.RouteGeneration != b.RouteGeneration {
		return b, ErrConflict
	}
	var pin knowledgebinding.KnowledgeRoutePin
	if err = knowledgePinLockedRead(tx).First(&pin, "pin = ?", active.ActivePin).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return b, ErrConflict
		}
		return b, ErrPending
	}
	if pin.Fingerprint != fingerprint || pin.InstanceID != b.InstanceID || pin.OriginProductID != product || pin.Model != name || pin.WireOperation != wire || pin.RouteID != b.RouteID || pin.TargetID != b.TargetID || pin.ChannelID != b.ChannelID || pin.RouteGeneration != b.RouteGeneration {
		return b, ErrConflict
	}
	b.PolicyDigest = pin.Pin
	return b, nil
}

// ActivateManagedKnowledge is an explicit in-process operator entry, never an
// HTTP route. The operator must activate all required origin products together.
// Every activation rotates route generation and retires every prior pointer;
// retained history has a hard cap of 64 per binding, with no GC or reuse.
func (a *Adapter) ActivateManagedKnowledge(ctx context.Context, name, wire string, products []string) ([]ManagedBinding, error) {
	if a == nil || !a.Enabled() || a.bindingResolver == nil || a.bindingRotator == nil || !ApprovedKnowledgePair(name, wire) || len(products) < 1 || len(products) > 2 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range products {
		if seen[p] || (p != "llmrouter" && p != "superwagie") {
			return nil, ErrInvalid
		}
		seen[p] = true
	}
	var out []ManagedBinding
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := a.bindingRotator(tx, name); err != nil {
			return err
		}
		for _, product := range products {
			b, fingerprint, err := a.bindingResolver(tx, product, name, wire)
			if err != nil {
				return err
			}
			var count int64
			if err = tx.Model(&knowledgebinding.KnowledgeRoutePin{}).Where("instance_id = ? AND origin_product_id = ? AND model = ? AND wire_operation = ?", b.InstanceID, product, name, wire).Count(&count).Error; err != nil {
				return err
			}
			if count >= 64 {
				return ErrPending
			}
			pin, err := newKnowledgeID()
			if err != nil {
				return err
			}
			b.PolicyDigest = pin
			history := knowledgebinding.KnowledgeRoutePin{Pin: pin, InstanceID: b.InstanceID, OriginProductID: product, Model: name, WireOperation: wire, RouteID: b.RouteID, TargetID: b.TargetID, ChannelID: b.ChannelID, RouteGeneration: b.RouteGeneration, Fingerprint: fingerprint}
			if err = tx.Create(&history).Error; err != nil {
				return err
			}
			active := knowledgebinding.KnowledgeActiveBinding{InstanceID: b.InstanceID, OriginProductID: product, Model: name, WireOperation: wire, RouteID: b.RouteID, RouteGeneration: b.RouteGeneration, ActivePin: pin}
			if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "instance_id"}, {Name: "origin_product_id"}, {Name: "model"}, {Name: "wire_operation"}}, DoUpdates: clause.AssignmentColumns([]string{"route_id", "route_generation", "active_pin"})}).Create(&active).Error; err != nil {
				return err
			}
			out = append(out, b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func newKnowledgeID() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func ValidKnowledgeID(s string) bool           { return opaqueKnowledgeID(s) }
func (a *Adapter) KnowledgeInstanceID() string { return a.config.InstanceID }
func (a *Adapter) ManagedKnowledgePolicy(name, wire string) (KnowledgePolicy, error) {
	p, ok := a.config.KnowledgePolicies[wire][name]
	if !ok || !p.valid(a.config, name, wire) || (wire == "rerank.create" && !a.config.AllowAliRerank) {
		return p, ErrInvalid
	}
	raw, err := common.Marshal(p)
	if err != nil {
		return KnowledgePolicy{}, ErrInvalid
	}
	var copied KnowledgePolicy
	if common.Unmarshal(raw, &copied) != nil {
		return KnowledgePolicy{}, ErrInvalid
	}
	return copied, nil
}
func (a *Adapter) SetKnowledgeBindingResolver(resolve func(*gorm.DB, string, string, string) (ManagedBinding, string, error), rotate func(*gorm.DB, string) error) {
	a.bindingResolver = resolve
	a.bindingRotator = rotate
}

func knowledgePinLockedRead(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "sqlite" {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}
