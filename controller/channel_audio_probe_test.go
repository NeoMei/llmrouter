package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audioProbeFixture answers the DashScope synthesis wire, the generated clip and
// the transcription wire, recording the path and body it received.
func audioProbeFixture(t *testing.T, paths *atomic.Value, bodies *atomic.Value) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		if paths != nil {
			paths.Store(r.URL.Path)
		}
		if bodies != nil {
			bodies.Store(string(buf[:n]))
		}
		switch {
		case r.URL.Path == bailianSpeechPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":{"audio":{"url":"` + server.URL + `/probe-clip.mp3"}},"usage":{"characters":24}}`))
		case r.URL.Path == "/probe-clip.mp3":
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/audio/transcriptions") || strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"test"},"finish_reason":"stop"}],"usage":{"seconds":1,"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"audio":1,"text":0},"completion_tokens_details":{"text":1}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func setupAudioChannel(t *testing.T, baseURL, modelName string) (*model.User, *model.Channel) {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	common.MemoryCacheEnabled = false
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
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
	server := audioProbeFixture(t, nil, nil)
	user, channel := setupAudioChannel(t, server.URL, "qwen3-tts-flash")

	payload := bailianSpeechBody("qwen3-tts-flash", "Cherry")
	assert.Contains(t, string(payload), `"voice":"Cherry"`, "speech probes must carry a voice")
	assert.Contains(t, string(payload), `"text"`)

	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.NoError(t, result.localErr, "a synthesised clip with a reachable URL is a passing probe")
}

func TestChannelTestProbesTranscriptionWithWAV(t *testing.T) {
	var path, body atomic.Value
	server := audioProbeFixture(t, &path, &body)
	user, channel := setupAudioChannel(t, server.URL, "qwen3-asr-flash")

	result := testChannel(context.Background(), channel, user.Id, "", "", false)
	require.NoError(t, result.localErr)
	gotPath, _ := path.Load().(string)
	assert.True(t, strings.HasSuffix(gotPath, "/transcriptions"), "transcription probes use the upload wire, got %q", gotPath)
}
