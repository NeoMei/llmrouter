package minimax

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http/httptest"
	"testing"
)

func TestNativeTTSRequestContract(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{RelayMode: constant.RelayModeAudioSpeech, OriginModelName: "public-alias", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "speech-2.8-hd"}}
	request := dto.AudioRequest{Model: "speech-2.8-hd", Input: "hello", Voice: "owned-voice", ResponseFormat: "wav", Metadata: []byte(`{"voice_setting":{"pitch":0,"text_normalization":false}}`)}
	body, err := (&Adaptor{}).ConvertAudioRequest(c, info, request)
	require.NoError(t, err)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	var sent map[string]any
	require.NoError(t, common.Unmarshal(raw, &sent))
	assert.Equal(t, "speech-2.8-hd", sent["model"])
	assert.Equal(t, "hex", sent["output_format"])
	voice := sent["voice_setting"].(map[string]any)
	assert.Equal(t, float64(0), voice["pitch"])
	assert.Equal(t, false, voice["text_normalization"])
	request.Metadata = []byte(`{"model":"unpriced","text":"replacement"}`)
	_, err = (&Adaptor{}).ConvertAudioRequest(c, info, request)
	require.Error(t, err)
}
