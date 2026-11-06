package platformbilling

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeScopeCredentialDoesNotUseHistoricalModelSnapshot(t *testing.T) {
	var grant Claims
	require.NoError(t, common.Unmarshal([]byte(`{"id":"key","audience":"llmrouter","credential_kind":"api_key","actor_id":"actor","account_id":"team","origin_product_id":"superwagie","instance_id":"default","asset":"CNY/6","funding_mode":"prepaid_cash","model_scope":"llmrouter_available","pricing_source":"llmrouter_native","credential_version":2,"models":["old-model"]}`), &grant))
	a := &Adapter{config: Config{InstanceID: "default"}}
	assert.NoError(t, a.validateModelCredential("uh_key_test", "", "newly-enabled-model", grant))
}

func TestSelectedScopeCredentialKeepsExactModelBoundary(t *testing.T) {
	grant := Claims{ID: "key", Audience: "llmrouter", CredentialKind: "api_key", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", InstanceID: "default", Models: []string{"old-model"}}
	a := &Adapter{config: Config{InstanceID: "default"}}
	assert.Error(t, a.validateModelCredential("uh_key_test", "", "newly-enabled-model", grant))
	assert.NoError(t, a.validateModelCredential("uh_key_test", "", "old-model", grant))
}

func TestNativeScopeRequiresExplicitCompatibleGrant(t *testing.T) {
	valid := Claims{ID: "key", Audience: "llmrouter", CredentialKind: "api_key", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", InstanceID: "default", ModelScope: "llmrouter_available", PricingSource: "llmrouter_native", CredentialVersion: 2, Asset: "CNY/6", FundingMode: "prepaid_cash"}
	a := &Adapter{config: Config{InstanceID: "default"}}
	for _, mutate := range []func(*Claims){
		func(c *Claims) { c.PricingSource = "" }, func(c *Claims) { c.CredentialVersion = 0 }, func(c *Claims) { c.CredentialKind = "task" }, func(c *Claims) { c.OriginProductID = "other" }, func(c *Claims) { c.Asset = "CREDIT/6" }, func(c *Claims) { c.FundingMode = "credits" }, func(c *Claims) { c.ModelScope = "all" },
	} {
		grant := valid
		mutate(&grant)
		assert.Error(t, a.validateModelCredential("uh_key_test", "", "model", grant))
	}
}
