package platformbilling

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestConfigFromEnvAcceptsDeployedAliTextEmbeddingPolicy(t *testing.T) {
	policy, err := common.Marshal(map[string]any{
		"origin":             "https://dashscope.aliyuncs.com",
		"channel_key_sha256": "2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683",
		"version":            "knowledge_v1",
		"provider_reference": "https://fixture.example/knowledge",
		"public_model":       "text-embedding-v4",
		"wire_operation":     "embeddings.create",
		"channel_id":         102,
		"channel_type":       constant.ChannelTypeAli,
		"upstream_model":     "text-embedding-v4",
		"hard_input_tokens":  4096,
		"visual_tokens":      0,
		"max_item_tokens":    4096,
		"max_batch":          1,
		"embedding_space":    map[string]any{"provider": "aliyun", "model": "text-embedding-v4", "version": "v4", "dimensions": 1024, "input_semantics": "text/query-document-v1"},
		"rate_ids":           map[string]string{"CNY/6": "knowledge-text-embedding-v4-input-cny-v1"},
	})
	require.NoError(t, err)
	env := map[string]string{
		"UNIHUB_PLATFORM_ENABLED":     "true",
		"UNIHUB_ROUTING_PRINCIPAL_ID": "14",
		"UNIHUB_MAX_INPUT_TOKENS":     "65536",
		"UNIHUB_MAX_OUTPUT_TOKENS":    "4096",
		"UNIHUB_MAX_BODY_BYTES":       "32768",
		"UNIHUB_KNOWLEDGE_POLICIES":   `{"embeddings.create":{"text-embedding-v4":` + string(policy) + `}}`,
	}
	_, err = ConfigFromEnv(func(key string) string { return env[key] })
	require.NoError(t, err)
}
