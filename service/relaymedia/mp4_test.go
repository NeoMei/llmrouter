package relaymedia

import (
	"bytes"
	"encoding/binary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestInspectMP4(t *testing.T) {
	data, err := os.ReadFile("testdata/h264.mp4")
	require.NoError(t, err)
	facts, err := InspectMP4(data)
	require.NoError(t, err)
	assert.Equal(t, 64, facts.Width)
	assert.Equal(t, 48, facts.Height)
	assert.Equal(t, 1.0, facts.Seconds)
	assert.True(t, facts.AudioKnown)
	assert.False(t, facts.HasAudio)
	for _, typ := range []string{"stco", "stsz", "stsc", "stts", "avcC", "hdlr"} {
		t.Run(typ, func(t *testing.T) {
			bad := bytes.Clone(data)
			i := bytes.Index(bad, []byte(typ))
			require.Greater(t, i, 0)
			binary.BigEndian.PutUint32(bad[i+8:i+12], 0xffffffff)
			_, err := InspectMP4(bad)
			require.Error(t, err)
		})
	}
	for _, bad := range [][]byte{nil, data[:len(data)-1], append(bytes.Clone(data), 0), []byte("not video")} {
		_, err := InspectMP4(bad)
		require.Error(t, err)
	}
}

func TestInspectMP4StructuralNegatives(t *testing.T) {
	data, err := os.ReadFile("testdata/h264.mp4")
	require.NoError(t, err)
	cases := []struct {
		name string
		edit func([]byte) []byte
	}{
		{"movie-transform", func(b []byte) []byte {
			i := bytes.Index(b, []byte("mvhd"))
			binary.BigEndian.PutUint32(b[i+40:], 32768)
			return b
		}},
		{"sample-outside-mdat", func(b []byte) []byte {
			i := bytes.Index(b, []byte("stco"))
			binary.BigEndian.PutUint32(b[i+12:], 1)
			return b
		}},
		{"zero-sample", func(b []byte) []byte {
			i := bytes.Index(b, []byte("stsz"))
			binary.BigEndian.PutUint32(b[i+16:], 0)
			return b
		}},
		{"sample-crosses-file", func(b []byte) []byte {
			i := bytes.Index(b, []byte("stsz"))
			binary.BigEndian.PutUint32(b[i+16:], MaxBytes)
			return b
		}},
		{"stsc-cardinality", func(b []byte) []byte {
			i := bytes.Index(b, []byte("stsc"))
			binary.BigEndian.PutUint32(b[i+16:], 1)
			return b
		}},
		{"duration-mismatch", func(b []byte) []byte {
			i := bytes.Index(b, []byte("stts"))
			binary.BigEndian.PutUint32(b[i+16:], 1)
			return b
		}},
		{"zero-timescale", func(b []byte) []byte {
			i := bytes.Index(b, []byte("mdhd"))
			binary.BigEndian.PutUint32(b[i+16:], 0)
			return b
		}},
		{"audio-track", func(b []byte) []byte { i := bytes.Index(b, []byte("hdlr")); copy(b[i+12:], "soun"); return b }},
		{"encrypted-codec", func(b []byte) []byte { i := bytes.Index(b, []byte("stsd")); copy(b[i+16:], "encv"); return b }},
		{"external-reference", func(b []byte) []byte {
			i := bytes.Index(b, []byte("url "))
			binary.BigEndian.PutUint32(b[i+4:], 0)
			return b
		}},
		{"NAL-overrun", func(b []byte) []byte {
			i := bytes.Index(b, []byte("stco"))
			offset := binary.BigEndian.Uint32(b[i+12:])
			binary.BigEndian.PutUint32(b[offset:], MaxBytes)
			return b
		}},
		{"dimensions-disagree-with-SPS", func(b []byte) []byte {
			i := bytes.Index(b, []byte("tkhd"))
			binary.BigEndian.PutUint32(b[i+80:], 32<<16)
			i = bytes.Index(b, []byte("stsd"))
			binary.BigEndian.PutUint16(b[i+44:], 32)
			return b
		}},
		{"fragment", func(b []byte) []byte { return append(b, 0, 0, 0, 8, 'm', 'o', 'o', 'f') }},
		{"duplicate-track", func(b []byte) []byte {
			i := bytes.Index(b, []byte("trak")) - 4
			n := int(binary.BigEndian.Uint32(b[i:]))
			dup := bytes.Clone(b[i : i+n])
			out := append(bytes.Clone(b[:i]), dup...)
			out = append(out, b[i:]...)
			m := bytes.Index(out, []byte("moov")) - 4
			binary.BigEndian.PutUint32(out[m:], binary.BigEndian.Uint32(b[m:])+uint32(n))
			return out
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { _, err := InspectMP4(tc.edit(bytes.Clone(data))); require.Error(t, err) })
	}
}
