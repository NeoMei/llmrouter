package platformbilling

import (
	"strings"

	"gorm.io/gorm"
)

// NewKnowledgeBindingOperatorAdapter constructs the minimal in-process adapter
// required by ActivateManagedKnowledge. It intentionally does not initialize
// the HTTP client, auto-migrations, vault, recovery workers, or provider state.
// The offline caller must verify all required schemas before calling it.
func NewKnowledgeBindingOperatorAdapter(db *gorm.DB, c Config) (*Adapter, error) {
	if db == nil || !c.Enabled || !stableID.MatchString(strings.TrimSpace(c.InstanceID)) || c.Group == "" || c.Group == "auto" || c.PrincipalID < 1 || c.MaxInputTokens < 1 || c.MaxInputTokens > 1<<20 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 32768 || c.MaxBodyBytes < 1 || c.MaxBodyBytes > 1<<20 || !c.AllowAliEmbeddingV4 || !c.AllowAliRerank || c.NativeHistory != nil {
		return nil, ErrInvalid
	}
	c.InstanceID = strings.TrimSpace(c.InstanceID)
	c.KnowledgePolicies = cloneKnowledgePolicies(c.KnowledgePolicies)
	for _, pair := range [][2]string{{"unihub-text-embedding-v4", "embeddings.create"}, {"qwen3-rerank", "rerank.create"}} {
		policy, ok := c.KnowledgePolicies[pair[1]][pair[0]]
		if !ok || !policy.valid(c, pair[0], pair[1]) {
			return nil, ErrInvalid
		}
	}
	return &Adapter{db: db, config: c}, nil
}
