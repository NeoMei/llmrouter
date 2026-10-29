package platformbilling

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAliKnowledgeConfigurationRequiresValidVersionedKeyring(t *testing.T) {
	base := map[string]string{"UNIHUB_PLATFORM_ENABLED": "true", "UNIHUB_ROUTING_PRINCIPAL_ID": "1", "UNIHUB_MAX_INPUT_TOKENS": "1000", "UNIHUB_MAX_OUTPUT_TOKENS": "100", "UNIHUB_MAX_BODY_BYTES": "8192"}
	read := func(k string) string { return base[k] }
	c, err := ConfigFromEnv(read)
	require.NoError(t, err)
	assert.False(t, c.AllowAliEmbeddingV4)
	assert.False(t, c.AllowAliRerank)
	assert.False(t, c.EnableKnowledgeResultRead)
	base["UNIHUB_KNOWLEDGE_RESULT_READ_ENABLED"] = "true"
	_, err = ConfigFromEnv(read)
	assert.Error(t, err)
	base["UNIHUB_KNOWLEDGE_RESULT_READ_ENABLED"] = "false"
	base["UNIHUB_ALI_EMBEDDING_ENABLED"] = "true"
	_, err = ConfigFromEnv(read)
	assert.Error(t, err)
	old := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	next := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	base["UNIHUB_KNOWLEDGE_RESULT_KEYS"] = `{"v1":"` + old + `","v2":"` + next + `"}`
	base["UNIHUB_KNOWLEDGE_RESULT_ACTIVE_KEY"] = "v2"
	c, err = ConfigFromEnv(read)
	require.NoError(t, err)
	assert.True(t, c.AllowAliEmbeddingV4)
	assert.Equal(t, "v2", c.AliEmbeddingResultActiveKey)
	assert.Equal(t, []byte(strings.Repeat("a", 32)), c.AliEmbeddingResultKeys["v1"])
	serialized, err := common.Marshal(c)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), old)
	assert.NotContains(t, string(serialized), next)
	for _, bad := range []string{`{"v1":"bad"}`, `{"v1":"` + old + `","v1":"` + next + `"}`, `{"v1":"` + old + `","v2":"` + old + `"}`} {
		base["UNIHUB_KNOWLEDGE_RESULT_KEYS"] = bad
		_, err = ConfigFromEnv(read)
		assert.Error(t, err)
	}
	base["UNIHUB_ALI_EMBEDDING_ENABLED"] = "false"
	base["UNIHUB_ALI_RERANK_ENABLED"] = "true"
	delete(base, "UNIHUB_KNOWLEDGE_RESULT_KEYS")
	_, err = ConfigFromEnv(read)
	assert.Error(t, err)
	base["UNIHUB_KNOWLEDGE_RESULT_KEYS"] = `{"v1":"` + old + `","v2":"` + next + `"}`
	c, err = ConfigFromEnv(read)
	require.NoError(t, err)
	assert.False(t, c.AllowAliEmbeddingV4)
	assert.True(t, c.AllowAliRerank)
	base["UNIHUB_KNOWLEDGE_RESULT_READ_ENABLED"] = "true"
	c, err = ConfigFromEnv(read)
	require.NoError(t, err)
	assert.True(t, c.EnableKnowledgeResultRead)
}
