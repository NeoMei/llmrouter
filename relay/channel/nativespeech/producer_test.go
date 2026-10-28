package nativespeech

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }
func wavFixture() []byte {
	b := make([]byte, 244)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 236)
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 24000)
	binary.LittleEndian.PutUint32(b[28:], 48000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], 200)
	for i := 44; i < len(b); i += 2 {
		binary.LittleEndian.PutUint16(b[i:], uint16(int16(1000*math.Sin(float64(i)*0.1))))
	}
	return b
}
func fixtureProducer(t *testing.T, provider, model, origin string, resources ...string) *Producer {
	t.Helper()
	policy, err := PolicyForModel(provider, model)
	require.NoError(t, err)
	p, err := newProducer(Target{Policy: policy, Origin: origin, Credential: "fixture-secret", ResourceOrigins: resources, Limits: Limits{MaxAudioBytes: 1 << 20, MaxAudioSeconds: 10, TimeoutSeconds: 5}}, true)
	require.NoError(t, err)
	return p
}
func TestBailianNativeBodyAndZeroUsageWithRealPCM(t *testing.T) {
	audio := wavFixture()
	var posts, gets int
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		assert.Equal(t, "GET", r.Method)
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("Xi-Api-Key"))
		assert.Equal(t, "/tone.wav?signature=test", r.URL.String())
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(audio)
	}))
	defer resource.Close()
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/services/aigc/multimodal-generation/generation", r.URL.Path)
		assert.Equal(t, "Bearer fixture-secret", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"model":"qwen3-tts-instruct-flash-2026-01-26","input":{"text":"你好🙂","voice":"Cherry","language_type":"Chinese","instructions":"Warm voice","optimize_instructions":false}}`, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"request_id":"req-123","output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":0,"input_tokens":0,"output_tokens":0}}`, resource.URL+"/tone.wav?signature=test")
	}))
	defer supplier.Close()
	p := fixtureProducer(t, "bailian", "qwen3-tts-instruct-flash-2026-01-26", supplier.URL, resource.URL)
	out, err := p.Send(context.Background(), Request{Text: "你好🙂", Voice: "Cherry", Language: ptr("Chinese"), Instructions: ptr("Warm voice"), OptimizeInstructions: ptr(false)})
	require.NoError(t, err)
	assert.True(t, out.Completed)
	assert.Equal(t, audio, out.Audio)
	assert.Equal(t, sha256.Sum256(audio), out.SHA256)
	assert.Equal(t, "audio/wav", out.ContentType)
	assert.Equal(t, "confirmed", out.Usage.State)
	require.NotNil(t, out.Usage.Characters)
	assert.Equal(t, int64(0), *out.Usage.Characters)
	assert.Equal(t, "req-123", out.RequestID)
	assert.Equal(t, 1, posts)
	assert.Equal(t, 1, gets)
}
func TestElevenNativeBinaryAndUncoveredExactCost(t *testing.T) {
	audio := mp3Fixture(t)
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/text-to-speech/Voice123?output_format=mp3_44100_128", r.URL.String())
		assert.Equal(t, "fixture-secret", r.Header.Get("Xi-Api-Key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"text":"Hi","model_id":"eleven_multilingual_v2","voice_settings":{"stability":0,"similarity_boost":0,"style":0,"speed":0.7,"use_speaker_boost":false}}`, string(raw))
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("character-cost", "0.50")
		w.Header().Set("request-id", "request-123")
		w.Header().Set("x-trace-id", "trace-123")
		_, _ = w.Write(audio)
	}))
	defer supplier.Close()
	p := fixtureProducer(t, "elevenlabs", "eleven_multilingual_v2", supplier.URL)
	out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "Voice123", VoiceSettings: &VoiceSettings{Stability: ptr(0.0), SimilarityBoost: ptr(0.0), Style: ptr(0.0), Speed: ptr(0.7), UseSpeakerBoost: ptr(false)}})
	require.NoError(t, err)
	assert.True(t, out.Completed)
	assert.Equal(t, audio, out.Audio)
	assert.Equal(t, sha256.Sum256(audio), out.SHA256)
	assert.Equal(t, "uncovered", out.Usage.State)
	assert.Equal(t, "0.50", out.Usage.RawQuantity)
	assert.Nil(t, out.Usage.Characters)
	assert.Equal(t, "trace-123", out.TraceID)
}
func TestElevenUsageDistinguishesMissingZeroMalformedAndDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		values     []string
		state, raw string
	}{{"missing", nil, "missing", ""}, {"zero", []string{"0"}, "uncovered", "0"}, {"decimal-zero", []string{"0.00"}, "uncovered", "0.00"}, {"fraction", []string{"1.25"}, "uncovered", "1.25"}, {"integer", []string{"20"}, "uncovered", "20"}, {"negative", []string{"-1"}, "invalid", ""}, {"exponent", []string{"1e3"}, "invalid", ""}, {"nan", []string{"NaN"}, "invalid", ""}, {"joined", []string{"0, 0"}, "conflict", ""}, {"duplicate", []string{"0", "0"}, "conflict", ""}, {"conflict", []string{"0", "1"}, "conflict", ""}, {"oversize", []string{strings.Repeat("9", 100)}, "invalid", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, v := range tc.values {
					w.Header().Add("character-cost", v)
				}
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = w.Write(mp3Fixture(t))
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "elevenlabs", "eleven_multilingual_v2", supplier.URL)
			out, err := p.Send(context.Background(), Request{Text: "hello", Voice: "V"})
			require.NoError(t, err)
			assert.True(t, out.Completed)
			assert.Equal(t, tc.state, out.Usage.State)
			assert.Equal(t, tc.raw, out.Usage.RawQuantity)
			assert.Nil(t, out.Usage.Characters)
		})
	}
}
func TestBailianUsageAndIncompleteEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, usage, tail, finish, state string
		wantErr                          bool
	}{{"missing", "", "", "stop", "missing", false}, {"null", `,"usage":{"characters":null}`, "", "stop", "missing", false}, {"fraction", `,"usage":{"characters":0.5}`, "", "stop", "invalid", false}, {"negative", `,"usage":{"characters":-1}`, "", "stop", "invalid", false}, {"duplicate", `,"usage":{"characters":0,"characters":1}`, "", "stop", "conflict", true}, {"trailing", `,"usage":{"characters":0}`, `{}`, "stop", "invalid", true}, {"nonterminal", `,"usage":{"characters":0}`, "", "", "missing", true}, {"native-error", `,"usage":{"characters":0},"code":"InvalidParameter"`, "", "stop", "missing", true}} {
		t.Run(tc.name, func(t *testing.T) {
			gets := 0
			resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets++
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write(wavFixture())
			}))
			defer resource.Close()
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"output":{"finish_reason":%q,"audio":{"url":%q}}%s}%s`, tc.finish, resource.URL+"/tone", tc.usage, tc.tail)
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL, resource.URL)
			out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "Cherry"})
			if tc.wantErr {
				require.Error(t, err)
				assert.False(t, out.Completed)
				assert.Zero(t, gets)
			} else {
				require.NoError(t, err)
				assert.True(t, out.Completed)
			}
			assert.Equal(t, tc.state, out.Usage.State)
		})
	}
}
func TestInvalidRequestNeverDispatches(t *testing.T) {
	calls := 0
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer supplier.Close()
	for _, r := range []Request{{Text: "", Voice: "V"}, {Text: strings.Repeat("🙂", 601), Voice: "V"}, {Text: string([]byte{255}), Voice: "V"}, {Text: "hi", Voice: "../secret"}, {Text: "hi", Voice: "V", Instructions: ptr("x")}, {Text: "hi", Voice: "V", OptimizeInstructions: ptr(false)}, {Text: "hi", Voice: "V", Language: ptr("Klingon")}, {Text: "hi", Voice: "V", VoiceSettings: &VoiceSettings{Speed: ptr(1.0)}}} {
		p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL)
		_, err := p.Send(context.Background(), r)
		assert.Error(t, err)
	}
	for _, r := range []Request{{Text: "hi", Voice: "../secret"}, {Text: "hi", Voice: "V", Language: ptr("en")}, {Text: "hi", Voice: "V", VoiceSettings: &VoiceSettings{Speed: ptr(0.0)}}, {Text: "hi", Voice: "V", VoiceSettings: &VoiceSettings{Style: ptr(math.NaN())}}, {Text: "hi", Voice: "V", VoiceSettings: &VoiceSettings{SimilarityBoost: ptr(1.01)}}} {
		p := fixtureProducer(t, "elevenlabs", "eleven_multilingual_v2", supplier.URL)
		_, err := p.Send(context.Background(), r)
		assert.Error(t, err)
	}
	assert.Zero(t, calls)
	_, err := PolicyForModel("bailian", "qwen3-new-unknown")
	assert.Error(t, err)
}
func TestRequestDecodeRejectsDuplicateUnknownAndPreservesFalse(t *testing.T) {
	r, err := DecodeRequest([]byte(`{"text":"Hi","voice":"V","optimize_instructions":false}`))
	require.NoError(t, err)
	require.NotNil(t, r.OptimizeInstructions)
	assert.False(t, *r.OptimizeInstructions)
	for _, body := range []string{`{"text":"Hi","voice":"V","url":"https://evil"}`, `{"text":"Hi","text":"Other","voice":"V"}`, `{"text":"Hi","voice_settings":{"speed":1,"speed":2}}`, `{} {}`, strings.Repeat(" ", 131073)} {
		_, err := DecodeRequest([]byte(body))
		assert.Error(t, err)
	}
}
func TestResourceOriginRedirectAndNoBearer(t *testing.T) {
	for _, mode := range []string{"wrong-origin", "redirect", "user-info", "fragment"} {
		t.Run(mode, func(t *testing.T) {
			leaks := 0
			evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaks++ }))
			defer evil.Close()
			res := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Empty(t, r.Header.Get("Authorization"))
				http.Redirect(w, r, evil.URL, http.StatusFound)
			}))
			defer res.Close()
			u := res.URL + "/a"
			switch mode {
			case "wrong-origin":
				u = evil.URL
			case "user-info":
				u = strings.Replace(u, "http://", "http://secret@", 1)
			case "fragment":
				u += "#x"
			}
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":1}}`, u)
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL, res.URL)
			out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "V"})
			require.Error(t, err)
			assert.False(t, out.Completed)
			assert.Equal(t, "confirmed", out.Usage.State)
			assert.Zero(t, leaks)
		})
	}
}
func TestAudioRejectsEmptyTruncatedWrongTypeAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, ct string
		body     []byte
	}{{"empty", "audio/mpeg", nil}, {"fake", "audio/mpeg", []byte("MP3-not-real")}, {"truncated", "audio/mpeg", mp3Fixture(t)[:500]}, {"wrong-type", "text/html", mp3Fixture(t)}, {"garbage-tail", "audio/mpeg", append(mp3Fixture(t), 1, 2, 3)}, {"oversize", "audio/mpeg", bytes.Repeat([]byte{1}, (1<<20)+1)}} {
		t.Run(tc.name, func(t *testing.T) {
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ct)
				w.Header().Set("character-cost", "0")
				_, _ = w.Write(tc.body)
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "elevenlabs", "eleven_multilingual_v2", supplier.URL)
			out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "V"})
			assert.Error(t, err)
			assert.False(t, out.Completed)
			assert.Empty(t, out.Audio)
		})
	}
}

func mp3Fixture(t *testing.T) []byte {
	t.Helper()
	b, e := base64.StdEncoding.DecodeString("//uQxAAAFFSVIFW3gAshOCMbPUAABZATg4E311NnUQUxmPKxwVId1hHdUhwCYZ2KmHi5j5GZCOmMiZh4WYOBlo0wxMBbBCA1AhguBCDQUCvT6HnOh6Hq9nj0gPGBkePKa94b9+/vv3+KUpr3pr//0pHu/w8MAPgAAeHh4eGADoAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAAAAHh4eHhgAAO94eAQAAAAADOYs5TRjI8WGBkEMY/toBgvAKGTihcbnxcpg0haHOMMASgcne0HoSAkmIWFAYSoTwQEVCwEBVKwM9HA4TsyP4G4hgYU0BnTCKPgZlAAYJAxQkDED0lJ9cLEgAgQGBCBsoNw2/wbVD9gxUFoQpENW//hkUY4QWEFhyRQIoFv/5DhcwuYho5o5pNDnDnfb/8okVIqZEWIsYl0umRe//9XyLGJFS6cJ42Ol01pOtH//WqqpPp7Kq16TLRSSMkUTFSRl///WMCEA2zAjANswfAJBMBiB9jA/QrI4H351NmSPIzH5hRcw//uSxBCD1eIJAB3ygAKeQCAB/g059YLFMHTCTDCFQgYwkEG8MDDAsEamLpExWVDhI+ZjGIrCrtZzPK4TQzka5lEBQrMzoVJlU4mOzIahaFxETd0XdiGohLkZkGxGQTKzrQjvRTHukzO7dWnRnMq59XNdzFUxTy5GK55VnMjstmdpWs7MQ1R+90zSoxDu7O5VRxpmY4ndmIVinMkjotS290jnFDAJQC0wIoCWMENBLTB9gmgxIsdfN2Y4Yzc5R2MxKAKoMItBxzBLQU04lKjiP9NVp0xECEU2cP/DlJT8/8e46y/kaqZfh3n556ocOZp05Z8XJ8enygyq0SiZ5CrfrvDGz2L1W+TMVY5E21IiZjE8ks981ZTpVQ8X08s8iEq5+fSelyBaqiDNI5kX76pmebKUzzf+665p8L0hQqcJIfsRXR+UVqpNKHqsSjAjwJ4wJwDTMDVBSjBZgn0wzcemNV47UDU6R3owvQIlNfpIykZTaq0OIWc1wazE4AStcadzOQ1pd0cBT2omooNBH8qsV7PodCy1GsSaxisrChPO8xL3ZP/7ksQrg9NSBQIP8KbK00Ffwf6g2X286z1JQcydFRdXWTnMjGVxlkS9WJdT81KlqVSERKLMrmdGntchmOWmyehnKqXVZXZGOR5yVdnauYY72KVE3VtiLGazANgCowKsCnME8BSjCTwqcxUwgYOJ592jhqCCAxWML3OhIENWGCNNVhNLpUMvi6MKQPLwMPfyGw/HD4vmlZkCa+pd5t+QrzfDzKdtNVFURVcq1zMTpHBaoiXdwXFuMmYuPqmvihS5d/MKmM5LbVkq7vPh4pTHEtcs/UtlWPpIkYvbVTM8Xd3LD76u4i66dBRFuapo3g9GXl6o8Y1mT858WPoYzdJcZ6VE68MpSLs6y3SxVFjFajAjAJ8wJkDWMDMBUTBWQowwxQfTNSj8mTUGB6cwtEI2NVKMy+WDbirOM1g16ZgETUknVkWCFM91KzlgPTfo5wUrIy1KYUV3M6KuqoID3oRlQqTZXZrJaiWnd2vFTKp2tedNZB7fUxPbqutAgKbl8/st6CVHHg5ydsnhV9/ZGuLsnSK2viYts36sv39wHwspLVcsPvz/+5LESgPSzPUCD/CmyuI9n8K+gAGNSqYBQAemA9giJhA4OCYOIIcGI4EVx1lnCactWlHmUGjhJirYUiYNeGgmF2hUZhaIVCYFaB5mAWgA4kAFpeOI6gcEGO6jN+pBwsmUeei2sNNSJmTN0+dcHyiX+QPmrd7WxiG8rNCg6GXWLruN4i+akctsmOn6npa1Gy7enNX1UTMLExSTaP27UL3P/ST8jSvNDm8pHjHxNtTN3F06rML2fz/0cz1XNtcjWepWX4bhDKtJkdRcfzSQPPnPAgAgQggQgMIUJAylJrjJ6NYMx7xQxQBlTDvMEPH8FgRApHQmGOZXBBRnwoNGC4EiYK4BcPGHsHEYUgIxgjADmAIBDF6Q6t5NXRV1mHDN6xmZuImhExigUYsJhcKMXGO9w4DgwDD7bxsxkaCgSYcHmHCfP/nH5VO26QiAQQgBgYAYGDKUpE/+X/6sBeNB9a6ABMQv67UPO1yVd/9ax9kiYbE3XZZGJZGoi1lnMBM6Xd+/73//5HU5K4bl7+OxFH1Zyw2JP85Uplsp/9c1/e/33Ld+//uSxGkAJ64vFtntgAFui2ELnjAGLuRDksh+ntyzmX/qmjUzGYZmo1+v/9b///n//6pLFPG7cYjE5D8btxikf6ZjMM1qaVY83+ONam///9f//////////////Yp88+bzqUlinp7cspLEvp7f/////8qq0sZrU0aq0tLlll//urSiAg5IgEoBOJMdo9J4j6BTHCX4eo6o6dQ1l0oCAgKxm+MZ/G/h6hQGCsRHhEegrDThEeER7WGvwaiKVOiUNQaUDUqdKu/8S8RHiwdgqPBVwNBwGgaWCqwV4ieoGkxBTUU0LjBVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVQ==")
	require.NoError(t, e)
	return b
}

func TestCaseAliasesCannotHideConflictingUsageOrRequest(t *testing.T) {
	_, err := DecodeRequest([]byte(`{"text":"Hi","Text":"Other","voice":"V"}`))
	require.Error(t, err)
	gets := 0
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wavFixture())
	}))
	defer resource.Close()
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":1,"Characters":0}}`, resource.URL+"/audio")
	}))
	defer supplier.Close()
	p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL, resource.URL)
	out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "V"})
	require.Error(t, err)
	assert.Equal(t, "conflict", out.Usage.State)
	assert.False(t, out.Completed)
	assert.Zero(t, gets)
}
func TestBailianUnexpectedTokenCountersRemainUncovered(t *testing.T) {
	for _, usage := range []string{`{"characters":0,"input_tokens":4}`, `{"characters":0,"output_tokens":2}`, `{"characters":0,"total_tokens":2}`, `{"characters":0,"input_tokens":-1}`, `{"characters":0,"future_paid_unit":1}`} {
		t.Run(usage, func(t *testing.T) {
			resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write(wavFixture())
			}))
			defer resource.Close()
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":%s}`, resource.URL+"/audio", usage)
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL, resource.URL)
			out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "V"})
			require.NoError(t, err)
			assert.True(t, out.Completed)
			assert.NotEqual(t, "confirmed", out.Usage.State)
			assert.Nil(t, out.Usage.Characters)
		})
	}
}
func TestConstructionAndOutboundAddressPolicy(t *testing.T) {
	policy, err := PolicyForModel("bailian", "qwen3-tts-flash-2025-11-27")
	require.NoError(t, err)
	target := Target{Policy: policy, Credential: "fixture-secret"}
	_, err = New(target)
	require.NoError(t, err)
	for _, origin := range []string{"http://dashscope.aliyuncs.com", "https://127.0.0.1", "https://dashscope.aliyuncs.com@evil.test", "https://dashscope.aliyuncs.com/path", "https://evil.test"} {
		bad := target
		bad.Origin = origin
		_, err = New(bad)
		assert.Error(t, err)
	}
	for _, origin := range []string{"http://127.0.0.1", "https://evil.test", "https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com:444"} {
		bad := target
		bad.ResourceOrigins = []string{origin}
		_, err = New(bad)
		assert.Error(t, err)
	}
	bad := target
	bad.Policy.MaxTextScalars = 100000
	_, err = New(bad)
	assert.Error(t, err)
	for _, limits := range []Limits{{MaxAudioBytes: -1}, {MaxAudioBytes: 33 << 20}, {MaxAudioSeconds: 601}, {TimeoutSeconds: 121}} {
		bad := target
		bad.Limits = limits
		_, err = New(bad)
		assert.Error(t, err)
	}
	p, err := New(target)
	require.NoError(t, err)
	tr := p.client.Transport.(*http.Transport)
	for _, address := range []string{"127.0.0.1:80", "169.254.169.254:80", "evil.test:443"} {
		conn, err := tr.DialContext(context.Background(), "tcp", address)
		assert.Error(t, err)
		assert.Nil(t, conn)
	}
	for _, raw := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "192.0.2.1", "2001:db8::1", "64:ff9b::a00:1"} {
		assert.False(t, publicIP(netip.MustParseAddr(raw)), raw)
	}
	assert.True(t, publicIP(netip.MustParseAddr("8.8.8.8")))
	u, err := p.resourceURL("http://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/a.wav?signature=x")
	require.NoError(t, err)
	assert.Equal(t, "https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/a.wav?signature=x", u)
}
func TestHTTPFailureTruncationCancellationNoRetryOrUnsafeEvidence(t *testing.T) {
	for _, mode := range []string{"refusal", "redirect", "truncated", "unsafe-ref", "duplicate-ref", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("request-id", "safe-ref")
				if mode == "unsafe-ref" {
					w.Header().Set("request-id", "secret with spaces")
				}
				if mode == "duplicate-ref" {
					w.Header().Add("request-id", "second")
				}
				if mode == "refusal" {
					w.WriteHeader(429)
					_, _ = w.Write([]byte("sensitive-response"))
					return
				}
				if mode == "redirect" {
					w.Header().Set("Location", "/again")
					w.WriteHeader(307)
					return
				}
				w.Header().Set("Content-Type", "audio/mpeg")
				if mode == "truncated" {
					w.Header().Set("Content-Length", "100000")
				}
				_, _ = w.Write(mp3Fixture(t))
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "elevenlabs", "eleven_multilingual_v2", supplier.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			out, err := p.Send(ctx, Request{Text: "Hi", Voice: "V"})
			if mode == "unsafe-ref" || mode == "duplicate-ref" {
				require.NoError(t, err)
				assert.Empty(t, out.RequestID)
			} else {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "fixture-secret")
				assert.NotContains(t, err.Error(), "sensitive-response")
				assert.False(t, out.Completed)
			}
			if mode == "cancelled" {
				assert.Zero(t, calls)
			} else {
				assert.Equal(t, 1, calls)
			}
		})
	}
}
func TestWAVAndDurationValidation(t *testing.T) {
	good := wavFixture()
	badLength := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(badLength[4:], 9000000)
	badSamples := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(badSamples[40:], 201)
	badRate := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(badRate[24:], math.MaxUint32)
	for _, audio := range [][]byte{good[:40], good[:100], badLength, badSamples, badRate, []byte("RIFFfake")} {
		assert.Error(t, validateAudio(audio, "audio/wav", 1))
	}
	assert.NoError(t, validateAudio(good, "audio/wav", 1))
	assert.Error(t, validateAudio(bytes.Repeat(mp3Fixture(t), 9), "audio/mpeg", 1))
}

func TestBailianDownloadFailureRetainsUsageWithoutAnotherSynthesis(t *testing.T) {
	for _, mode := range []string{"truncated", "wrong-type", "invalid-wav", "refused"} {
		t.Run(mode, func(t *testing.T) {
			posts, gets := 0, 0
			resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets++
				assert.Empty(t, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "audio/wav")
				switch mode {
				case "truncated":
					w.Header().Set("Content-Length", "9000")
				case "wrong-type":
					w.Header().Set("Content-Type", "application/json")
				case "refused":
					w.WriteHeader(403)
					return
				case "invalid-wav":
					_, _ = w.Write([]byte("RIFF invalid"))
					return
				}
				_, _ = w.Write(wavFixture())
			}))
			defer resource.Close()
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"request_id":"safe-ref","output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":42}}`, resource.URL+"/audio")
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL, resource.URL)
			out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "Cherry"})
			require.Error(t, err)
			assert.False(t, out.Completed)
			assert.Empty(t, out.Audio)
			assert.Equal(t, "confirmed", out.Usage.State)
			require.NotNil(t, out.Usage.Characters)
			assert.Equal(t, int64(42), *out.Usage.Characters)
			assert.Equal(t, "safe-ref", out.RequestID)
			assert.Equal(t, 1, posts)
			assert.Equal(t, 1, gets)
		})
	}
}
func TestInstructionsAndBodyAdmissionLimits(t *testing.T) {
	calls := 0
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer supplier.Close()
	p := fixtureProducer(t, "bailian", "qwen3-tts-instruct-flash-2026-01-26", supplier.URL)
	for _, r := range []Request{{Text: "Hi", Voice: "V", Instructions: ptr("")}, {Text: "Hi", Voice: "V", Instructions: ptr(strings.Repeat("a", 1601))}, {Text: "Hi", Voice: "V", OptimizeInstructions: ptr(true)}} {
		_, err := p.Send(context.Background(), r)
		assert.Error(t, err)
	}
	p = fixtureProducer(t, "elevenlabs", "eleven_multilingual_v2", supplier.URL)
	_, err := p.Send(context.Background(), Request{Text: strings.Repeat("a", 10001), Voice: "V"})
	require.Error(t, err)
	assert.Zero(t, calls)
	r, err := DecodeRequest([]byte(`{"text":"Hi","voice":"V","voice_settings":{"stability":0,"use_speaker_boost":false}}`))
	require.NoError(t, err)
	require.NotNil(t, r.VoiceSettings)
	require.NotNil(t, r.VoiceSettings.Stability)
	assert.Zero(t, *r.VoiceSettings.Stability)
	require.NotNil(t, r.VoiceSettings.UseSpeakerBoost)
	assert.False(t, *r.VoiceSettings.UseSpeakerBoost)
}

func TestUnicodeCaseAliasesCannotCertifyUsage(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"top-level-usage", `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":42},"uſage":{"characters":0}}`},
		{"nested-characters", `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":42,"Characterſ":0}}`},
		{"nested-completion", `{"output":{"finish_reason":"length","finiſh_reason":"stop","audio":{"url":%q}},"usage":{"characters":0}}`},
		{"top-level-status", `{"status_code":400,"ſtatus_code":200,"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gets := 0
			resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets++
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write(wavFixture())
			}))
			defer resource.Close()
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, tc.body, resource.URL+"/tone")
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL, resource.URL)
			out, err := p.Send(context.Background(), Request{Text: "Hello", Voice: "Cherry"})
			require.Error(t, err)
			assert.False(t, out.Completed)
			assert.Equal(t, "conflict", out.Usage.State)
			assert.Nil(t, out.Usage.Characters)
			assert.Empty(t, out.Usage.RawQuantity)
			assert.Zero(t, gets)
		})
	}
}

func TestUnicodeCaseAliasesRejectRequestFieldsWithoutChangingText(t *testing.T) {
	for _, body := range []string{
		`{"text":"Hi","voice":"V","instructions":"first","inſtructions":"second"}`,
		`{"text":"Hi","voice":"V","voice_settings":{"stability":1,"ſtability":0}}`,
		`{"text":"Hi","voice":"V","voice_settings":{"use_speaker_boost":true,"uſe_speaker_booſt":false}}`,
	} {
		_, err := DecodeRequest([]byte(body))
		assert.Error(t, err)
	}
	r, err := DecodeRequest([]byte(`{"text":"ſ Ελληνικά 中文 🙂 K","voice":"V","instructions":"Warm ſoft voice"}`))
	require.NoError(t, err)
	assert.Equal(t, "ſ Ελληνικά 中文 🙂 K", r.Text)
	require.NotNil(t, r.Instructions)
	assert.Equal(t, "Warm ſoft voice", *r.Instructions)
}

func TestUnknownUnicodeUsageDimensionRemainsUncovered(t *testing.T) {
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wavFixture())
	}))
	defer resource.Close()
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":42,"obſervation":1}}`, resource.URL+"/tone")
	}))
	defer supplier.Close()
	p := fixtureProducer(t, "bailian", "qwen3-tts-flash-2025-11-27", supplier.URL, resource.URL)
	out, err := p.Send(context.Background(), Request{Text: "Hi", Voice: "Cherry"})
	require.NoError(t, err)
	assert.True(t, out.Completed)
	assert.Equal(t, "uncovered", out.Usage.State)
	assert.Nil(t, out.Usage.Characters)
	assert.Equal(t, "42", out.Usage.RawQuantity)
}
