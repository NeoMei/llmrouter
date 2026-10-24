package relaymedia

// Small bounded SPS reader verifies baseline 4:2:0 progressive dimensions. It
// does not decode video slices or certify playback/visual quality.
type bitReader struct {
	data   []byte
	pos    int
	failed bool
}

func (r *bitReader) bits(n int) uint32 {
	if n < 0 || n > 32 || r.pos+n > len(r.data)*8 {
		r.failed = true
		return 0
	}
	var v uint32
	for i := 0; i < n; i++ {
		v = v<<1 | uint32((r.data[r.pos/8]>>uint(7-r.pos%8))&1)
		r.pos++
	}
	return v
}
func (r *bitReader) ue() uint32 {
	z := 0
	for !r.failed && r.bits(1) == 0 {
		z++
		if z > 24 {
			r.failed = true
			return 0
		}
	}
	return (1 << uint(z)) - 1 + r.bits(z)
}
func validBaselineSPS(sps []byte, width, height int) bool {
	rbsp, ok := avcRBSP(sps)
	if !ok {
		return false
	}
	r := bitReader{data: rbsp}
	if r.bits(8) != 66 {
		return false
	}
	r.bits(8)
	level := r.bits(8)
	if level == 0 || level > 62 {
		return false
	}
	if r.ue() != 0 || r.ue() > 12 {
		return false
	}
	poc := r.ue()
	switch poc {
	case 0:
		if r.ue() > 12 {
			return false
		}
	case 2:
	default:
		return false
	}
	if r.ue() > 16 {
		return false
	}
	r.bits(1)
	w, h := r.ue(), r.ue()
	if w > 4095 || h > 4095 || uint64(w+1)*uint64(h+1)*256 > 16*1024*1024 || r.bits(1) != 1 {
		return false
	}
	r.bits(1)
	var left, right, top, bottom uint32
	if r.bits(1) == 1 {
		left = r.ue()
		right = r.ue()
		top = r.ue()
		bottom = r.ue()
	}
	if r.failed {
		return false
	}
	x, y := int64(w+1)*16-int64(left+right)*2, int64(h+1)*16-int64(top+bottom)*2
	if x != int64(width) || y != int64(height) {
		return false
	}
	if r.bits(1) == 1 && !validVUI(&r) {
		return false
	}
	return r.trailing()
}

func (r *bitReader) se() int64 {
	v := r.ue()
	if v&1 != 0 {
		return int64(v+1) / 2
	}
	return -int64(v) / 2
}
func (r *bitReader) trailing() bool {
	if r.failed || r.bits(1) != 1 {
		return false
	}
	for r.pos%8 != 0 {
		if r.bits(1) != 0 {
			return false
		}
	}
	return !r.failed && r.pos == len(r.data)*8
}
func avcRBSP(nal []byte) ([]byte, bool) {
	if len(nal) < 2 || len(nal) > 4096 {
		return nil, false
	}
	out := make([]byte, 0, len(nal))
	zeros := 0
	for i := 1; i < len(nal); i++ {
		b := nal[i]
		if zeros >= 2 {
			if b == 3 {
				if i+1 >= len(nal) || nal[i+1] > 3 {
					return nil, false
				}
				zeros = 0
				continue
			}
			if b < 3 {
				return nil, false
			}
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out, true
}
func validBaselinePPS(nal []byte) bool {
	data, ok := avcRBSP(nal)
	if !ok {
		return false
	}
	r := bitReader{data: data}
	if r.ue() != 0 || r.ue() != 0 || r.bits(1) != 0 || r.bits(1) != 0 || r.ue() != 0 || r.ue() > 31 || r.ue() > 31 {
		return false
	}
	r.bits(1)
	if r.bits(2) > 2 {
		return false
	}
	qp, qs, chroma := r.se(), r.se(), r.se()
	if qp < -26 || qp > 25 || qs < -26 || qs > 25 || chroma < -12 || chroma > 12 {
		return false
	}
	r.bits(1)
	r.bits(1)
	if r.bits(1) != 0 {
		return false
	}
	return r.trailing()
}
func validVUI(r *bitReader) bool {
	if r.bits(1) == 1 {
		aspect := r.bits(8)
		if aspect == 255 {
			w, h := r.bits(16), r.bits(16)
			if w == 0 || w != h {
				return false
			}
		} else if aspect != 1 {
			return false
		}
	}
	if r.bits(1) == 1 {
		r.bits(1)
	}
	if r.bits(1) == 1 {
		if r.bits(3) > 5 {
			return false
		}
		r.bits(1)
		if r.bits(1) == 1 {
			r.bits(8)
			r.bits(8)
			r.bits(8)
		}
	}
	if r.bits(1) == 1 {
		if r.ue() > 5 || r.ue() > 5 {
			return false
		}
	}
	if r.bits(1) == 1 {
		if r.bits(32) == 0 || r.bits(32) == 0 {
			return false
		}
		r.bits(1)
	}
	// HRD timing models are outside this deliberately small subset.
	if r.bits(1) != 0 || r.bits(1) != 0 {
		return false
	}
	r.bits(1)
	if r.bits(1) == 1 {
		r.bits(1)
		for i := 0; i < 6; i++ {
			if r.ue() > 16 {
				return false
			}
		}
	}
	return !r.failed
}
