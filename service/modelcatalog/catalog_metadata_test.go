package modelcatalog

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCatalogPreservesExplicitSchemaAndOutputLimits(t *testing.T) {
	m, e := NormalizeModelCapabilities(`{"strict_schema":false,"max_output_tokens":4096}`)
	require.NoError(t, e)
	require.NotNil(t, m.StrictSchema)
	assert.False(t, *m.StrictSchema)
	require.NotNil(t, m.MaxOutputTokens)
	assert.Equal(t, 4096, *m.MaxOutputTokens)
	copy := cloneCapabilities(m)
	*copy.StrictSchema = true
	*copy.MaxOutputTokens = 1
	assert.False(t, *m.StrictSchema)
	assert.Equal(t, 4096, *m.MaxOutputTokens)
	absent, e := NormalizeModelCapabilities(`{}`)
	require.NoError(t, e)
	assert.Nil(t, absent.StrictSchema)
	assert.Nil(t, absent.MaxOutputTokens)
	for _, raw := range []string{`{"max_output_tokens":0}`, `{"max_output_tokens":-1}`, `{"max_output_tokens":2147483647}`} {
		_, e = NormalizeModelCapabilities(raw)
		assert.Error(t, e)
	}
}
