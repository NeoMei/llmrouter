package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audioProbeFixture answers both speech and transcription wires and records the
// path and body it received.
func audioProbeFixture(t *testing.T, paths *atomic.Value, bodies *atomic.Value) *httptest.Server {
	t.Helper()
	var lastPath atomic.Value
	var lastBody atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		lastPath.Store(r.URL.Path)
		lastBody.Store(string(buf[:n]))
		if paths != nil {
			paths.Store(r.URL.Path)
		}
		if bodies != nil {
			bodies.Store(string(buf[:n]))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/audio/speech" {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("ID3fake-mp3"))
			return
		}
		_, _ = w.Write([]byte(`{"text":"","usage":{"seconds":1}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func setupAudioChannel(t *testing.T, baseURL, modelName string) (*model.User, *model.Channel) {
	t.Helper()
	oldRatios := ratio_setting.ModelRatio2JSONString()
	oldPrices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"`+modelName+`":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"`+modelName+`":0.001}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
	})
	db := setupModelListControllerTestDB(t)
	common.MemoryCacheEnabled = false
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	user := &model.User{Username: "audio-probe-user", Group: "default", Status: common.UserStatusEnabled, Quota: 1000000}
	require.NoError(t, db.Create(user).Error)
	service.InitHttpClient()
	channel := &model.Channel{
		Type: constant.ChannelTypeAli, Name: "audio probe", Key: "fixture-key", Models: modelName, Group: "default",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1),
	}
	require.NoError(t, db.Create(channel).Error)
	return user, channel
}

func TestChannelTestProbesSpeechWithVoice(t *testing.T) {
	var path, body atomic.Value
	server := audioProbeFixture(t, &path, &body)
	user, channel := setupAudioChannel(t, server.URL, "qwen3-tts-flash")

	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.NoError(t, result.localErr)
	assert.Equal(t, "/compatible-mode/v1/audio/speech", path.Load())
	assert.Contains(t, body.Load(), "Cherry", "speech probes must carry a voice")
}

func TestChannelTestProbesTranscriptionWithWAV(t *testing.T) {
	var path, body atomic.Value
	server := audioProbeFixture(t, &path, &body)
	user, channel := setupAudioChannel(t, server.URL, "qwen3-asr-flash")

	// Aliyun Flash ASR refuses a real call without a validated inline carrier, so
	// the probe must stop before the supplier and say why.
	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.ErrorContains(t, result.localErr, "unvalidated Flash ASR request")
	assert.Nil(t, path.Load(), "no supplier request may be sent without validated audio")
}
