package gallery

import (
	"bytes"
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
)

func TestPrintImage(t *testing.T) {
	l, root := testLib(t, "a.jpg")
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 5000, 2500)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	size := func() image.Point {
		t.Helper()
		b, err := l.PrintImage(context.Background(), byRel(l, "a.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return image.Pt(cfg.Width, cfg.Height)
	}
	// full size from the original, not the preview
	if got := size(); got != image.Pt(5000, 2500) {
		t.Errorf("print size %v", got)
	}
	if !l.PrintAsIs(byRel(l, "a.jpg")) {
		t.Error("a plain JPEG should print as it is")
	}
	// turned like in the viewer (the rotation stays in Schuhkarton here),
	// so the original no longer prints as it is
	if _, err := l.Rotate([]string{byRel(l, "a.jpg").ID}, 90, "test"); err != nil {
		t.Fatal(err)
	}
	if got := size(); got != image.Pt(2500, 5000) {
		t.Errorf("turned print size %v", got)
	}
	if l.PrintAsIs(byRel(l, "a.jpg")) {
		t.Error("a turned photo must not print as it is")
	}
}

// A photo Go cannot decode prints from the large preview, which the helper
// or ffmpeg made for the viewer.
func TestPrintImageFallsBackToPreview(t *testing.T) {
	l, root := testLib(t, "a.jpg")
	it := byRel(l, "a.jpg")
	writeTestJPEG(t, l.CachePath(it, VarLarge), 300, 200)
	if err := os.WriteFile(filepath.Join(root, "a.jpg"), []byte("no picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := l.PrintImage(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width != 300 || cfg.Height != 200 {
		t.Errorf("preview not used: %v %dx%d", err, cfg.Width, cfg.Height)
	}
}
