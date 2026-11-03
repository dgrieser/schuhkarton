package gallery

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A stand-in heif tool (it only copies a JPEG): decodeFile uses it for HEIC
// files, also when the tool writes one file per image.
func TestDecodeHEIFTool(t *testing.T) {
	compiled := wasmCompiled
	wasmCompiled = false // as on 32-bit ARM
	defer func() { wasmCompiled = compiled }()
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

// media.slow_decodes: that many on the slow path, the next one waits.
func TestSlowDecodes(t *testing.T) {
	defer setSlowDecodes(1)
	setSlowDecodes(2)
	a, b := slowSlot(), slowSlot()
	got := make(chan bool)
	go func() {
		release := slowSlot()
		got <- true
		release()
	}()
	select {
	case <-got:
		t.Fatal("a third decode did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	a()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("the waiting decode did not start")
	}
	b()
}

// On 64-bit the built-in decoder goes first; a native heif-dec on the PATH
// steps in for files it cannot read.
func TestHEIFToolAfterBuiltIn(t *testing.T) {
	if !wasmCompiled {
		t.Skip("64-bit only")
	}
	dir := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 5, 4))
	jp := filepath.Join(dir, "ready.jpg")
	if err := writeJPEG(jp, src, 90); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "heif-dec"), []byte("#!/bin/sh\ncp "+jp+" \"$4\"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	heic := filepath.Join(dir, "IMG_2.HEIC")
	os.WriteFile(heic, append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...), 0o644)
	img, err := decodeFile(heic)
	if err != nil || img.Bounds().Dx() != 5 {
		t.Fatalf("fallback: %v", err)
	}
}

// A HEIC waiting for the slow path gives its decode slot back meanwhile.
func TestSlowDecodeLendsSlot(t *testing.T) {
	compiled, tool := wasmCompiled, heifTool
	wasmCompiled, heifTool = false, func() string { return "" }
	defer func() { wasmCompiled, heifTool = compiled, tool; setSlowDecodes(1) }()
	setSlowDecodes(1)
	heic := filepath.Join(t.TempDir(), "x.heic")
	os.WriteFile(heic, append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...), 0o644)
	busy := slowSlot() // somebody else on the slow path
	var mine []func()
	for range cap(decodeSlots) - 1 {
		mine = append(mine, decodeSlot())
	}
	done := make(chan struct{})
	go func() {
		release := decodeSlot() // the last slot
		decodeHeld(heic)
		release()
		close(done)
	}()
	got := make(chan func())
	go func() { got <- decodeSlot() }()
	select {
	case r := <-got: // the waiting HEIC lent its slot
		busy()
		r()
	case <-time.After(2 * time.Second):
		t.Fatal("a JPEG waited behind the slow HEIC")
	}
	<-done
	for _, r := range mine {
		r()
	}
}
