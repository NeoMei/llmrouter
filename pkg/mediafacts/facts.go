package mediafacts

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/url"
	"strings"
)

// MediaFact contains only facts decoded from bounded inline bytes. Empty Format
// means a remote or unsupported carrier whose facts have not been verified.
// No request-supplied dimension, duration, MIME or URL suffix is authoritative.
type MediaFact struct {
	Modality, Format string
	Pixels           int64
	Width, Height    int
	Pages            int
	EncodedBytes     int
	Seconds          float64
}

const MaxInlineMediaBytes = 10 * 1024 * 1024

func DecodeMediaFact(modality, carrier string) (MediaFact, error) {
	fact := MediaFact{Modality: modality}
	if strings.HasPrefix(carrier, "https://") || strings.HasPrefix(carrier, "http://") {
		u, err := url.ParseRequestURI(carrier)
		if err != nil || u.Host == "" {
			return fact, fmt.Errorf("invalid media URL")
		}
		return fact, nil
	}
	encoded := carrier
	if strings.HasPrefix(carrier, "data:") {
		head, data, ok := strings.Cut(carrier, ",")
		if !ok || !strings.HasSuffix(head, ";base64") {
			return fact, fmt.Errorf("media requires base64 data URI")
		}
		encoded = data
	}
	fact.EncodedBytes = len(encoded)
	if len(encoded) == 0 || len(encoded) > base64.StdEncoding.EncodedLen(MaxInlineMediaBytes) {
		return fact, fmt.Errorf("inline media exceeds bounded decoder or is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fact, fmt.Errorf("invalid media base64")
	}
	if len(raw) > MaxInlineMediaBytes {
		return fact, fmt.Errorf("inline media exceeds bounded decoder")
	}
	switch modality {
	case "image":
		if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) && !bytes.HasPrefix(raw, []byte("\xff\xd8")) {
			return fact, nil
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || (format != "png" && format != "jpeg") || config.Width <= 0 || config.Height <= 0 {
			return fact, fmt.Errorf("invalid PNG/JPEG image")
		}
		// DecodeConfig never allocates the pixel buffer. Bound arithmetic separately.
		if int64(config.Width) > math.MaxInt64/int64(config.Height) {
			return fact, fmt.Errorf("image dimension overflow")
		}
		fact.Format = format
		fact.Width = config.Width
		fact.Height = config.Height
		fact.Pixels = int64(config.Width) * int64(config.Height)
		if fact.Pixels > 16*1024*1024 {
			return fact, fmt.Errorf("inline image exceeds bounded pixel decoder")
		}
		if _, _, err := image.Decode(bytes.NewReader(raw)); err != nil {
			return fact, fmt.Errorf("invalid image bytes")
		}
	case "audio":
		// Only uncompressed integer PCM WAV is certified. Unknown codecs retain no
		// verified facts and cannot satisfy strict configured limits.
		if len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
			return fact, nil
		}
		if uint64(binary.LittleEndian.Uint32(raw[4:8]))+8 != uint64(len(raw)) {
			return fact, fmt.Errorf("invalid WAV length")
		}
		var rate uint32
		var align uint16
		var dataBytes uint64
		var haveFormat, haveData bool
		for offset := 12; offset < len(raw); {
			if len(raw)-offset < 8 {
				return fact, fmt.Errorf("truncated WAV chunk")
			}
			size := uint64(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
			start := offset + 8
			if size > uint64(len(raw)-start) {
				return fact, fmt.Errorf("truncated WAV data")
			}
			chunk := raw[start : start+int(size)]
			switch string(raw[offset : offset+4]) {
			case "fmt ":
				if haveFormat || len(chunk) < 16 {
					return fact, fmt.Errorf("invalid WAV format chunk")
				}
				if binary.LittleEndian.Uint16(chunk) != 1 {
					return fact, nil
				}
				channels := binary.LittleEndian.Uint16(chunk[2:])
				rate = binary.LittleEndian.Uint32(chunk[4:])
				byteRate := binary.LittleEndian.Uint32(chunk[8:])
				align = binary.LittleEndian.Uint16(chunk[12:])
				bits := binary.LittleEndian.Uint16(chunk[14:])
				if channels == 0 || rate == 0 || bits == 0 || bits%8 != 0 || uint64(align) != uint64(channels)*uint64(bits/8) || uint64(byteRate) != uint64(rate)*uint64(align) {
					return fact, fmt.Errorf("inconsistent WAV format")
				}
				haveFormat = true
			case "data":
				dataBytes += size
				haveData = true
			}
			offset = start + int(size) + int(size%2)
			if offset > len(raw) {
				return fact, fmt.Errorf("missing WAV padding")
			}
		}
		if !haveFormat || !haveData || dataBytes == 0 || dataBytes%uint64(align) != 0 {
			return fact, fmt.Errorf("invalid WAV samples")
		}
		fact.Format = "wav"
		fact.Seconds = float64(dataBytes/uint64(align)) / float64(rate)
	}
	return fact, nil
}

// AllowsFormat retains exact format declarations and recognizes only the two
// PNG/JPEG codec names emitted by our image decoder and their canonical MIME.
// It does not infer aliases, extensions, casing or provider-specific codecs.
func AllowsFormat(declared []string, actual string) bool {
	for _, format := range declared {
		if format == actual ||
			((format == "png" || format == "image/png") && (actual == "png" || actual == "image/png")) ||
			((format == "jpeg" || format == "image/jpeg") && (actual == "jpeg" || actual == "image/jpeg")) {
			return true
		}
	}
	return false
}
