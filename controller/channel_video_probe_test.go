package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/task/ali"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVideoTaskStateParsesWanStatus(t *testing.T) {
	for _, state := range []string{"PENDING", "RUNNING", "SUCCEED", "FAILED", "CANCELED", "UNKNOWN"} {
		raw := []byte(`{"output":{"task_id":"t","task_status":"` + state + `"}}`)
		assert.Equal(t, lowerTrim(state), videoTaskState(raw))
	}
	assert.Equal(t, "", videoTaskState([]byte(`not json`)))
}

func TestVideoProbePollsWithTheChannelTaskWire(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/tasks/fixture-task" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if calls.Load() < 2 {
			_, _ = w.Write([]byte(`{"output":{"task_status":"RUNNING"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"output":{"task_status":"SUCCEED"}}`))
	}))
	t.Cleanup(server.Close)

	adaptor := &ali.TaskAdaptor{}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	info.ChannelType = constant.ChannelTypeAli
	info.ChannelBaseUrl = server.URL
	info.ApiKey = "fixture-key"
	info.UpstreamModelName = "wan2.6-t2v"
	adaptor.Init(info)
	state, err := videoProbeTaskState(context.Background(), adaptor, server.URL, "fixture-key", "fixture-task")
	require.NoError(t, err)
	assert.Equal(t, "succeed", state)
	assert.Equal(t, int32(2), calls.Load())
}
