// Package relaymedia publishes authenticated, immutable input snapshots. It is
// not an upload API, transcoder, or proof of decoded video quality.
package relaymedia

import (
	"encoding/binary"
	"errors"
	"sort"
)

const MaxBytes = 50_000_000
const maxSamples = 100_000

var ErrInvalidMP4 = errors.New("unsupported or invalid bounded MP4")

// VideoFacts are verified container/sample metadata, not client declarations.
// This first subset has exactly one video track and known absence of audio.
type VideoFacts struct {
	Format               string
	Bytes                int64
	Width, Height        int
	Seconds              float64
	AudioKnown, HasAudio bool
}
type box struct {
	kind       string
	start, end int
	data       []byte
}

// boxes accepts ordinary 32-bit boxes only. Size-zero/extended/fragmented layouts
// are intentionally unsupported. Slices alias the already bounded input.
func boxes(data []byte, base int) ([]box, error) {
	var out []box
	for p := 0; p < len(data); {
		if len(data)-p < 8 || len(out) >= 128 {
			return nil, ErrInvalidMP4
		}
		n := int(binary.BigEndian.Uint32(data[p:]))
		if n < 8 || n > len(data)-p {
			return nil, ErrInvalidMP4
		}
		out = append(out, box{string(data[p+4 : p+8]), base + p, base + p + n, data[p+8 : p+n]})
		p += n
	}
	return out, nil
}
func children(b box, allowed string) (map[string]box, error) {
	bs, err := boxes(b.data, b.start+8)
	if err != nil {
		return nil, err
	}
	out := map[string]box{}
	for _, v := range bs {
		if !containsBox(allowed, v.kind) {
			return nil, ErrInvalidMP4
		}
		if _, ok := out[v.kind]; ok {
			return nil, ErrInvalidMP4
		}
		out[v.kind] = v
	}
	return out, nil
}
func containsBox(list, kind string) bool {
	for i := 0; i+4 <= len(list); i += 4 {
		if list[i:i+4] == kind {
			return true
		}
	}
	return false
}
func table(b box, stride int) ([]byte, int, error) {
	if len(b.data) < 8 || binary.BigEndian.Uint32(b.data) != 0 {
		return nil, 0, ErrInvalidMP4
	}
	n := int(binary.BigEndian.Uint32(b.data[4:]))
	if n < 1 || n > maxSamples || len(b.data) != 8+n*stride {
		return nil, 0, ErrInvalidMP4
	}
	return b.data[8:], n, nil
}
func u32(b []byte, p int) uint32 { return binary.BigEndian.Uint32(b[p:]) }

// InspectMP4 accepts nonfragmented baseline H.264 avc1 MP4, one video track,
// no edit lists, no audio/encryption/external references, version-0 timing and
// 32-bit chunk offsets. Every sample must be bounded inside mdat, nonoverlapping,
// carry valid length-prefixed AVC NAL framing and contain a video slice.
func InspectMP4(data []byte) (VideoFacts, error) {
	bad := func() (VideoFacts, error) { return VideoFacts{}, ErrInvalidMP4 }
	if len(data) < 32 || len(data) > MaxBytes {
		return bad()
	}
	top, err := boxes(data, 0)
	if err != nil {
		return bad()
	}
	roots := map[string]box{}
	for _, b := range top {
		if b.kind == "free" {
			continue
		}
		if !containsBox("ftypmoovmdat", b.kind) {
			return bad()
		}
		if _, ok := roots[b.kind]; ok {
			return bad()
		}
		roots[b.kind] = b
	}
	ft, moov, mdat := roots["ftyp"], roots["moov"], roots["mdat"]
	if len(ft.data) < 8 || len(ft.data)%4 != 0 || !containsBox("isomiso2mp41mp42avc1", string(ft.data[:4])) || len(mdat.data) == 0 {
		return bad()
	}
	movie, err := children(moov, "mvhdtrakudta")
	if err != nil {
		return bad()
	}
	mv := movie["mvhd"].data
	if len(mv) != 100 || u32(mv, 0) != 0 || u32(mv, 12) == 0 || u32(mv, 16) == 0 {
		return bad()
	}
	track, err := children(movie["trak"], "tkhdmdia")
	if err != nil {
		return bad()
	}
	tk := track["tkhd"].data
	if len(tk) != 84 || tk[0] != 0 || u32(tk, 12) == 0 || u32(tk, 20) != u32(mv, 16) {
		return bad()
	}
	// Reject rotated/scaled tracks; reported pixels are actual sample dimensions.
	matrix := []uint32{65536, 0, 0, 0, 65536, 0, 0, 0, 1073741824}
	for i, v := range matrix {
		if u32(tk, 40+i*4) != v || u32(mv, 36+i*4) != v {
			return bad()
		}
	}
	media, err := children(track["mdia"], "mdhdhdlrminf")
	if err != nil {
		return bad()
	}
	md := media["mdhd"].data
	hd := media["hdlr"].data
	if len(md) != 24 || u32(md, 0) != 0 || u32(md, 12) == 0 || u32(md, 16) == 0 || len(hd) < 24 || u32(hd, 0) != 0 || u32(hd, 4) != 0 || string(hd[8:12]) != "vide" {
		return bad()
	}
	scale, duration := uint64(u32(md, 12)), uint64(u32(md, 16))
	seconds := float64(duration) / float64(scale)
	if seconds <= 0 || seconds > 3600 || duration*uint64(u32(mv, 12)) != uint64(u32(mv, 16))*scale {
		return bad()
	}
	minf, err := children(media["minf"], "vmhddinfstbl")
	if err != nil {
		return bad()
	}
	if len(minf["vmhd"].data) != 12 {
		return bad()
	}
	dinf, err := children(minf["dinf"], "dref")
	if err != nil {
		return bad()
	}
	dr := dinf["dref"]
	if len(dr.data) != 20 || u32(dr.data, 0) != 0 || u32(dr.data, 4) != 1 || u32(dr.data, 8) != 12 || string(dr.data[12:16]) != "url " || u32(dr.data, 16) != 1 {
		return bad()
	}
	st, err := children(minf["stbl"], "stsdsttsstssstscstszstcoctts")
	if err != nil {
		return bad()
	}
	sd := st["stsd"]
	if len(sd.data) < 8 || u32(sd.data, 0) != 0 || u32(sd.data, 4) != 1 {
		return bad()
	}
	entries, err := boxes(sd.data[8:], sd.start+16)
	if err != nil || len(entries) != 1 || entries[0].kind != "avc1" {
		return bad()
	}
	avc := entries[0]
	if len(avc.data) < 78 || binary.BigEndian.Uint16(avc.data[6:]) != 1 {
		return bad()
	}
	width, height := int(binary.BigEndian.Uint16(avc.data[24:])), int(binary.BigEndian.Uint16(avc.data[26:]))
	if width < 1 || height < 1 || width*height > 16*1024*1024 || u32(tk, 76) != uint32(width)<<16 || u32(tk, 80) != uint32(height)<<16 {
		return bad()
	}
	configs, err := boxes(avc.data[78:], avc.start+86)
	if err != nil {
		return bad()
	}
	var cfg []byte
	for _, c := range configs {
		switch c.kind {
		case "avcC":
			if cfg != nil {
				return bad()
			}
			cfg = c.data
		case "pasp":
			if len(c.data) != 8 || u32(c.data, 0) != u32(c.data, 4) || u32(c.data, 0) == 0 {
				return bad()
			}
		case "btrt":
			if len(c.data) != 12 {
				return bad()
			}
		default:
			return bad()
		}
	}
	if !validAVCConfig(cfg, width, height) {
		return bad()
	}
	sz := st["stsz"].data
	if len(sz) < 12 || u32(sz, 0) != 0 {
		return bad()
	}
	n := int(u32(sz, 8))
	if n < 1 || n > maxSamples {
		return bad()
	}
	sizes := make([]uint32, n)
	fixed := u32(sz, 4)
	if fixed == 0 {
		if len(sz) != 12+4*n {
			return bad()
		}
		for i := range sizes {
			sizes[i] = u32(sz, 12+4*i)
		}
	} else {
		if len(sz) != 12 {
			return bad()
		}
		for i := range sizes {
			sizes[i] = fixed
		}
	}
	ts, tn, err := table(st["stts"], 8)
	if err != nil {
		return bad()
	}
	var sampleCount, ticks uint64
	for i := 0; i < tn; i++ {
		count, delta := uint64(u32(ts, i*8)), uint64(u32(ts, i*8+4))
		if count == 0 || delta == 0 || count > uint64(n)-min(sampleCount, uint64(n)) {
			return bad()
		}
		sampleCount += count
		ticks += count * delta
	}
	if sampleCount != uint64(n) || ticks != duration {
		return bad()
	}
	if c, ok := st["ctts"]; ok {
		ct, cn, e := table(c, 8)
		if e != nil {
			return bad()
		}
		var count uint64
		for i := 0; i < cn; i++ {
			v := uint64(u32(ct, i*8))
			if v == 0 || v > uint64(n) || u32(ct, i*8+4) != 0 {
				return bad()
			}
			count += v
		}
		if count != uint64(n) {
			return bad()
		}
	}
	if c, ok := st["stss"]; ok {
		ss, sn, e := table(c, 4)
		if e != nil {
			return bad()
		}
		var prev uint32
		for i := 0; i < sn; i++ {
			v := u32(ss, i*4)
			if v <= prev || v > uint32(n) {
				return bad()
			}
			prev = v
		}
	}
	co, cn, err := table(st["stco"], 4)
	if err != nil {
		return bad()
	}
	sc, sn, err := table(st["stsc"], 12)
	if err != nil {
		return bad()
	}
	for i := 0; i < sn; i++ {
		first, per, desc := u32(sc, i*12), u32(sc, i*12+4), u32(sc, i*12+8)
		if first == 0 || first > uint32(cn) || per == 0 || per > uint32(n) || desc != 1 || (i == 0 && first != 1) || (i > 0 && first <= u32(sc, (i-1)*12)) {
			return bad()
		}
	}
	type extent struct{ start, end uint64 }
	extents := make([]extent, 0, n)
	sample, run := 0, 0
	for i := 0; i < cn; i++ {
		for run+1 < sn && u32(sc, (run+1)*12) <= uint32(i+1) {
			run++
		}
		pos := uint64(u32(co, i*4))
		per := int(u32(sc, run*12+4))
		if per > n-sample {
			return bad()
		}
		for j := 0; j < per; j++ {
			size := uint64(sizes[sample])
			end := pos + size
			if size == 0 || pos < uint64(mdat.start+8) || end > uint64(mdat.end) {
				return bad()
			}
			extents = append(extents, extent{pos, end})
			pos = end
			sample++
		}
	}
	if sample != n {
		return bad()
	}
	sort.Slice(extents, func(i, j int) bool { return extents[i].start < extents[j].start })
	for i := 1; i < len(extents); i++ {
		if extents[i].start < extents[i-1].end {
			return bad()
		}
	}
	// Establish disjoint extents before touching codec payloads. Even invalid
	// files can now cause at most one AVC inspection per stored payload byte.
	for _, sample := range extents {
		if !validAVCSample(data[int(sample.start):int(sample.end)], width, height) {
			return bad()
		}
	}
	return VideoFacts{Format: "mp4", Bytes: int64(len(data)), Width: width, Height: height, Seconds: seconds, AudioKnown: true}, nil
}

func validAVCSample(b []byte, width, height int) bool {
	slices := false
	for count := 0; len(b) > 0; count++ {
		if count >= 4096 || len(b) < 5 {
			return false
		}
		n := int(u32(b, 0))
		b = b[4:]
		if n < 2 || n > len(b) || b[0]&0x80 != 0 {
			return false
		}
		typ := b[0] & 31
		if typ == 1 || typ == 5 {
			rbsp, ok := avcRBSP(b[:min(n, 128)])
			if !ok {
				return false
			}
			bits := bitReader{data: rbsp}
			first, sliceType, pps := bits.ue(), bits.ue(), bits.ue()
			if bits.failed || first >= uint32(((width+15)/16)*((height+15)/16)) || sliceType > 9 || sliceType%5 > 2 || sliceType%5 == 1 || pps != 0 {
				return false
			}
			slices = true
		} else if typ != 6 && typ != 9 {
			return false
		}
		b = b[n:]
	}
	return slices
}
func validAVCConfig(b []byte, width, height int) bool {
	if len(b) < 8 || b[0] != 1 || b[1] != 66 || b[4] != 255 || b[5] != 225 {
		return false
	}
	n := int(binary.BigEndian.Uint16(b[6:]))
	if n < 4 || 8+n+3 > len(b) || b[8] != 0x67 {
		return false
	}
	sps := b[8 : 8+n]
	if sps[1] != b[1] || sps[2] != b[2] || sps[3] != b[3] || !validBaselineSPS(sps, width, height) {
		return false
	}
	p := 8 + n
	if b[p] != 1 {
		return false
	}
	n = int(binary.BigEndian.Uint16(b[p+1:]))
	return n >= 2 && p+3+n == len(b) && b[p+3] == 0x68 && validBaselinePPS(b[p+3:])
}
