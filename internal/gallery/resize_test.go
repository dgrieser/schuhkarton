package gallery

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"runtime"
	"testing"

	xdraw "golang.org/x/image/draw"
)

func TestAreaResizeSolidAndSizes(t *testing.T) {
	for _, c := range []struct{ sw, sh, w, h int }{
		{4000, 3000, 2048, 1536}, {3000, 4000, 360, 480}, {101, 37, 50, 18}, {3, 3, 1, 1}, {640, 480, 639, 479}, {7, 5, 7, 5},
	} {
		src := image.NewYCbCr(image.Rect(0, 0, c.sw, c.sh), image.YCbCrSubsampleRatio420)
		for i := range src.Y {
			src.Y[i] = 120
		}
		for i := range src.Cb {
			src.Cb[i], src.Cr[i] = 100, 160
		}
		want := color.RGBAModel.Convert(src.At(0, 0)).(color.RGBA)
		dst := areaResize(src, c.w, c.h)
		if dst.Bounds().Dx() != c.w || dst.Bounds().Dy() != c.h {
			t.Fatalf("%v: size %v", c, dst.Bounds())
		}
		for y := 0; y < c.h; y++ {
			for x := 0; x < c.w; x++ {
				if got := dst.RGBAAt(x, y); absDiff(got.R, want.R) > 1 || absDiff(got.G, want.G) > 1 || absDiff(got.B, want.B) > 1 || got.A != 255 {
					t.Fatalf("%v: pixel %d,%d = %v, want %v", c, x, y, got, want)
				}
			}
		}
	}
}

// Close to a good kernel scaler on a photo-like image.
func TestAreaResizeQuality(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1200, 900))
	r := rand.New(rand.NewSource(1))
	for y := 0; y < 900; y++ {
		for x := 0; x < 1200; x++ {
			v := 128 + 100*math.Sin(float64(x)/37)*math.Cos(float64(y)/23)
			n := r.Intn(9) - 4
			src.SetRGBA(x, y, color.RGBA{uint8(v) + uint8(n), uint8(255 - v), uint8(x * 255 / 1200), 255})
		}
	}
	got := areaResize(src, 400, 300)
	ref := image.NewRGBA(image.Rect(0, 0, 400, 300))
	xdraw.CatmullRom.Scale(ref, ref.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	var se float64
	for i := range got.Pix {
		d := float64(got.Pix[i]) - float64(ref.Pix[i])
		se += d * d
	}
	psnr := 10 * math.Log10(255*255/(se/float64(len(got.Pix))))
	if psnr < 35 {
		t.Errorf("PSNR against CatmullRom %.1f dB", psnr)
	}
}

func TestAreaResizeAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		src.SetNRGBA(x, 0, color.NRGBA{255, 0, 0, 255})
		src.SetNRGBA(x, 1, color.NRGBA{0, 0, 0, 0})
	}
	got := areaResize(src, 2, 1).RGBAAt(0, 0)
	if absDiff(got.A, 128) > 1 || absDiff(got.R, 128) > 1 {
		t.Errorf("half transparent red: %v", got)
	}
}

func TestAreaResizeMemory(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	src := image.NewYCbCr(image.Rect(0, 0, 3000, 4000), image.YCbCrSubsampleRatio420)
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	areaResize(src, 1536, 2048)
	runtime.ReadMemStats(&b)
	result := uint64(1536 * 2048 * 4)
	if extra := b.TotalAlloc - a.TotalAlloc - result; extra > 1<<20 {
		t.Errorf("allocated %d KB beyond the result", extra>>10)
	}
}

func absDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}

// Growing (a small face crop enlarged to 200 px) keeps the picture: before,
// the averaging scaler, which only shrinks, garbled it.
func TestAreaResizeGrows(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 60, 40))
	for y := range 40 {
		for x := range 60 {
			src.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 6), 100, 255})
		}
	}
	dst := areaResize(src, 150, 100)
	for _, p := range []image.Point{{10, 10}, {75, 50}, {140, 90}} {
		want := src.RGBAAt(p.X*60/150, p.Y*40/100)
		got := dst.RGBAAt(p.X, p.Y)
		if d := int(absDiff(got.R, want.R)) + int(absDiff(got.G, want.G)) + int(absDiff(got.B, want.B)); d > 30 {
			t.Errorf("at %v: %v, want about %v", p, got, want)
		}
	}
}
