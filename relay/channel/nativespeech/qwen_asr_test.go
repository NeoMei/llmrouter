package nativespeech

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func qwenDataURI() string {
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wavFixture())
}

func qwenASRBody(dataURI string) []byte {
	if dataURI == "" {
		dataURI = qwenDataURI()
	}
	req := QwenASRRequest{
		Model: "qwen3-asr-flash",
		Messages: []QwenASRMessage{{Role: "user", Content: []QwenASRContent{{
			Type:       "input_audio",
			InputAudio: &QwenASRInputAudio{Data: dataURI},
		}}}},
		ASROptions: &QwenASROptions{Language: ptr("zh"), EnableITN: ptr(false)},
	}
	b, e := common.Marshal(req)
	if e != nil {
		panic(e)
	}
	return b
}

func TestDecodeQwenASRRequest(t *testing.T) {
	req, err := DecodeQwenASRRequest(qwenASRBody(""))
	require.NoError(t, err)
	require.Equal(t, "qwen3-asr-flash", req.Model)
	require.NotNil(t, req.ASROptions)
	require.NotNil(t, req.ASROptions.EnableITN)
	require.False(t, *req.ASROptions.EnableITN)
	require.Equal(t, "zh", *req.ASROptions.Language)
	seconds, err := req.AudioSeconds()
	require.NoError(t, err)
	require.Greater(t, seconds, float64(0))

	withSystem := "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"system\",\"content\":[{\"type\":\"text\",\"text\":\"ctx\"}]},{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + qwenDataURI() + "\"}}]}]}"
	_, err = DecodeQwenASRRequest([]byte(withSystem))
	require.NoError(t, err)

	cases := map[string]string{
		"empty-model":     "{\"model\":\"\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + qwenDataURI() + "\"}}]}]}",
		"stream-true":     "{\"model\":\"qwen3-asr-flash\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + qwenDataURI() + "\"}}]}]}",
		"stream-options":  "{\"model\":\"qwen3-asr-flash\",\"stream_options\":{\"include_usage\":true},\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + qwenDataURI() + "\"}}]}]}",
		"remote-url":      "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"https://example.com/a.wav\"}}]}]}",
		"empty-audio":     "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"\"}}]}]}",
		"non-wav-mime":    "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"data:audio/mpeg;base64,AAAA\"}}]}]}",
		"bad-language":    "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + qwenDataURI() + "\"}}]}],\"asr_options\":{\"language\":\"xx\"}}",
		"no-user-message": "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"system\",\"content\":[{\"type\":\"text\",\"text\":\"x\"}]}]}",
		"two-user-parts":  "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + qwenDataURI() + "\"}},{\"type\":\"text\",\"text\":\"x\"}]}]}",
		"duplicate-key":   "{\"model\":\"qwen3-asr-flash\",\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"" + qwenDataURI() + "\"}}]}]}",
		"non-wav-bytes":   "{\"model\":\"qwen3-asr-flash\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"input_audio\":{\"data\":\"data:audio/wav;base64,AAAA\"}}]}]}",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeQwenASRRequest([]byte(body))
			require.Error(t, err)
		})
	}
}

func TestParseQwenASRResponse(t *testing.T) {
	ok := "{\"id\":\"chatcmpl-1\",\"object\":\"chat.completion\",\"model\":\"qwen3-asr-flash\",\"created\":1,\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"你好\",\"annotations\":[{\"type\":\"audio_info\",\"language\":\"zh\",\"emotion\":\"neutral\"}]}}],\"usage\":{\"seconds\":1,\"total_tokens\":54}}"
	out, err := ParseQwenASRResponse([]byte(ok))
	require.NoError(t, err)
	assert.True(t, out.Completed)
	assert.Equal(t, "你好", out.Transcript)
	assert.Equal(t, "confirmed", out.UsageState)
	assert.Equal(t, "1", out.Seconds.Value)
	assert.Equal(t, 0, out.Seconds.Scale)
	assert.Len(t, out.Annotations, 1)
	assert.Equal(t, "audio_info", out.Annotations[0].Type)
	assert.Equal(t, "zh", out.Annotations[0].Language)
	assert.Equal(t, "neutral", out.Annotations[0].Emotion)
	assert.Equal(t, "chatcmpl-1", out.RequestID)
	assert.NotEmpty(t, out.Raw)

	decimal := "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"x\"}}],\"usage\":{\"seconds\":1.5}}"
	out, err = ParseQwenASRResponse([]byte(decimal))
	require.NoError(t, err)
	assert.Equal(t, "15", out.Seconds.Value)
	assert.Equal(t, 1, out.Seconds.Scale)

	cases := map[string]string{
		"empty-content":    "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"\"}}],\"usage\":{\"seconds\":1}}",
		"missing-usage":    "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"x\"}}]}",
		"zero-seconds":     "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"x\"}}],\"usage\":{\"seconds\":0}}",
		"invalid-seconds":  "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"x\"}}],\"usage\":{\"seconds\":\"abc\"}}",
		"length-truncated": "{\"choices\":[{\"index\":0,\"finish_reason\":\"length\",\"message\":{\"role\":\"assistant\",\"content\":\"x\"}}],\"usage\":{\"seconds\":1}}",
		"wrong-role":       "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"user\",\"content\":\"x\"}}],\"usage\":{\"seconds\":1}}",
		"two-choices":      "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"x\"}},{\"index\":1,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"y\"}}],\"usage\":{\"seconds\":1}}",
		"duplicate-key":    "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"x\",\"content\":\"y\"}}],\"usage\":{\"seconds\":1}}",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseQwenASRResponse([]byte(body))
			require.Error(t, err)
		})
	}
}

func TestParseQwenSeconds(t *testing.T) {
	ok := map[string]string{"1": "1|0", "1.5": "15|1", "0.25": "25|2", "999999.000001": "999999000001|6"}
	for in, want := range ok {
		q, err := parseQwenSeconds(in)
		require.NoError(t, err, in)
		assert.Equal(t, strings.Split(want, "|")[0], q.Value, in)
		assert.Equal(t, fmt.Sprint(q.Scale), strings.Split(want, "|")[1], in)
	}
	for _, in := range []string{"0", "0.0", "-1", "1e3", "abc", "", "1.2345678", "1000000"} {
		_, err := parseQwenSeconds(in)
		require.Error(t, err, in)
	}
}

func TestSendQwenASR(t *testing.T) {
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/compatible-mode/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"你好\"}}],\"usage\":{\"seconds\":2}}"))
	}))
	defer supplier.Close()
	p, err := NewQwenASRProducerWithTransport("https://dashscope.aliyuncs.com", "fixture-key", rewriteTransport(supplier.URL, http.DefaultTransport))
	require.NoError(t, err)
	req, err := DecodeQwenASRRequest(qwenASRBody(""))
	require.NoError(t, err)
	result, err := p.SendQwenASR(t.Context(), req)
	require.NoError(t, err)
	assert.True(t, result.Completed)
	assert.Equal(t, "你好", result.Transcript)
	assert.Equal(t, "2", result.Seconds.Value)

	errorSupplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer errorSupplier.Close()
	pErr, err := NewQwenASRProducerWithTransport("https://dashscope.aliyuncs.com", "fixture-key", rewriteTransport(errorSupplier.URL, http.DefaultTransport))
	require.NoError(t, err)
	_, err = pErr.SendQwenASR(t.Context(), req)
	require.Error(t, err)
}

type qwenRewriteTransport struct {
	target string
	base   http.RoundTripper
}

func (r qwenRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(r.target)
	req = req.Clone(req.Context())
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	req.Host = u.Host
	return r.base.RoundTrip(req)
}

func rewriteTransport(target string, base http.RoundTripper) http.RoundTripper {
	return qwenRewriteTransport{target: target, base: base}
}

func TestNewQwenASRProducer(t *testing.T) {
	p, err := NewQwenASRProducer("https://dashscope.aliyuncs.com", "key")
	require.NoError(t, err)
	require.NotNil(t, p)

	for name, args := range map[string][2]string{
		"bad-scheme":   {"http://dashscope.aliyuncs.com", "key"},
		"with-path":    {"https://dashscope.aliyuncs.com/v1", "key"},
		"with-query":   {"https://dashscope.aliyuncs.com?a=1", "key"},
		"empty-cred":   {"https://dashscope.aliyuncs.com", ""},
		"cred-newline": {"https://dashscope.aliyuncs.com", "key\r\n"},
		"cred-space":   {"https://dashscope.aliyuncs.com", " "},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewQwenASRProducer(args[0], args[1])
			require.Error(t, err)
		})
	}

	_, err = NewQwenASRProducerWithTransport("https://dashscope.aliyuncs.com", "key", nil)
	require.Error(t, err)
	withTransport, err := NewQwenASRProducerWithTransport("https://dashscope.aliyuncs.com", "key", http.DefaultTransport)
	require.NoError(t, err)
	require.NotNil(t, withTransport)
	require.NotNil(t, withTransport.client.Transport)
}
