package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	// DashScope serves qwen3-tts on the multimodal-generation wire; the OpenAI
	// compatible /v1/audio/speech path returns 404 for these models.
	bailianSpeechPath = "/api/v1/services/aigc/multimodal-generation/generation"
	probeSpeechText   = "LLMRouter connection test."
)

// buildChannelAudioProbe returns the request shape a speech or transcription
// model accepts. Voice is mandatory for synthesis; transcription carries the
// WAV separately in the multipart body.
func buildChannelAudioProbe(modelName, voice string) *dto.AudioRequest {
	if isTranscriptionTestModel(modelName) {
		return &dto.AudioRequest{Model: modelName, ResponseFormat: "json"}
	}
	if voice == "" {
		voice = "Cherry"
	}
	return &dto.AudioRequest{Model: modelName, Input: probeSpeechText, Voice: voice}
}

// channelProbeWAV renders one second of 16 kHz mono silence as PCM WAV. Speech
// models accept it as valid audio, and it costs a single second of ASR pricing.
func channelProbeWAV() ([]byte, error) {
	const (
		rate          = 16000
		seconds       = 1
		channels      = 1
		bitsPerSample = 16
	)
	dataBytes := rate * seconds * channels * bitsPerSample / 8
	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+dataBytes))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate*channels*bitsPerSample/8))
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels*bitsPerSample/8))
	_ = binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(dataBytes))
	buf.Write(make([]byte, dataBytes))
	// RIFF(4) size(4) WAVE(4) fmt(4) fmtSize(4) fmtBody(16) data(4) dataSize(4)
	if buf.Len() != 44+dataBytes {
		return nil, fmt.Errorf("unexpected WAV size %d", buf.Len())
	}
	return buf.Bytes(), nil
}

// bailianSpeechBody shapes the DashScope multimodal-generation body used by
// qwen3-tts.
func bailianSpeechBody(modelName, voice string) []byte {
	if voice == "" {
		voice = "Cherry"
	}
	raw, err := common.Marshal(struct {
		Model string `json:"model"`
		Input struct {
			Text  string `json:"text"`
			Voice string `json:"voice"`
		} `json:"input"`
	}{Model: modelName, Input: struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}{Text: probeSpeechText, Voice: voice}})
	if err != nil {
		return nil
	}
	return raw
}

// channelProbeASRBody builds the multipart upload the transcription wire expects.
func channelProbeASRBody(modelName string) ([]byte, string) {
	wav, err := channelProbeWAV()
	if err != nil {
		return nil, ""
	}
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	_ = writer.WriteField("model", modelName)
	if part, err := writer.CreateFormFile("file", "probe.wav"); err == nil {
		_, _ = part.Write(wav)
	}
	_ = writer.Close()
	return payload.Bytes(), writer.FormDataContentType()
}

// testAudioChannel probes speech and transcription models with their own wire
// shapes. Connection tests never bill or record usage, so success is a 2xx with a
// non-empty body that the model produced.
func testAudioChannel(ctx context.Context, channel *model.Channel, key, modelName, voice string, speech bool, tik time.Time) testResult {
	if speech {
		return testBailianSpeech(ctx, channel, key, modelName, voice, tik)
	}
	return testTranscription(ctx, channel, key, modelName, tik)
}

// testBailianSpeech posts the DashScope synthesis body directly. The native
// speech path is separate from the compat chat adaptor, so there is no adaptor
// body to reuse.
func testBailianSpeech(ctx context.Context, channel *model.Channel, key, modelName, voice string, tik time.Time) testResult {
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com"
	}
	body := bailianSpeechBody(modelName, voice)
	if body == nil {
		return simpleTestResult(fmt.Errorf("speech probe: cannot encode the synthesis body"))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+bailianSpeechPath, bytes.NewReader(body))
	if err != nil {
		return simpleTestResult(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	resp, err := service.GetHttpClient().Do(request)
	if err != nil {
		return simpleTestResult(fmt.Errorf("speech probe: send failed (%v)", err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return simpleTestResult(fmt.Errorf("speech probe: HTTP %d %s", resp.StatusCode, truncateForProbe(raw)))
	}
	audioURL := bailianAudioURL(raw)
	if audioURL == "" {
		return simpleTestResult(fmt.Errorf("speech probe: response carried no audio (%s)", truncateForProbe(raw)))
	}
	// The signed URL itself proves synthesis happened. Fetching it is optional:
	// HEAD is often rejected (403) while GET works, and both stay free.
	if status, err := headOrGet(ctx, audioURL); err != nil || (status != http.StatusOK && status != http.StatusForbidden) {
		return simpleTestResult(fmt.Errorf("speech probe: audio %s unavailable (HTTP %d)", audioURL, status))
	}
	_ = tik
	return testResult{}
}

func bailianAudioURL(raw []byte) string {
	var payload struct {
		Output struct {
			Audio struct {
				URL string `json:"url"`
			} `json:"audio"`
		} `json:"output"`
	}
	if common.UnmarshalJsonStr(string(raw), &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Output.Audio.URL)
}

func headOrGet(ctx context.Context, url string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := service.GetHttpClient().Do(request)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func truncateForProbe(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// testTranscription runs the production audio admission and the channel's own
// adaptor, so Flash ASR forwards the same inline carrier a real request uses.
func testTranscription(ctx context.Context, channel *model.Channel, key, modelName string, tik time.Time) testResult {
	body, contentType := channelProbeASRBody(modelName)
	if body == nil {
		return simpleTestResult(fmt.Errorf("transcription probe: cannot build the WAV upload"))
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	storage, err := common.CreateBodyStorage(body)
	if err != nil {
		return simpleTestResult(err)
	}
	c.Set(common.KeyBodyStorage, storage)
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/audio/transcriptions", nil)
	c.Request.Header.Set("Content-Type", contentType)
	c.Set("channel_test_probe", true)
	c.Set("channel", channel.Type)
	c.Set("base_url", channel.GetBaseURL())
	group, err := model.GetUserGroup(0, false)
	if err == nil {
		c.Set("group", group)
	}
	request := buildChannelAudioProbe(modelName, "")
	info, relayErr := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAIAudio, request, nil)
	if relayErr != nil {
		return simpleTestResult(relayErr)
	}
	info.IsChannelTest = true
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ChannelBaseUrl: channel.GetBaseURL(), ApiKey: key}
	if err := relayhelper.ValidateDomesticAudio(c, info, channel.Type, modelName, modelName); err != nil {
		return simpleTestResult(err)
	}
	adaptor := newChannelTestAudioAdaptor(channel, info)
	if adaptor == nil {
		return simpleTestResult(fmt.Errorf("transcription probe: channel %d has no audio adaptor", channel.Id))
	}
	adaptor.Init(info)
	converted, err := adaptor.ConvertAudioRequest(c, info, *request)
	if err != nil {
		return simpleTestResult(err)
	}
	resp, sendErr := adaptor.DoRequest(c, info, converted)
	return finishAudioChannelTest(c, info, resp, sendErr, tik)
}

// finishAudioChannelTest mirrors the chat success criteria for transcription.
func finishAudioChannelTest(c *gin.Context, info *relaycommon.RelayInfo, resp any, sendErr error, tik time.Time) testResult {
	if sendErr != nil {
		return simpleTestResult(sendErr)
	}
	httpResp, ok := resp.(*http.Response)
	if !ok || httpResp == nil {
		return simpleTestResult(fmt.Errorf("audio channel test returned no response"))
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
		return simpleTestResult(fmt.Errorf("audio channel test: HTTP %d %s", httpResp.StatusCode, truncateForProbe(raw)))
	}
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		return simpleTestResult(fmt.Errorf("audio channel test returned an empty response"))
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "time": float64(time.Since(tik).Milliseconds()) / 1000.0})
	return testResult{context: c}
}

// channelProbeSeedImage returns the operator-supplied first frame for video
// probes, read from the channel's settings so image-to-video models can be
// tested against real media instead of a fabricated frame.
func channelProbeSeedImage(channel *model.Channel) string {
	return channelProbeSetting(channel, "probe_seed_image")
}

// channelProbeVoice returns the configured synthesis voice, defaulting to the
// supplier's public voice used by the built-in probe.
func channelProbeVoice(channel *model.Channel) string {
	return channelProbeSetting(channel, "probe_voice")
}

func channelProbeSetting(channel *model.Channel, key string) string {
	if channel == nil {
		return ""
	}
	var settings map[string]any
	if common.UnmarshalJsonStr(channel.OtherSettings, &settings) != nil {
		return ""
	}
	value, _ := settings[key].(string)
	return strings.TrimSpace(value)
}

// channelProbeKey takes the first pool-admitted channel key without advancing
// rotation, so a manual test leaves multi-key state untouched.
func channelProbeKey(ctx context.Context, channel *model.Channel, modelName string) (string, error) {
	refs, keys, err := model.ChannelAccountRefs(ctx, model.DB, channel)
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("channel %d has no available key", channel.Id)
	}
	ref := refs[0]
	if ref.KeyIndex < 0 || ref.KeyIndex >= len(keys) || keys[ref.KeyIndex] == "" {
		return "", fmt.Errorf("channel %d key %d is unavailable", channel.Id, ref.KeyIndex)
	}
	if err := model.CheckUpstreamAccounts(ctx, model.DB, []model.UpstreamAccountRef{ref},
		model.UpstreamAccountScope{Operation: "channel.test", Model: modelName}, time.Now()); err != nil {
		return "", fmt.Errorf("channel %d key is cooling down: %v", channel.Id, err)
	}
	return keys[ref.KeyIndex], nil
}

// newChannelTestAudioAdaptor resolves the channel's own API adaptor so the
// transcription probe follows the production wire.
func newChannelTestAudioAdaptor(channel *model.Channel, info *relaycommon.RelayInfo) audioAdaptor {
	adaptor, ok := relay.GetAdaptor(info.ApiType).(audioAdaptor)
	if !ok {
		return nil
	}
	return adaptor
}

// audioAdaptor narrows the fields used by the audio probe.
type audioAdaptor interface {
	Init(*relaycommon.RelayInfo)
	ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error)
	DoRequest(*gin.Context, *relaycommon.RelayInfo, io.Reader) (any, error)
}
