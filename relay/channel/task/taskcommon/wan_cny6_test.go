package taskcommon

import (
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWanCashEvidenceBindsProviderResolutionAndDuration(t *testing.T) {
	p := metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan27_t2v_cny6_v1", Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "600000", Denominator: "1"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1000000", Denominator: "1"}},
	}}
	body := `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://media.example/v.mp4"},"usage":{"duration":5,"input_video_duration":0,"output_video_duration":5,"video_count":1,"SR":720,"ratio":"16:9"}}`
	u := VideoNativeUsage(p, []byte(body), "task-1", "video_720p")
	require.Equal(t, "confirmed", u.State)
	require.Len(t, u.Measurements, 1)
	assert.Equal(t, "video_720p", u.Measurements[0].Key)
	got, err := metering.Calculate(p, u)
	require.NoError(t, err)
	assert.Equal(t, "3000000", got.RatString())
	for _, tc := range []struct{ name, body, id, tier string }{
		{"wrong tier", body, "task-1", "video_1080p"},
		{"wrong id", body, "other", "video_720p"},
		{"missing SR", `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://media.example/v.mp4"},"usage":{"duration":5,"video_count":1}}`, "task-1", "video_720p"},
		{"conflicting duration", `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://media.example/v.mp4"},"usage":{"duration":5,"input_video_duration":0,"output_video_duration":6,"video_count":1,"SR":720,"ratio":"16:9"}}`, "task-1", "video_720p"},
		{"nonzero input", `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://media.example/v.mp4"},"usage":{"duration":5,"input_video_duration":1,"output_video_duration":5,"video_count":1,"SR":720,"ratio":"16:9"}}`, "task-1", "video_720p"},
		{"unknown usage", `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://media.example/v.mp4"},"usage":{"duration":5,"input_video_duration":0,"output_video_duration":5,"video_count":1,"SR":720,"ratio":"16:9","unexpected":1}}`, "task-1", "video_720p"},
		{"missing output duration", `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://media.example/v.mp4"},"usage":{"duration":5,"input_video_duration":0,"video_count":1,"SR":720,"ratio":"16:9"}}`, "task-1", "video_720p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEqual(t, "confirmed", VideoNativeUsage(p, []byte(tc.body), tc.id, tc.tier).State)
		})
	}
}

func TestWanFailureWithOutputEvidenceMustNotRelease(t *testing.T) {
	for _, policy := range []string{"ali_wan26_t2v_billable_duration_v1", "ali_wan26_t2v_cny6_v2", "ali_wan27_t2v_cny6_v1"} {
		for _, status := range []string{"FAILED", "CANCELED"} {
			for name, usage := range map[string]string{
				"output duration":     `{"output_video_duration":5}`,
				"input duration":      `{"input_video_duration":5}`,
				"resolution":          `{"SR":720}`,
				"ratio":               `{"ratio":"16:9"}`,
				"unknown measurement": `{"unexpected_units":5}`,
				"null usage":          `null`,
			} {
				t.Run(policy+status+name, func(t *testing.T) {
					body := `{"output":{"task_id":"task-1","task_status":"` + status + `"},"usage":` + usage + `}`
					u := VideoNativeUsage(metering.Price{Policy: policy}, []byte(body), "task-1", "video_720p")
					assert.NotEqual(t, "provider_confirmed_no_output", u.Source)
				})
			}
			body := `{"output":{"task_id":"task-1","task_status":"` + status + `"},"usage":{"duration":0,"output_video_duration":0,"video_count":0,"SR":0,"ratio":""}}`
			u := VideoNativeUsage(metering.Price{Policy: policy}, []byte(body), "task-1", "video_720p")
			assert.Equal(t, "provider_confirmed_no_output", u.Source)
		}
	}
}
