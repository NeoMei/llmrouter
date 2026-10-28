package nativespeech

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/tcolgate/mp3"
)

// validateAudio validates complete PCM samples or complete MPEG frames within
// byte/duration ceilings. MPEG framing is not an acoustic-quality guarantee.
func validateAudio(b []byte, contentType string, maxSeconds int) error {
	return validateAudioFile(b, contentType, maxSeconds, true)
}

// ValidateAudioFile validates complete bounded PCM or MPEG audio from compatible
// producers, without imposing Eleven's particular negotiated sample rate.
func ValidateAudioFile(b []byte, contentType string, maxSeconds int) error {
	if len(b) > 32<<20 || maxSeconds < 1 || maxSeconds > 86400 {
		return errResponse
	}
	return validateAudioFile(b, contentType, maxSeconds, false)
}
func validateAudioFile(b []byte, contentType string, maxSeconds int, exactEleven bool) error {
	if len(b) == 0 {
		return errResponse
	}
	if contentType == "audio/wav" {
		if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(b[4:8]))+8 != uint64(len(b)) {
			return errResponse
		}
		var fmtSeen, dataSeen bool
		var channels, rate, bits, dataSize int
		for offset := 12; offset < len(b); {
			if len(b)-offset < 8 {
				return errResponse
			}
			size64 := uint64(binary.LittleEndian.Uint32(b[offset+4 : offset+8]))
			if size64 > uint64(len(b)-offset-8) {
				return errResponse
			}
			size := int(size64)
			payload := b[offset+8 : offset+8+size]
			switch string(b[offset : offset+4]) {
			case "fmt ":
				if fmtSeen || size < 16 || binary.LittleEndian.Uint16(payload) != 1 {
					return errResponse
				}
				fmtSeen = true
				channels = int(binary.LittleEndian.Uint16(payload[2:]))
				rate = int(binary.LittleEndian.Uint32(payload[4:]))
				bits = int(binary.LittleEndian.Uint16(payload[14:]))
				if channels < 1 || channels > 2 || rate < 8000 || rate > 48000 || (bits != 16 && bits != 24 && bits != 32) {
					return errResponse
				}
				block := channels * (bits / 8)
				if int(binary.LittleEndian.Uint16(payload[12:])) != block || uint64(binary.LittleEndian.Uint32(payload[8:])) != uint64(rate*block) {
					return errResponse
				}
			case "data":
				if !fmtSeen || dataSeen || size == 0 {
					return errResponse
				}
				dataSeen = true
				dataSize = size
			}
			offset += 8 + size + (size % 2)
			if offset > len(b) {
				return errResponse
			}
		}
		if !fmtSeen || !dataSeen || dataSize%(channels*(bits/8)) != 0 || int64(dataSize) > int64(maxSeconds)*int64(rate)*int64(channels*(bits/8)) {
			return errResponse
		}
		decoder := wav.NewDecoder(bytes.NewReader(b))
		if !decoder.IsValidFile() {
			return errResponse
		}
		samples := 0
		buffer := &audio.IntBuffer{Data: make([]int, 4096), Format: &audio.Format{NumChannels: channels, SampleRate: rate}}
		for {
			n, e := decoder.PCMBuffer(buffer)
			samples += n
			if e != nil && e != io.EOF {
				return errResponse
			}
			if samples > dataSize/(bits/8) {
				return errResponse
			}
			if n == 0 || e == io.EOF {
				break
			}
		}
		if samples != dataSize/(bits/8) {
			return errResponse
		}
		return nil
	}
	if contentType != "audio/mpeg" {
		return errResponse
	}
	if len(b) >= 10 && string(b[:3]) == "ID3" {
		if (b[3] != 3 && b[3] != 4) || b[4] != 0 || b[5]&0x0f != 0 {
			return errResponse
		}
		size := 0
		for _, v := range b[6:10] {
			if v&128 != 0 {
				return errResponse
			}
			size = (size << 7) | int(v)
		}
		size += 10
		if b[5]&0x10 != 0 {
			size += 10
		}
		if size >= len(b) {
			return errResponse
		}
		b = b[size:]
	}
	if len(b) >= 128 && string(b[len(b)-128:len(b)-125]) == "TAG" {
		b = b[:len(b)-128]
	}
	reader := bytes.NewReader(b)
	decoder := mp3.NewDecoder(reader)
	var frame mp3.Frame
	frames := 0
	duration := 0.0
	for reader.Len() > 0 {
		if reader.Len() < 4 {
			return errResponse
		}
		offset := len(b) - reader.Len()
		h := mp3.FrameHeader(b[offset : offset+4])
		if h.Version() == mp3.MPEGReserved || (exactEleven && h.Version() != mp3.MPEG1) || h.Layer() != mp3.Layer3 || h.SampleRate() <= 0 || h.BitRate() <= 0 || (exactEleven && (h.SampleRate() != 44100 || h.BitRate() != 128000)) {
			return errResponse
		}
		skipped := 0
		before := reader.Len()
		if decoder.Decode(&frame, &skipped) != nil || skipped != 0 || before-reader.Len() != frame.Size() {
			return errResponse
		}
		frames++
		duration += frame.Duration().Seconds()
		if duration > float64(maxSeconds) {
			return errResponse
		}
	}
	if frames == 0 {
		return errResponse
	}
	return nil
}
