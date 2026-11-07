package nativespeech

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The observed Bailian response contains complete PCM but these streaming
// placeholders instead of RIFF/data byte counts. It is not a valid generic WAV.
func bailianPlaceholderWAV() []byte {
	b := wavFixture()
	binary.LittleEndian.PutUint32(b[4:], 0x7fffffbf)
	binary.LittleEndian.PutUint32(b[40:], 0x7fffff9b)
	return b
}

func TestBailianCompletePlaceholderWAVUsesTransportLengthAndSupplierUsage(t *testing.T) {
	audio := bailianPlaceholderWAV()
	original := bytes.Clone(audio)
	want := wavFixture()
	posts, gets := 0, 0
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		assert.Empty(t, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "audio/x-wav")
		w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
		_, _ = w.Write(audio)
	}))
	defer resource.Close()
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"request_id":"placeholder-audio","output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":14}}`, resource.URL+"/audio.wav")
	}))
	defer supplier.Close()
	p := fixtureProducer(t, "bailian", "qwen3-tts-flash", supplier.URL, resource.URL)
	out, err := p.Send(context.Background(), Request{Text: "你好，语音测试。", Voice: "Cherry"})
	require.NoError(t, err)
	assert.True(t, out.Completed)
	assert.Equal(t, want, out.Audio)
	assert.Equal(t, sha256.Sum256(want), out.SHA256)
	assert.Equal(t, "audio/wav", out.ContentType)
	require.NotNil(t, out.Usage.Characters)
	assert.Equal(t, int64(14), *out.Usage.Characters)
	assert.Equal(t, "confirmed", out.Usage.State)
	assert.Equal(t, "placeholder-audio", out.RequestID)
	assert.Equal(t, original, audio)
	assert.Equal(t, 1, posts)
	assert.Equal(t, 1, gets)
	assert.Error(t, ValidateAudioFile(original, "audio/wav", 10), "generic compatible audio must still reject the placeholders")
}

func TestBailianPlaceholderWAVRejectsIncompleteTransportAndNoncanonicalAudio(t *testing.T) {
	for _, tc := range []struct {
		name, transport string
		mutate          func([]byte) []byte
		maxBytes        int
	}{
		{name: "truncated HTTP", transport: "truncated"},
		{name: "chunked HTTP", transport: "chunked"},
		{name: "unknown HTTP length", transport: "unknown"},
		{name: "oversize HTTP body", maxBytes: 100},
		{name: "odd PCM frame", mutate: func(b []byte) []byte { return b[:len(b)-1] }},
		{name: "no PCM frames", mutate: func(b []byte) []byte { return b[:44] }},
		{name: "wrong RIFF placeholder", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:], 0x7fffffff); return b }},
		{name: "wrong data placeholder", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:], 0x7fffffff); return b }},
		{name: "only RIFF placeholder", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:], 200); return b }},
		{name: "only data placeholder", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:], 236); return b }},
		{name: "non PCM", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint16(b[20:], 3); return b }},
		{name: "stereo", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint16(b[22:], 2); return b }},
		{name: "wrong sample rate", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[24:], 48000); return b }},
		{name: "wrong byte rate", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[28:], 24000); return b }},
		{name: "wrong block alignment", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint16(b[32:], 4); return b }},
		{name: "wrong sample width", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint16(b[34:], 24); return b }},
		{name: "extended format", mutate: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[16:], 18); return b }},
		{name: "unexpected first chunk", mutate: func(b []byte) []byte { copy(b[12:], "JUNK"); return b }},
		{name: "unexpected audio chunk", mutate: func(b []byte) []byte { copy(b[36:], "LIST"); return b }},
		{name: "duration exceeds limit", mutate: func(b []byte) []byte { return append(b[:44], make([]byte, 480002)...) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			audio := bailianPlaceholderWAV()
			if tc.mutate != nil {
				audio = tc.mutate(audio)
			}
			posts, gets := 0, 0
			resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets++
				w.Header().Set("Content-Type", "audio/wav")
				switch tc.transport {
				case "truncated":
					w.Header().Set("Content-Length", strconv.Itoa(len(audio)+2))
				case "chunked":
					w.(http.Flusher).Flush()
				case "unknown":
					conn, buf, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
					defer conn.Close()
					_, err = buf.WriteString("HTTP/1.0 200 OK\r\nContent-Type: audio/wav\r\nConnection: close\r\n\r\n")
					require.NoError(t, err)
					_, err = buf.Write(audio)
					require.NoError(t, err)
					require.NoError(t, buf.Flush())
					return
				default:
					w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
				}
				_, _ = w.Write(audio)
			}))
			defer resource.Close()
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":14}}`, resource.URL+"/audio.wav")
			}))
			defer supplier.Close()
			p := fixtureProducer(t, "bailian", "qwen3-tts-flash", supplier.URL, resource.URL)
			if tc.maxBytes != 0 {
				p.target.Limits.MaxAudioBytes = tc.maxBytes
			}
			out, err := p.Send(context.Background(), Request{Text: "你好", Voice: "Cherry"})
			require.Error(t, err)
			assert.False(t, out.Completed)
			assert.Empty(t, out.Audio)
			require.NotNil(t, out.Usage.Characters)
			assert.Equal(t, int64(14), *out.Usage.Characters)
			assert.Equal(t, 1, posts)
			assert.Equal(t, 1, gets)
		})
	}
}

func TestBailianWAVNormalizationRequiresExactTransportAndPreservesInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		length  int64
		encoded bool
	}{
		{"missing length", -1, false},
		{"zero length", 0, false},
		{"short declared body", 242, false},
		{"long declared body", 246, false},
		{"encoded body despite length", 244, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := bailianPlaceholderWAV()
			out := normalizeBailianWAV(input, tc.length, tc.encoded)
			assert.Equal(t, input, out)
			assert.Error(t, ValidateAudioFile(out, "audio/wav", 10))
		})
	}
	input := bailianPlaceholderWAV()
	out := normalizeBailianWAV(input, 244, false)
	require.Equal(t, wavFixture(), out)
	out[44] ^= 0xff
	assert.Equal(t, bailianPlaceholderWAV(), input, "normalization must not alias supplier input")
	valid := wavFixture()
	assert.Equal(t, valid, normalizeBailianWAV(valid, -1, true), "valid WAV does not gain a new HTTP framing restriction")
}

func TestElevenDoesNotAcceptBailianWAVPlaceholders(t *testing.T) {
	posts := 0
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("character-cost", "14")
		w.Header().Set("Content-Length", "244")
		_, _ = w.Write(bailianPlaceholderWAV())
	}))
	defer supplier.Close()
	p := fixtureProducer(t, "elevenlabs", "eleven_multilingual_v2", supplier.URL)
	out, err := p.Send(context.Background(), Request{Text: "hello", Voice: "Voice123"})
	require.Error(t, err)
	assert.False(t, out.Completed)
	assert.Empty(t, out.Audio)
	assert.Equal(t, 1, posts)
}

// Opt-in replay reads the saved diagnostic body, never a supplier credential or
// network URL. The tiny deterministic fixture above remains the CI regression.
func TestBailianSavedVendorWAVReplay(t *testing.T) {
	path := os.Getenv("BAILIAN_WAV_REPLAY_FILE")
	if path == "" {
		t.Skip("no retained vendor fixture requested")
	}
	audio, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "06546f44401af87d6bbb683697b2f6b5cf955d2e5b0faf4b61f733b44044fdd6", fmt.Sprintf("%x", sha256.Sum256(audio)))
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/x-wav")
		w.Header().Set("Content-Length", "38444")
		_, _ = w.Write(audio)
	}))
	defer resource.Close()
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":{"finish_reason":"stop","audio":{"url":%q}},"usage":{"characters":5}}`, resource.URL+"/saved.wav")
	}))
	defer supplier.Close()
	p := fixtureProducer(t, "bailian", "qwen3-tts-flash", supplier.URL, resource.URL)
	out, err := p.Send(context.Background(), Request{Text: "诊断。", Voice: "Cherry"})
	require.NoError(t, err)
	require.Len(t, out.Audio, 38444)
	assert.Equal(t, uint32(38436), binary.LittleEndian.Uint32(out.Audio[4:]))
	assert.Equal(t, uint32(38400), binary.LittleEndian.Uint32(out.Audio[40:]))
	assert.Equal(t, audio[44:], out.Audio[44:])
	assert.Equal(t, int64(5), *out.Usage.Characters)
	require.NoError(t, ValidateAudioFile(out.Audio, "audio/wav", 10))
}
