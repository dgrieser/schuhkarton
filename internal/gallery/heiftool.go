package gallery

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// The built-in HEIC/AVIF decoders run libheif compiled to WebAssembly. On
// amd64 and arm64 wazero compiles it to machine code; elsewhere (32-bit ARM,
// e.g. a Raspberry Pi with the 32-bit OS) it interprets it, and one photo
// takes minutes. There a native heif-dec / heif-convert (libheif) is used
// when it is installed. Like the built-in decoder it applies the rotation
// stored in the container (see needsOrient).

var wasmCompiled = runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"

var heifTool = sync.OnceValue(func() string {
	if wasmCompiled {
		return ""
	}
	return FindHEIFTool()
})

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
	p := slowSlots.Load()
	if p == nil {
		setSlowDecodes(1)
		p = slowSlots.Load()
	}
	ch := *p
	ch <- struct{}{}
	return func() { <-ch }
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
func isHEIF(f io.ReadSeeker) bool {
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	f.Seek(0, io.SeekStart)
	switch sniffExt(head[:n]) {
	case ".heic", ".avif":
		return true
	}
	return false
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
	if b, err := exec.CommandContext(ctx, tool, "-q", "95", p, out).CombinedOutput(); err != nil {
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
