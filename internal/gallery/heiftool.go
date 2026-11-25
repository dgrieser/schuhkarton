package gallery

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/avif"
	h265heic "github.com/gen2brain/h265/heic"
	wasmheic "github.com/gen2brain/heic"
)

// HEIC/HEIF and AVIF photos go through three decoders (decodeHEIF):
//
//  1. HEIC in pure Go (gen2brain/h265): seconds, also on a Raspberry Pi
//     with the 32-bit OS (a 24 MP iPhone photo in about 5 s there).
//  2. heif-dec (libheif): the static one the Linux release builds carry
//     (heifdec_*.go, built by deploy/heif-dec), else one installed.
//  3. libheif and libavif compiled to WebAssembly (gen2brain/heic, /avif).
//     On amd64 and arm64 wazero compiles them to machine code; elsewhere
//     (32-bit ARM) it interprets them, and one photo takes half an hour:
//     there only on the slow path, one at a time (slowSlot).
//
// All of them apply the rotation stored in the container (see needsOrient).

var wasmCompiled = runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"

// heifTool returns the heif-dec to use: the bundled one, else one on the
// PATH, else "" (decodeHEIF also tries the one on the PATH after the
// bundled one).
var heifTool = func() string {
	if p := bundledHEIFDec(); p != "" {
		return p
	}
	return pathHEIFTool()
}

var pathHEIFTool = sync.OnceValue(FindHEIFTool)

// bundledHEIFGz is the gzipped static heif-dec of this build (empty: none;
// see heifdec_*.go). It is unpacked into the cache folder when first needed.
var bundle struct {
	sync.Mutex
	dir  string // the folder to unpack into; empty: the user's cache folder
	path string // where it was unpacked
	err  error  // unpacking failed: not tried again
}

// setToolDir sets where the bundled tools are unpacked (the cache folder).
func setToolDir(dir string) {
	bundle.Lock()
	defer bundle.Unlock()
	if bundle.path == "" {
		bundle.dir, bundle.err = dir, nil
	}
}

// HasBundledHEIFDec tells whether this build carries its own heif-dec.
func HasBundledHEIFDec() bool { return len(bundledHEIFGz) > 0 }

// bundledHEIFDec returns the bundled heif-dec, unpacked, or "".
func bundledHEIFDec() string {
	if len(bundledHEIFGz) == 0 {
		return ""
	}
	bundle.Lock()
	defer bundle.Unlock()
	if bundle.path != "" || bundle.err != nil {
		return bundle.path
	}
	dir := bundle.dir
	if dir == "" {
		d, err := os.UserCacheDir()
		if err != nil {
			bundle.err = err
			return ""
		}
		dir = filepath.Join(d, "schuhkarton")
	}
	sum := sha256.Sum256(bundledHEIFGz)
	p := filepath.Join(dir, "tools", fmt.Sprintf("heif-dec-%x", sum[:6]))
	if _, err := os.Stat(p); err != nil {
		if err := unpackGzip(p, bundledHEIFGz); err != nil {
			bundle.err = err
			log.Printf("heif-dec: not unpacked: %v", err)
			return ""
		}
		// the ones of earlier versions
		old, _ := filepath.Glob(filepath.Join(dir, "tools", "heif-dec-*"))
		for _, o := range old {
			if o != p {
				os.Remove(o)
			}
		}
	}
	bundle.path = p
	return p
}

// unpackGzip writes gzipped data as an executable to p; another process
// unpacking the same at the same time does no harm.
func unpackGzip(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".unpack-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, zr); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Decoding a photo holds tens of MB (a 12 MP picture, then its scaled
// copies), a HEIC in interpreted WebAssembly some hundred MB for minutes.
// A browser asks for many previews at once, so the decodes queue: as many
// at a time as there are CPUs, and one at a time on the slow path.
// Background work takes one of fewer slots first, so a slot is always left
// for what somebody is looking at.
var (
	decodeSlots = make(chan struct{}, max(2, runtime.NumCPU()))
	bgSlots     = make(chan struct{}, max(1, runtime.NumCPU()-1))
	slowSlots   atomic.Pointer[chan struct{}] // media.slow_decodes, 1 when not set
)

func setSlowDecodes(n int) {
	ch := make(chan struct{}, max(1, n))
	slowSlots.Store(&ch)
}

// slowSlot waits for a slot on the slow path; call the result to give it back.
func slowSlot() func() {
	release, _ := waitSlowSlot(context.Background())
	return release
}

// waitSlowSlot is slowSlot that gives up (nil, the error) when the wait
// ends, like decodeSlot: one slow decode takes minutes, so a closed time
// window or a viewer that left does not queue behind it.
func waitSlowSlot(ctx context.Context) (func(), error) {
	p := slowSlots.Load()
	if p == nil {
		setSlowDecodes(1)
		p = slowSlots.Load()
	}
	ch := *p
	done, stop := waitDone(ctx)
	defer stop()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-done:
		return nil, context.Cause(waitCtx(ctx))
	}
}

// SlowHEIF tells whether HEIC/AVIF photos decode slowly here (no
// WebAssembly compiler for this CPU): then heif-dec and media.slow_decodes
// matter.
func SlowHEIF() bool { return !wasmCompiled }

// FindHEIFTool returns the native HEIF decoder used on such machines, or "".
func FindHEIFTool() string {
	for _, c := range []string{"heif-dec", "heif-convert"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// decodeSlot waits for a free decode slot; call the result to give it
// back. Background work keeps one slot free for people looking at photos.
// It gives up (nil, the error) when the wait ends: ctx, or the request
// that asked for the picture (see waitFor) is gone.
func decodeSlot(ctx context.Context, background bool) (func(), error) {
	done, stop := waitDone(ctx)
	defer stop()
	if background {
		select {
		case bgSlots <- struct{}{}:
		case <-done:
			return nil, context.Cause(waitCtx(ctx))
		}
	}
	select {
	case decodeSlots <- struct{}{}:
	case <-done:
		if background {
			<-bgSlots
		}
		return nil, context.Cause(waitCtx(ctx))
	}
	return func() {
		<-decodeSlots
		if background {
			<-bgSlots
		}
	}, nil
}

type waitKey struct{}

// waitFor marks work (that goes on after a request ends) with the request:
// while it waits for a decode slot, it gives up when the request is gone,
// so a viewer leafing quickly through photos does not queue up decodes
// nobody looks at any more. Once a slot is taken, the work runs to the end.
func waitFor(work, request context.Context) context.Context {
	return context.WithValue(work, waitKey{}, request)
}

func waitCtx(ctx context.Context) context.Context {
	if r, ok := ctx.Value(waitKey{}).(context.Context); ok && r.Err() != nil {
		return r
	}
	return ctx
}

// waitDone ends when ctx or the request of waitFor ends; call stop when
// done waiting.
func waitDone(ctx context.Context) (<-chan struct{}, func()) {
	r, ok := ctx.Value(waitKey{}).(context.Context)
	switch {
	case !ok || r.Done() == nil:
		return ctx.Done(), func() {}
	case ctx.Done() == nil:
		return r.Done(), func() {}
	}
	done, quit := make(chan struct{}), make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			close(done)
		case <-r.Done():
			close(done)
		case <-quit:
		}
	}()
	return done, func() { close(quit) }
}

// rewind goes back to the start of f.
func rewind(f io.Seeker) bool {
	_, err := f.Seek(0, io.SeekStart)
	return err == nil
}

// isHEIF tells by its content whether f is a HEIC/HEIF or AVIF image (also
// one with a .jpeg name) and rewinds it.
func isHEIF(f io.ReadSeeker) bool { return heifKind(f) != "" }

// heifKind is ".heic" or ".avif" for such an image, else ""; it rewinds f.
func heifKind(f io.ReadSeeker) string {
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	f.Seek(0, io.SeekStart)
	switch ext := sniffExt(head[:n]); ext {
	case ".heic", ".avif":
		return ext
	}
	return ""
}

// decodeHEIF decodes a HEIC/HEIF or AVIF photo (see the top of this file):
// HEIC in Go, then with the heif tool, then in WebAssembly; on a machine
// where that is slow only when slow is allowed, else errNeedSlow.
func decodeHEIF(f io.ReadSeeker, p, tool string, slow bool) (image.Image, error) {
	kind := heifKind(f)
	var errs []error
	if kind == ".heic" {
		img, err := h265heic.Decode(f, h265heic.Options{AutoRotate: true})
		if err == nil {
			return img, nil
		}
		errs = append(errs, fmt.Errorf("heic: %w", err))
		if !rewind(f) {
			return nil, errors.Join(errs...)
		}
	}
	// the bundled heif-dec reads only HEVC: an installed one, which may
	// read AV1 too, goes first for AVIF, else after it
	tools := []string{tool, pathHEIFTool()}
	if kind == ".avif" {
		tools[0], tools[1] = tools[1], tools[0]
	}
	for i, t := range tools {
		if t == "" || (i == 1 && t == tools[0]) {
			continue
		}
		img, err := decodeHEIFToolWith(t, p)
		if err == nil {
			return img, nil
		}
		errs = append(errs, err)
	}
	if !wasmCompiled && !slow {
		return nil, errNeedSlow
	}
	var img image.Image
	var err error
	if kind == ".avif" {
		img, err = avif.Decode(f)
	} else {
		img, err = wasmheic.Decode(f)
	}
	if err == nil {
		return img, nil
	}
	return nil, errors.Join(append(errs, err)...)
}

// decodeHEIFTool decodes with the native tool, or says why it could not.
func decodeHEIFTool(p string) (image.Image, error) { return decodeHEIFToolWith(heifTool(), p) }

func decodeHEIFToolWith(tool, p string) (image.Image, error) {
	if tool == "" {
		return nil, errors.New("no heif tool")
	}
	dir, err := os.MkdirTemp("", "schuhkarton-heif-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "img.jpg")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if b, err := combinedNice(exec.CommandContext(ctx, tool, "-q", "95", p, out)); err != nil {
		return nil, fmt.Errorf("%s: %v %s", filepath.Base(tool), err, firstLine(b))
	}
	if !exists(out) {
		// files with several images: one output per image (img-1.jpg ...),
		// the primary image comes first
		m, _ := filepath.Glob(filepath.Join(dir, "img-*.jpg"))
		if len(m) == 0 {
			return nil, errors.New("heif tool wrote nothing")
		}
		out = m[0]
	}
	f, err := os.Open(out)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}
