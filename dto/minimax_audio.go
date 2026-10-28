package dto

import (
	"bytes"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"math"
	"unicode/utf8"
)

// MaxMiniMaxSpeechBodyBytes bounds the original canonical request, including JSON whitespace.
const MaxMiniMaxSpeechBodyBytes = 128 << 10

// MiniMaxSpeechOptions is an explicit native extension allowlist. Routing,
// text, stream and priced codec always come from the canonical AudioRequest.
type MiniMaxSpeechOptions struct {
	VoiceSetting MiniMaxVoiceSetting `json:"voice_setting,omitempty"`
	AudioSetting MiniMaxAudioSetting `json:"audio_setting,omitempty"`
}
type MiniMaxVoiceSetting struct {
	VoiceID           string   `json:"voice_id,omitempty"`
	Speed             *float64 `json:"speed,omitempty"`
	Vol               *float64 `json:"vol,omitempty"`
	Pitch             *int     `json:"pitch,omitempty"`
	Emotion           string   `json:"emotion,omitempty"`
	TextNormalization *bool    `json:"text_normalization,omitempty"`
	LatexRead         *bool    `json:"latex_read,omitempty"`
}
type MiniMaxAudioSetting struct {
	Format     string `json:"format,omitempty"`
	SampleRate *int   `json:"sample_rate,omitempty"`
	Bitrate    *int   `json:"bitrate,omitempty"`
	Channel    *int   `json:"channel,omitempty"`
	ForceCBR   *bool  `json:"force_cbr,omitempty"`
}
type MiniMaxSpeechRequest struct {
	Model         string                `json:"model"`
	Text          string                `json:"text"`
	Stream        *bool                 `json:"stream,omitempty"`
	StreamOptions *MiniMaxStreamOptions `json:"stream_options,omitempty"`
	VoiceSetting  MiniMaxVoiceSetting   `json:"voice_setting"`
	AudioSetting  MiniMaxAudioSetting   `json:"audio_setting"`
	OutputFormat  string                `json:"output_format"`
}
type MiniMaxStreamOptions struct {
	ExcludeAggregatedAudio *bool `json:"exclude_aggregated_audio,omitempty"`
}

func BuildMiniMaxSpeech(r AudioRequest) (*MiniMaxSpeechRequest, error) {
	fail := func() (*MiniMaxSpeechRequest, error) {
		return nil, fmt.Errorf("unsupported MiniMax speech model, input or options")
	}
	if r.Model != "speech-2.8-hd" && r.Model != "speech-2.8-turbo" {
		return fail()
	}
	if !utf8.ValidString(r.Input) || utf8.RuneCountInString(r.Input) == 0 || utf8.RuneCountInString(r.Input) >= 10000 || r.Voice == "" {
		return fail()
	}
	if r.Instructions != "" || r.StreamFormat != "" && r.StreamFormat != "audio" || len(r.TaskType)+len(r.Language)+len(r.RefAudio)+len(r.RefText)+len(r.XVectorOnlyMode)+len(r.MaxNewTokens)+len(r.InitialCodecChunkFrames) > 0 {
		return fail()
	}
	options := MiniMaxSpeechOptions{}
	if len(r.Metadata) > 0 {
		if err := common.UnmarshalUnique(r.Metadata, &options); err != nil {
			return fail()
		}
		decoder := common.NewJsonDecoder(bytes.NewReader(r.Metadata))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return fail()
		}
	}
	if options.VoiceSetting.VoiceID != "" || options.VoiceSetting.Speed != nil || options.AudioSetting.Format != "" {
		return fail()
	}
	options.VoiceSetting.VoiceID = r.Voice
	options.VoiceSetting.Speed = r.Speed
	v := options.VoiceSetting
	if v.Speed != nil && (math.IsNaN(*v.Speed) || math.IsInf(*v.Speed, 0) || *v.Speed < 0.5 || *v.Speed > 2) {
		return fail()
	}
	if v.Vol != nil && (math.IsNaN(*v.Vol) || math.IsInf(*v.Vol, 0) || *v.Vol <= 0 || *v.Vol > 10) {
		return fail()
	}
	if v.Pitch != nil && (*v.Pitch < -12 || *v.Pitch > 12) {
		return fail()
	}
	switch v.Emotion {
	case "", "happy", "sad", "angry", "fearful", "disgusted", "surprised", "calm":
	default:
		return fail()
	}
	format := r.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	switch format {
	case "mp3", "wav", "flac", "pcm":
	default:
		return fail()
	}
	stream := r.StreamFormat == "audio" || r.Stream != nil && *r.Stream
	if r.Stream != nil && !*r.Stream && r.StreamFormat == "audio" {
		return fail()
	}
	if stream && format != "mp3" {
		return fail()
	}
	a := options.AudioSetting
	a.Format = format
	if a.SampleRate != nil {
		switch *a.SampleRate {
		case 8000, 16000, 22050, 24000, 32000, 44100:
		default:
			return fail()
		}
	}
	if a.Bitrate != nil {
		if format != "mp3" {
			return fail()
		}
		switch *a.Bitrate {
		case 32000, 64000, 128000, 256000:
		default:
			return fail()
		}
	}
	if a.Channel != nil && *a.Channel != 1 && *a.Channel != 2 {
		return fail()
	}
	if a.ForceCBR != nil && *a.ForceCBR && (!stream || format != "mp3") {
		return fail()
	}
	result := &MiniMaxSpeechRequest{Model: r.Model, Text: r.Input, Stream: &stream, VoiceSetting: v, AudioSetting: a, OutputFormat: "hex"}
	if stream {
		result.StreamOptions = &MiniMaxStreamOptions{ExcludeAggregatedAudio: common.GetPointer(true)}
	}
	return result, nil
}
