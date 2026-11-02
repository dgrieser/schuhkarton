package gallery

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// A stand-in heif tool (it only copies a JPEG): decodeFile uses it for HEIC
// files, also when the tool writes one file per image.
func TestDecodeHEIFTool(t *testing.T) {
	dir := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for i := range src.Pix {
		src.Pix[i] = 200
	}
	src.Set(0, 0, color.RGBA{0, 0, 0, 255})
	jp := filepath.Join(dir, "ready.jpg")
	if err := writeJPEG(jp, src, 95); err != nil {
		t.Fatal(err)
	}
	for _, multi := range []bool{false, true} {
		target := `"$4"`
		if multi {
			target = `"${4%.jpg}-1.jpg"`
		}
		tool := filepath.Join(dir, "heif-dec")
		os.WriteFile(tool, []byte("#!/bin/sh\n[ \"$1\" = -q ] || exit 2\ncp "+jp+" "+target+"\n"), 0o755)
		old := heifTool
		heifTool = func() string { return tool }
		// a HEIC with a .jpeg name: told by its content
		heic := filepath.Join(dir, "IMG_1.jpeg")
		os.WriteFile(heic, append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...), 0o644)
		img, err := decodeFile(heic)
		heifTool = old
		if err != nil {
			t.Fatalf("multi %v: %v", multi, err)
		}
		if b := img.Bounds(); b.Dx() != 8 || b.Dy() != 6 {
			t.Errorf("multi %v: size %v", multi, b)
		}
	}
	old := heifTool
	heifTool = func() string { return filepath.Join(dir, "missing") }
	defer func() { heifTool = old }()
	if _, err := decodeFile(jp); err != nil { // a JPEG never goes to the tool
		t.Errorf("jpeg: %v", err)
	}
}
