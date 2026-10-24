package dto

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMessageMediaFidelity(t *testing.T) {
	var message Message
	require.NoError(t, common.Unmarshal([]byte(`{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.test/v","fps":0}},{"type":"input_audio","input_audio":{"data":"https://example.test/a"}},{"type":"image_url","image_url":{"url":"https://example.test/i","detail":"low"}},{"type":"text","text":"what happened?"}]}`), &message))
	parts := message.ParseContent()
	require.Len(t, parts, 4)
	raw, err := common.Marshal(parts)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"fps":0`)
	assert.Contains(t, string(raw), `"data":"https://example.test/a"`)
	assert.Contains(t, string(raw), `"detail":"low"`)
	assert.Contains(t, string(raw), `what happened?`)
}
