package gallery

import (
	"image"
	"image/draw"
)

// areaResize scales img down to w x h by averaging the source pixels each
// target pixel covers (box filter with exact fractional coverage). It reads
// the source one row at a time and keeps two rows of sums, so the memory
// beyond the result is a few rows, whatever the size of the photo. The
// kernel scalers of x/image/draw keep target width x source height x 32
// bytes instead: about 200 MB for a 12 MP photo, too much for a small
// machine. Averaging is also the right filter for shrinking.
func areaResize(img image.Image, w, h int) *image.RGBA {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if sw <= 0 || sh <= 0 || w <= 0 || h <= 0 {
		return dst
	}
	hx := spans(sw, w)
	fy := float64(sh) / float64(h)

	row := image.NewRGBA(image.Rect(0, 0, sw, 1)) // one source row as RGBA (premultiplied)
	hrow := make([]float32, w*4)                  // that row, scaled horizontally
	acc := make([]float32, w*4)                   // sums for the current target row
	next := make([]float32, w*4)                  // sums spilling into the next target row
	norm := float32(1 / (float64(sw) / float64(w) * fy))

	j := 0 // target row being filled
	for y := 0; y < sh && j < h; y++ {
		draw.Draw(row, row.Rect, img, image.Pt(b.Min.X, b.Min.Y+y), draw.Src)
		hx.apply(row.Pix, hrow)

		// the source row covers [y, y+1); target row j covers [j*fy, (j+1)*fy)
		end := float64(j+1) * fy
		if float64(y+1) <= end+1e-9 {
			addScaled(acc, hrow, 1)
		} else {
			part := float32(end - float64(y))
			addScaled(acc, hrow, part)
			addScaled(next, hrow, 1-part)
		}
		if float64(y+1) >= end-1e-9 {
			writeRow(dst, j, acc, norm)
			acc, next = next, acc
			clear(next)
			j++
			// a very thin target row (fy < 1 never happens: no upscaling) is done
		}
	}
	for ; j < h; j++ { // rounding left the last row open
		writeRow(dst, j, acc, norm)
	}
	return dst
}

// hspans holds, per target column, the source columns it covers and their
// weights.
type hspans struct {
	start []int32
	n     []int32
	w     []float32 // flattened weights
	off   []int32
}

func spans(sw, w int) hspans {
	f := float64(sw) / float64(w)
	s := hspans{start: make([]int32, w), n: make([]int32, w), off: make([]int32, w)}
	for x := 0; x < w; x++ {
		lo, hi := float64(x)*f, float64(x+1)*f
		if x == w-1 {
			hi = float64(sw)
		}
		i0 := int(lo)
		s.start[x] = int32(i0)
		s.off[x] = int32(len(s.w))
		for i := i0; i < sw && float64(i) < hi-1e-9; i++ {
			a, z := max(lo, float64(i)), min(hi, float64(i+1))
			if z > a {
				s.w = append(s.w, float32(z-a))
				s.n[x]++
			}
		}
	}
	return s
}

func (s hspans) apply(pix []byte, out []float32) {
	for x := range s.start {
		var r, g, b, a float32
		p := int(s.start[x]) * 4
		ws := s.w[s.off[x] : s.off[x]+s.n[x]]
		for _, wt := range ws {
			r += wt * float32(pix[p])
			g += wt * float32(pix[p+1])
			b += wt * float32(pix[p+2])
			a += wt * float32(pix[p+3])
			p += 4
		}
		o := out[x*4 : x*4+4 : x*4+4]
		o[0], o[1], o[2], o[3] = r, g, b, a
	}
}

func addScaled(dst, src []float32, k float32) {
	for i, v := range src {
		dst[i] += k * v
	}
}

func writeRow(dst *image.RGBA, y int, sum []float32, norm float32) {
	p := dst.Pix[y*dst.Stride : y*dst.Stride+len(sum)]
	for i, v := range sum {
		v = v*norm + 0.5
		if v > 255 {
			v = 255
		}
		p[i] = uint8(v)
	}
}
