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
	for _, c := range []string{"heif-dec", "heif-convert"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
})

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
func decodeHEIFTool(p string) (image.Image, error) {
	tool := heifTool()
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
