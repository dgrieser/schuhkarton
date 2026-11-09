package gallery

import (
	"bytes"
	"compress/gzip"
	"context"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	h265heic "github.com/gen2brain/h265/heic"
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

// HEIC goes to the decoder in Go first; the heif tool only steps in for a
// file that one cannot read.
func TestHEIFOrder(t *testing.T) {
	dir := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 5, 4))
	jp := filepath.Join(dir, "ready.jpg")
	if err := writeJPEG(jp, src, 90); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(dir, "called")
	tool := filepath.Join(dir, "heif-dec")
	os.WriteFile(tool, []byte("#!/bin/sh\ntouch "+called+"\ncp "+jp+" \"$4\"\n"), 0o755)
	old, oldPath := heifTool, pathHEIFTool
	heifTool, pathHEIFTool = func() string { return tool }, func() string { return "" }
	defer func() { heifTool, pathHEIFTool = old, oldPath }()

	// a real HEIC, 64 x 32
	pic := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for i := range pic.Pix {
		pic.Pix[i] = 128
	}
	var buf bytes.Buffer
	if err := h265heic.Encode(&buf, pic, h265heic.EncodeOptions{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	heic := filepath.Join(dir, "IMG_1.HEIC")
	os.WriteFile(heic, buf.Bytes(), 0o644)
	img, err := decodeFile(heic)
	if err != nil || img.Bounds().Dx() != 64 || img.Bounds().Dy() != 32 {
		t.Fatalf("go decoder: %v %v", err, img)
	}
	if exists(called) {
		t.Error("the tool was asked for a HEIC the Go decoder reads")
	}

	// one the Go decoder cannot read: the tool
	bad := filepath.Join(dir, "IMG_2.HEIC")
	os.WriteFile(bad, append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...), 0o644)
	img, err = decodeFile(bad)
	if err != nil || img.Bounds().Dx() != 5 || !exists(called) {
		t.Fatalf("fallback: %v", err)
	}
}

// The bundled heif-dec is unpacked once into the cache folder, executable.
func TestBundledHEIFDec(t *testing.T) {
	script := []byte("#!/bin/sh\necho bundled\n")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(script)
	zw.Close()
	oldGz := bundledHEIFGz
	bundledHEIFGz = gz.Bytes()
	reset := func() {
		bundle.Lock()
		bundle.dir, bundle.path, bundle.err = "", "", nil
		bundle.Unlock()
	}
	reset()
	defer func() { bundledHEIFGz = oldGz; reset() }()
	dir := t.TempDir()
	setToolDir(dir)
	p := bundledHEIFDec()
	if !strings.HasPrefix(p, filepath.Join(dir, "tools", "heif-dec-")) {
		t.Fatalf("path %q", p)
	}
	out, err := exec.Command(p).Output()
	if err != nil || string(out) != "bundled\n" {
		t.Fatalf("run: %v %q", err, out)
	}
	if heifTool() != p || bundledHEIFDec() != p {
		t.Error("not the bundled one")
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "tools", ".unpack-*")); len(m) > 0 {
		t.Errorf("left over: %v", m)
	}
}

// A HEIC waiting for the slow path holds no decode slot meanwhile.
func TestSlowDecodeLendsSlot(t *testing.T) {
	compiled, tool := wasmCompiled, heifTool
	wasmCompiled, heifTool = false, func() string { return "" }
	defer func() { wasmCompiled, heifTool = compiled, tool; setSlowDecodes(1) }()
	setSlowDecodes(1)
	dir := t.TempDir()
	heic := filepath.Join(dir, "x.heic")
	os.WriteFile(heic, append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...), 0o644)
	l := &Library{opt: Options{CacheDir: dir}}
	busy := slowSlot() // somebody else on the slow path
	slot := func() func() {
		r, err := decodeSlot(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	var mine []func()
	for range cap(decodeSlots) - 1 {
		mine = append(mine, slot())
	}
	done := make(chan struct{})
	go func() {
		l.convert(context.Background(), ConvertRequest{Src: heic, Outputs: []ConvertOutput{{Path: filepath.Join(dir, "o.jpg"), Quality: 80}}}, false)
		close(done)
	}()
	got := make(chan func())
	go func() { got <- slot() }()
	select {
	case r := <-got: // the waiting HEIC holds no slot
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

func TestHEIFLast(t *testing.T) {
	list := []*Item{{Rel: "a.HEIC"}, {Rel: "b.jpg"}, {Rel: "c.jpeg", Format: ".heic"}, {Rel: "d.png"}, {Rel: "e.avif"}}
	heifLast(list)
	var got []string
	for _, it := range list {
		got = append(got, it.Rel)
	}
	if strings.Join(got, " ") != "b.jpg d.png a.HEIC c.jpeg e.avif" {
		t.Errorf("order %v", got)
	}
}

// Background work leaves a slot for viewers; a viewer that left stops waiting.
func TestDecodeSlotsReserveAndGiveUp(t *testing.T) {
	ctx := context.Background()
	var bg []func()
	for range cap(bgSlots) {
		r, err := decodeSlot(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		bg = append(bg, r)
	}
	defer func() {
		for _, r := range bg {
			r()
		}
	}()
	// one more background decode waits, a viewer's does not
	c, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := decodeSlot(c, true); err == nil {
		t.Fatal("background took the viewers' slot")
	}
	r, err := decodeSlot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	// all taken: a viewer that left gives up waiting, its work context goes on
	req, leave := context.WithCancel(ctx)
	work := waitFor(context.WithoutCancel(req), req)
	errc := make(chan error, 1)
	go func() { _, err := decodeSlot(work, false); errc <- err }()
	leave()
	select {
	case err := <-errc:
		if err == nil {
			t.Error("got a slot although all are taken")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting after the viewer left")
	}
	r()
}

// A wait for the slow path gives up with its context (a closed window, a
// viewer that left), instead of queueing behind a decode of minutes.
func TestSlowSlotGivesUp(t *testing.T) {
	defer setSlowDecodes(1)
	setSlowDecodes(1)
	busy := slowSlot()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if release, err := waitSlowSlot(ctx); err == nil {
		release()
		t.Fatal("got a slot that was taken")
	}
	busy()
	release, err := waitSlowSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// Faces do not take the slow path: a HEIC the native tool cannot read is
// sent as its large preview, while somebody else holds the slow slot.
func TestFaceImageSkipsSlowPath(t *testing.T) {
	compiled, tool := wasmCompiled, heifTool
	wasmCompiled, heifTool = false, func() string { return "" }
	defer func() { wasmCompiled, heifTool = compiled, tool; setSlowDecodes(1) }()
	setSlowDecodes(1)
	root := t.TempDir()
	heic := filepath.Join(root, "2020/IMG_0001.HEIC")
	os.MkdirAll(filepath.Dir(heic), 0o755)
	os.WriteFile(heic, append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...), 0o644)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := l.Items()[0]
	p := l.CachePath(it, VarLarge)
	os.MkdirAll(filepath.Dir(p), 0o755)
	saveJPEG(t, p, picture(3, 320, 240, 0), 90)
	busy := slowSlot()
	defer busy()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	b, name, err := l.faceImage(ctx, it)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("waited %s for the slow path", d)
	}
	want, _ := os.ReadFile(p)
	if name != "face.jpg" || string(b) != string(want) {
		t.Errorf("not the preview: %s, %d bytes", name, len(b))
	}
}

// The bundled heif-dec reads only HEVC: for AVIF an installed one goes
// first; for HEIC the bundled one, then the installed one.
func TestHEIFToolsForAVIF(t *testing.T) {
	dir := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 7, 3))
	jp := filepath.Join(dir, "ready.jpg")
	if err := writeJPEG(jp, src, 90); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	mk := func(name string, ok bool) string {
		p := filepath.Join(dir, name)
		body := "#!/bin/sh\necho " + name + " >>" + log + "\n"
		if ok {
			body += "cp " + jp + " \"$4\"\n"
		} else {
			body += "exit 1\n"
		}
		os.WriteFile(p, []byte(body), 0o755)
		return p
	}
	bundled, installed := mk("bundled", false), mk("installed", true)
	oldPath := pathHEIFTool
	pathHEIFTool = func() string { return installed }
	defer func() { pathHEIFTool = oldPath }()
	for _, c := range []struct{ name, head, want string }{
		{"x.avif", "\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avif", "installed\n"},
		{"x.heic", "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic", "bundled\ninstalled\n"},
	} {
		os.Remove(log)
		f := filepath.Join(dir, c.name)
		os.WriteFile(f, append([]byte(c.head), make([]byte, 64)...), 0o644)
		img, err := decodeForConvert(f, false, bundled)
		if err != nil || img.Bounds().Dx() != 7 {
			t.Fatalf("%s: %v", c.name, err)
		}
		if b, _ := os.ReadFile(log); string(b) != c.want {
			t.Errorf("%s: tools %q, want %q", c.name, b, c.want)
		}
	}
}
