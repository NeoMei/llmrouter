package nativespeech

import (
	"bytes"
	"encoding/binary"
)

// normalizeBailianWAV handles the exact streaming-size placeholders observed in
// complete Bailian downloads. An exact HTTP length and the canonical 24 kHz,
// mono PCM16 layout are required; all other input still faces strict validation.
// Supplier bytes remain unchanged, and this exception is never used by generic
// compatible audio or another provider.
func normalizeBailianWAV(b []byte, contentLength int64, transferEncoded bool) []byte {
	if len(b) <= 44 || len(b) > 32<<20 || contentLength != int64(len(b)) || transferEncoded || (len(b)-44)%2 != 0 {
		return b
	}
	if string(b[:4]) != "RIFF" || binary.LittleEndian.Uint32(b[4:8]) != 0x7fffffbf || string(b[8:16]) != "WAVEfmt " ||
		binary.LittleEndian.Uint32(b[16:20]) != 16 || binary.LittleEndian.Uint16(b[20:22]) != 1 || binary.LittleEndian.Uint16(b[22:24]) != 1 ||
		binary.LittleEndian.Uint32(b[24:28]) != 24000 || binary.LittleEndian.Uint32(b[28:32]) != 48000 ||
		binary.LittleEndian.Uint16(b[32:34]) != 2 || binary.LittleEndian.Uint16(b[34:36]) != 16 ||
		string(b[36:40]) != "data" || binary.LittleEndian.Uint32(b[40:44]) != 0x7fffff9b {
		return b
	}
	normalized := bytes.Clone(b)
	binary.LittleEndian.PutUint32(normalized[4:8], uint32(len(normalized)-8))
	binary.LittleEndian.PutUint32(normalized[40:44], uint32(len(normalized)-44))
	return normalized
}
