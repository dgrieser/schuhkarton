package gallery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/gen2brain/avif" // pure Go (WebAssembly), no cgo
	_ "github.com/gen2brain/heic"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/dgrieser/schuhkarton/internal/helper"
)

// Variants of cached derivatives.
const (
	VarSmall   = "s" // grid thumbnail (jpg)
	VarLarge   = "m" // lightbox preview (jpg)
	VarPreview = "p" // animated video preview (mp4)
	VarH264    = "h" // browser-compatible video (mp4)
)

// thumbnail sizes from the options
func (l *Library) smallShort() int { return l.opt.ThumbSize }
func (l *Library) smallLong() int  { return l.opt.ThumbSize * 10 / 3 }
func (l *Library) largeLong() int  { return l.opt.PreviewSize }

// mediaGen produces and caches thumbnails, previews and transcodes.
type mediaGen struct {
	l      *Library
	flight sync.Map // key -> *call
	wake   chan struct{}
	ffmpeg string
}

type call struct {
	done chan struct{}
	err  error
}

func newMediaGen(l *Library) *mediaGen {
	return &mediaGen{l: l, wake: make(chan struct{}, 1), ffmpeg: FindFFmpeg(l.opt.FFmpeg)}
}

// FindFFmpeg returns a usable ffmpeg or "".
func FindFFmpeg(hint string) string {
	for _, c := range []string{hint, "ffmpeg"} {
		if c == "" {
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// needsOrient: HEIF and AVIF carry their rotation in the container, which
// the decoder applies; EXIF orientation must not be applied again.
func needsOrient(ext string) bool {
	switch ext {
	case ".heic", ".heif", ".avif":
		return false
	}
	return true
}

// CachePath returns where a derivative of an item is stored.
func (l *Library) CachePath(it *Item, variant string) string {
	ext := ".jpg"
	if variant == VarPreview || variant == VarH264 {
		ext = ".mp4"
	}
	return filepath.Join(l.opt.CacheDir, "thumbs", it.key[:2], it.key+"-"+variant+ext)
}

// FacePath returns the cached crop of one face.
func (l *Library) FacePath(it *Item, idx int) string {
	return filepath.Join(l.opt.CacheDir, "faces", it.key[:2], fmt.Sprintf("%s_%d.jpg", it.key, idx))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Ensure makes sure a derivative exists and returns its path. Concurrent
// requests for the same file share one generation.
func (l *Library) Ensure(ctx context.Context, it *Item, variant string) (string, error) {
	p := l.CachePath(it, variant)
	if exists(p) {
		return p, nil
	}
	fk := it.key + variant
	c := &call{done: make(chan struct{})}
	if prev, loaded := l.media.flight.LoadOrStore(fk, c); loaded {
		pc := prev.(*call)
		select {
		case <-pc.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if pc.err != nil {
			return "", pc.err
		}
		return p, nil
	}
	defer func() {
		close(c.done)
		l.media.flight.Delete(fk)
	}()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		c.err = err
		return "", err
	}
	switch {
	case it.Kind == Photo && (variant == VarSmall || variant == VarLarge):
		c.err = l.makePhotoThumbs(it, variant)
	case it.Kind == Video && variant == VarSmall:
		c.err = l.makePoster(it)
	case it.Kind == Video && variant == VarLarge:
		c.err = l.makePosterLarge(it)
	case it.Kind == Video && variant == VarPreview && l.opt.VideoPreviews:
		c.err = l.makeVideoPreview(it)
	default:
		c.err = errors.New("unknown variant")
	}
	if c.err == nil && !exists(p) {
		c.err = errors.New("not generated")
	}
	if c.err != nil {
		l.cnt.errors.Add(1)
		return "", c.err
	}
	return p, nil
}

// --- photos -------------------------------------------------------------------

func (l *Library) makePhotoThumbs(it *Item, want string) error {
	src := l.Path(it)
	ext := it.Ext()
	sp, lp := l.CachePath(it, VarSmall), l.CachePath(it, VarLarge)
	both := want == VarLarge || l.opt.Previews
	q := l.opt.Quality
	// Go decodes the common formats itself, HEIC and AVIF included
	var img image.Image
	var err error
	if rawExt[ext] {
		var b []byte
		if b, err = extractPreview(l.opt.ExifTool, src); err == nil {
			img, _, err = image.Decode(bytes.NewReader(b))
		}
	} else {
		img, err = decodeFile(src)
	}
	if err == nil {
		o := l.displayOrient(it)
		// scaling is rotation-invariant here, so scale first and rotate the small result
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		if o >= 5 && o <= 8 {
			w, h = h, w
		}
		l.setAspect(it, w, h)
		if both {
			large := resizeFit(img, l.largeLong(), l.largeLong())
			if err := writeJPEG(lp, orient(large, o), min(q+4, 95)); err != nil {
				return err
			}
			img = large
		}
		if !exists(sp) {
			return writeJPEG(sp, orient(resizeShort(img, l.smallShort(), l.smallLong()), o), q)
		}
		return nil
	}
	if l.local != nil {
		outs := []helper.ThumbSpec{{Path: sp, Short: l.smallShort(), MaxLong: l.smallLong(), Q: q}}
		if both {
			outs = append(outs, helper.ThumbSpec{Path: lp, Long: l.largeLong(), Q: min(q+4, 95)})
		}
		w, h, herr := l.local.Thumb(src, outs)
		if herr == nil {
			if it.Rot == 90 || it.Rot == 270 {
				w, h = h, w
			}
			l.setAspect(it, w, h)
			return l.turnOutputs(it, sp, lp)
		}
		l.logf("helper %s: %v", it.Rel, herr)
	}
	if err := l.ffmpegImage(src, sp, lp, both); err != nil {
		return err
	}
	return l.turnOutputs(it, sp, lp)
}

// turnOutputs applies an admin rotation to thumbnails made by the helper or
// ffmpeg (they only know the file's own orientation).
func (l *Library) turnOutputs(it *Item, paths ...string) error {
	if it.Rot == 0 {
		return nil
	}
	for _, p := range paths {
		if exists(p) {
			if err := rotateJPEGFile(p, p, it.Rot); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Library) setAspect(it *Item, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	a := float64(w) / float64(h)
	if d := a - it.Aspect; d > 0.01 || d < -0.01 {
		it.Aspect = a
		if it.W == 0 || (it.W > it.H) != (w > h) {
			it.W, it.H = w, h
		}
		l.markDirty()
	}
}

func (l *Library) ffmpegImage(src, sp, lp string, both bool) error {
	if l.media.ffmpeg == "" {
		return errors.New("no decoder")
	}
	targets := []struct {
		path, vf string
	}{{sp, scaleShort(l.smallShort())}}
	if both {
		targets = append(targets, struct{ path, vf string }{lp, scaleLong(l.largeLong())})
	}
	for _, t := range targets {
		cmd := exec.Command(l.media.ffmpeg, "-v", "error", "-y", "-i", src, "-frames:v", "1", "-vf", t.vf, "-q:v", "3", t.path+".tmp.jpg")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg: %v %s", err, firstLine(out))
		}
		if err := os.Rename(t.path+".tmp.jpg", t.path); err != nil {
			return err
		}
	}
	return nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s
}

func decodeFile(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func writeJPEG(p string, img image.Image, q int) error {
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: q}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// resizeFit scales so that the image fits into w x h (never upscales).
func resizeFit(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	s := min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	return scale(img, s)
}

// resizeShort scales the short edge to short, but the long edge to at most maxLong.
func resizeShort(img image.Image, short, maxLong int) image.Image {
	b := img.Bounds()
	s := float64(short) / float64(min(b.Dx(), b.Dy()))
	s = min(s, float64(maxLong)/float64(max(b.Dx(), b.Dy())))
	return scale(img, s)
}

func scale(img image.Image, s float64) image.Image {
	b := img.Bounds()
	if s >= 1 {
		return img
	}
	w, h := max(1, int(float64(b.Dx())*s+0.5)), max(1, int(float64(b.Dy())*s+0.5))
	// fast pre-shrink, then a good filter for the last step
	if b.Dx() > w*3 {
		mid := image.NewRGBA(image.Rect(0, 0, w*2, h*2))
		draw.ApproxBiLinear.Scale(mid, mid.Bounds(), img, b, draw.Src, nil)
		img, b = mid, mid.Bounds()
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// orient applies an EXIF orientation (1..8).
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	src, ok := img.(*image.RGBA)
	if !ok {
		b := img.Bounds()
		src = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		row := src.Pix[(y)*src.Stride:]
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			copy(dst.Pix[dy*dst.Stride+dx*4:dy*dst.Stride+dx*4+4], row[x*4:x*4+4])
		}
	}
	return dst
}

// --- videos -------------------------------------------------------------------

func scaleShort(n int) string {
	return fmt.Sprintf("scale='if(gt(iw,ih),-2,%d)':'if(gt(iw,ih),%d,-2)'", n, n)
}

func scaleLong(n int) string {
	return fmt.Sprintf("scale='if(gt(iw,ih),min(%d,iw),-2)':'if(gt(iw,ih),-2,min(%d,ih))'", n, n)
}

func (l *Library) posterAt(it *Item) float64 {
	switch {
	case it.Duration <= 0:
		return 0
	case it.Duration < 4:
		return it.Duration * 0.3
	default:
		return min(it.Duration*0.12, 8)
	}
}

func (l *Library) runFFmpeg(args ...string) error {
	if l.media.ffmpeg == "" {
		return errors.New("ffmpeg not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.media.ffmpeg, append([]string{"-v", "error", "-y", "-nostdin"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v %s", err, firstLine(out))
	}
	return nil
}

func (l *Library) makePoster(it *Item) error {
	p := l.CachePath(it, VarSmall)
	err := l.runFFmpeg("-ss", fmt.Sprintf("%.2f", l.posterAt(it)), "-i", l.Path(it), "-frames:v", "1",
		"-vf", scaleShort(l.smallShort()), "-q:v", "4", p+".tmp.jpg")
	if err != nil {
		// some files cannot seek: take the first frame
		err = l.runFFmpeg("-i", l.Path(it), "-frames:v", "1", "-vf", scaleShort(l.smallShort()), "-q:v", "4", p+".tmp.jpg")
	}
	if err != nil {
		return err
	}
	if img, err := decodeFile(p + ".tmp.jpg"); err == nil {
		b := img.Bounds()
		l.setAspect(it, b.Dx(), b.Dy())
	}
	return os.Rename(p+".tmp.jpg", p)
}

func (l *Library) makePosterLarge(it *Item) error {
	p := l.CachePath(it, VarLarge)
	if err := l.runFFmpeg("-ss", fmt.Sprintf("%.2f", l.posterAt(it)), "-i", l.Path(it), "-frames:v", "1",
		"-vf", scaleLong(l.largeLong()), "-q:v", "3", p+".tmp.jpg"); err != nil {
		return err
	}
	return os.Rename(p+".tmp.jpg", p)
}

// makeVideoPreview builds a short silent loop: the whole clip for short
// videos, otherwise a few snippets spread over the video.
func (l *Library) makeVideoPreview(it *Item) error {
	p := l.CachePath(it, VarPreview)
	tmp := p + ".tmp.mp4"
	vf := scaleShort(l.opt.VideoPreviewSize) + ",fps=24,setsar=1,format=yuv420p"
	enc := []string{"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "31", "-profile:v", "high",
		"-movflags", "+faststart", "-threads", "2", tmp}
	src := l.Path(it)
	var err error
	if it.Duration <= 0 || it.Duration <= 6 {
		args := append([]string{"-i", src, "-t", "6", "-vf", vf}, enc...)
		err = l.runFFmpeg(args...)
	} else {
		n, seg := 5, 1.2
		if it.Duration < 30 {
			n, seg = 4, 1.0
		} else if it.Duration > 300 {
			n, seg = 6, 1.2
		}
		var args []string
		var filter strings.Builder
		for i := range n {
			at := it.Duration * (float64(i) + 0.5) / float64(n)
			at = min(at, it.Duration-seg-0.1)
			args = append(args, "-ss", fmt.Sprintf("%.2f", max(at, 0)), "-t", fmt.Sprintf("%.2f", seg), "-i", src)
			fmt.Fprintf(&filter, "[%d:v]%s[v%d];", i, vf, i)
		}
		for i := range n {
			fmt.Fprintf(&filter, "[v%d]", i)
		}
		fmt.Fprintf(&filter, "concat=n=%d:v=1:a=0[out]", n)
		args = append(args, "-filter_complex", filter.String(), "-map", "[out]")
		err = l.runFFmpeg(append(args, enc...)...)
		if err != nil {
			// fall back to the beginning of the video
			args := append([]string{"-i", src, "-t", "5", "-vf", vf}, enc...)
			err = l.runFFmpeg(args...)
		}
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// StreamH264 writes a browser-compatible MP4 of a video to w. A finished
// transcode is cached and served with range support by the caller.
func (l *Library) StreamH264(ctx context.Context, it *Item, w io.Writer) error {
	if l.media.ffmpeg == "" || !l.opt.Transcode {
		return errors.New("transcoding disabled or ffmpeg not found")
	}
	final := l.CachePath(it, VarH264)
	os.MkdirAll(filepath.Dir(final), 0o755)
	tmp := fmt.Sprintf("%s.%d.tmp", final, time.Now().UnixNano())
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, l.media.ffmpeg, "-v", "error", "-nostdin", "-i", l.Path(it),
		"-map", "0:v:0", "-map", "0:a:0?", "-vf", scaleLong(1920)+",format=yuv420p",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-c:a", "aac", "-b:a", "160k",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		f.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		f.Close()
		return err
	}
	_, copyErr := io.Copy(io.MultiWriter(w, f), out)
	waitErr := cmd.Wait()
	f.Close()
	if copyErr == nil && waitErr == nil {
		return os.Rename(tmp, final)
	}
	os.Remove(tmp)
	if copyErr != nil {
		return copyErr
	}
	return waitErr
}

// PrintAsIs reports whether the original can go to the printer unchanged:
// a format every browser shows, turned by nothing but its EXIF orientation.
func (l *Library) PrintAsIs(it *Item) bool {
	if it.Kind != Photo || it.Rot != 0 {
		return false
	}
	switch it.Ext() {
	case ".jpg", ".jpeg", ".jpe", ".png", ".gif", ".webp":
		return true
	}
	return false
}

// PrintImage returns a JPEG of a photo at full size, turned like in the
// viewer: HEIC and the like decoded, RAW files from their embedded JPEG,
// odd formats by the helper or ffmpeg. Videos give their preview frame.
func (l *Library) PrintImage(ctx context.Context, it *Item) ([]byte, error) {
	if it.Kind == Photo {
		img, err := l.decodeOriginal(it, 0)
		if err != nil {
			img, err = l.decodeExternal(ctx, it)
		}
		if err == nil {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		}
		l.logf("print %s: %v (preview instead)", it.Rel, err)
	}
	return l.renderJPEG(ctx, it, 0, false, 95)
}

// decodeOriginal decodes a photo with Go (RAW: the embedded JPEG), at most
// n pixels on the long edge (0: full size), turned like in the viewer.
func (l *Library) decodeOriginal(it *Item, n int) (image.Image, error) {
	src := l.Path(it)
	var img image.Image
	var err error
	if rawExt[it.Ext()] {
		var b []byte
		if b, err = extractPreview(l.opt.ExifTool, src); err == nil {
			img, _, err = image.Decode(bytes.NewReader(b))
		}
	} else {
		img, err = decodeFile(src)
	}
	if err != nil {
		return nil, err
	}
	if n > 0 {
		img = resizeFit(img, n, n) // before turning: cheaper
	}
	return orient(img, l.displayOrient(it)), nil
}

// decodeExternal decodes a photo Go cannot read at full size with the
// helper (Pillow) or ffmpeg; they apply the file's orientation, the admin
// rotation comes on top.
func (l *Library) decodeExternal(ctx context.Context, it *Item) (image.Image, error) {
	src := l.Path(it)
	tmp, err := os.CreateTemp(l.opt.CacheDir, "full-*.jpg")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	err = errors.New("no decoder")
	if l.local != nil {
		_, _, err = l.local.Thumb(src, []helper.ThumbSpec{{Path: tmp.Name(), Long: 1 << 30, Q: 95}})
	}
	if err != nil && l.media.ffmpeg != "" {
		cmd := exec.CommandContext(ctx, l.media.ffmpeg, "-v", "error", "-y", "-i", src, "-frames:v", "1", "-q:v", "2", "-update", "1", tmp.Name())
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			err = fmt.Errorf("ffmpeg: %v %s", cerr, firstLine(out))
		} else {
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	img, err := decodeFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	return orient(img, composeOrient(1, it.Rot)), nil
}

// renderJPEG returns a JPEG of what the viewer shows, at most n pixels on
// the long edge (0: as it comes). original reads photos from the file even
// when a large preview exists; files Go cannot decode fall back to the
// preview.
func (l *Library) renderJPEG(ctx context.Context, it *Item, n int, original bool, quality int) ([]byte, error) {
	var img image.Image
	var err error
	if it.Kind == Photo && (original || !exists(l.CachePath(it, VarLarge))) {
		img, err = l.decodeOriginal(it, n)
	}
	if img == nil {
		// the large preview, oriented and rotated already; the helper or
		// ffmpeg make it for files Go cannot decode
		var p string
		if p, err = l.Ensure(ctx, it, VarLarge); err == nil {
			img, err = decodeFile(p)
		}
	}
	if err != nil {
		return nil, err
	}
	if n > 0 {
		img = resizeFit(img, n, n)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
