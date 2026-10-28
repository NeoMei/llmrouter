package taskcommon

import (
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestVideoNativeFinalEvidence(t *testing.T) {
	for _, tc := range []struct {
		policy, unit, namespace, body, state, value string
		scale                                       int
	}{
		{"sora_completed_seconds_v1", "video_second", "", `{"status":"completed","seconds":"5","seconds":"0"}`, "invalid", "", 0},
		{"sora_completed_seconds_v1", "video_second", "", `{"status":"completed","seconds":"5"}`, "confirmed", "5", 0},
		{"sora_completed_seconds_v1", "video_second", "", `{"status":"queued","seconds":"10"}`, "missing", "", 0},
		{"sora_completed_seconds_v1", "video_second", "", `{"status":"completed"}`, "missing", "", 0},
		{"sora_completed_seconds_v1", "video_second", "", `{"status":"completed","seconds":"0"}`, "confirmed", "0", 0},
		{"kling_final_units_v1", "provider_unit", "kling.final_unit_deduction", `{"data":{"task_status":"succeed","final_unit_deduction":"0.000001"}}`, "confirmed", "1", 6},
		{"kling_final_units_v1", "provider_unit", "kling.final_unit_deduction", `{"data":{"task_status":"succeed","final_unit_deduction":"-1"}}`, "invalid", "", 0},
		{"doubao_completion_tokens_v1", "provider_unit", "doubao.completion_tokens", `{"status":"succeeded","usage":{"completion_tokens":0}}`, "confirmed", "0", 0},
		{"doubao_completion_tokens_v1", "provider_unit", "doubao.completion_tokens", `{"status":"succeeded","usage":{"total_tokens":10}}`, "missing", "", 0},
		{"ali_video_duration_v1", "video_second", "", `{"output":{"task_status":"SUCCEEDED"},"usage":{"video_duration":5.125}}`, "confirmed", "5125", 3},
		{"veo_output_probe_v1", "video_second", "", `{"done":true,"durationSeconds":10}`, "missing", "", 0},
	} {
		t.Run(tc.policy+tc.body, func(t *testing.T) {
			key := "video"
			if tc.unit == "provider_unit" {
				key = "provider"
			}
			p := metering.Price{Version: "metered_v2", Operation: "video", Policy: tc.policy, Terms: []metering.Term{{Key: key, Unit: tc.unit, ProviderUnit: tc.namespace, Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
			u := VideoNativeUsage(p, []byte(tc.body), "task-1", "")
			assert.Equal(t, tc.state, u.State)
			if tc.state == "confirmed" {
				require.NoError(t, metering.ValidateUsage(p, u))
				require.Len(t, u.Measurements, 1)
				assert.Equal(t, metering.Quantity{Value: tc.value, Scale: tc.scale}, u.Measurements[0].Quantity)
			}
		})
	}
}

func TestWan26NativeEvidenceRequiresSelectedResolutionAndCompleteReceipt(t *testing.T) {
	p := metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "2", Denominator: "1"}},
	}}
	complete := `{"output":{"task_status":"SUCCEEDED","video_url":"https://media.example/video.mp4"},"usage":{"duration":5,"video_count":1}}`
	u := VideoNativeUsage(p, []byte(complete), "task-1", "video_720p")
	assert.Equal(t, "confirmed", u.State)
	require.NoError(t, metering.ValidateUsage(p, u))
	require.Len(t, u.Measurements, 1)
	assert.Equal(t, metering.Quantity{Value: "5"}, u.Measurements[0].Quantity)

	for name, body := range map[string]string{
		"missing_count":      `{"output":{"task_status":"SUCCEEDED","video_url":"https://media.example/video.mp4"},"usage":{"duration":5}}`,
		"plural_count":       `{"output":{"task_status":"SUCCEEDED","video_url":"https://media.example/video.mp4"},"usage":{"duration":5,"video_count":2}}`,
		"string_count":       `{"output":{"task_status":"SUCCEEDED","video_url":"https://media.example/video.mp4"},"usage":{"duration":5,"video_count":"1"}}`,
		"canceled_not_empty": `{"output":{"task_status":"CANCELED"},"usage":{"video_count":1}}`,
		"missing_resolution": complete,
		"unknown_resolution": complete,
		"conflicting_output": `{"output":{"task_status":"SUCCEEDED","video_url":"one","results":[{},{"url":"two"}]},"usage":{"duration":5,"video_count":1}}`,
	} {
		resolution := "video_720p"
		if name == "missing_resolution" {
			resolution = ""
		}
		if name == "unknown_resolution" {
			resolution = "video_480p"
		}
		assert.NotEqual(t, "confirmed", VideoNativeUsage(p, []byte(body), "task-1", resolution).State, name)
	}
	cancelBody := `{"output":{"task_status":"CANCELED","results":[]},"usage":{"video_count":0}}`
	canceled := VideoNativeUsage(p, []byte(cancelBody), "task-1", "video_720p")
	assert.Equal(t, "incomplete", canceled.State)
	assert.Equal(t, "provider_confirmed_no_output", canceled.Source)
}

func TestVideoNativeNoOutputRequiresExplicitEmptyCollection(t *testing.T) {
	p := metering.Price{Policy: "sora_completed_seconds_v1"}
	assert.NotEqual(t, "provider_confirmed_no_output", VideoNativeUsage(p, []byte(`{"status":"failed"}`), "id", "").Source)
	assert.Equal(t, "provider_confirmed_no_output", VideoNativeUsage(p, []byte(`{"status":"failed","videos":[]}`), "id", "").Source)
	assert.NotEqual(t, "provider_confirmed_no_output", VideoNativeUsage(p, []byte(`{"status":"failed","videos":[{"id":"output"}]}`), "id", "").Source)
}
