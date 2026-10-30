package router

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWanImageSubmissionRequiresBoundPendingTask(t *testing.T) {
	id, err := wanImageSubmission([]byte(`{"request_id":"r","output":{"task_id":"task-1","task_status":"PENDING"}}`))
	require.NoError(t, err)
	assert.Equal(t, "task-1", id)
	for _, raw := range []string{
		`{"output":{"task_id":"../../secret","task_status":"PENDING"}}`,
		`{"output":{"task_id":"task-1","task_status":"SUCCEEDED"}}`,
		`{"output":{"task_id":"task-1","task_id":"other","task_status":"PENDING"}}`,
		`{"output":{"task_id":"task-1","task_status":"PENDING","url":"https://elsewhere"}}`,
	} {
		_, err := wanImageSubmission([]byte(raw))
		require.Error(t, err)
	}
}

func TestWanImageFailureRequiresPositiveNoOutputEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		accepted  bool
	}{
		{"confirmed empty", `{"output":{"task_id":"task","task_status":"FAILED","task_metrics":{"TOTAL":1,"SUCCEEDED":0,"FAILED":1},"results":[]},"usage":{"image_count":0}}`, true},
		{"missing metrics", `{"output":{"task_id":"task","task_status":"FAILED"}}`, false},
		{"partial output", `{"output":{"task_id":"task","task_status":"FAILED","task_metrics":{"TOTAL":2,"SUCCEEDED":1,"FAILED":1}}}`, false},
		{"claimed images", `{"output":{"task_id":"task","task_status":"FAILED","task_metrics":{"TOTAL":1,"SUCCEEDED":0,"FAILED":1}},"usage":{"image_count":1}}`, false},
		{"output present", `{"output":{"task_id":"task","task_status":"FAILED","task_metrics":{"TOTAL":1,"SUCCEEDED":0,"FAILED":1},"results":[{"url":"https://x"}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var envelope map[string]any
			require.NoError(t, common.UnmarshalUnique([]byte(tc.raw), &envelope))
			output := envelope["output"].(map[string]any)
			assert.Equal(t, tc.accepted, wanImageNoOutput(envelope, output, 1))
		})
	}
}
