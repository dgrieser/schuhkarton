package gallery

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary plays the convert child when asked to (see
// TestConvertChild).
func TestMain(m *testing.M) {
	if os.Getenv("SCHUHKARTON_TEST_CONVERT") == "1" {
		if err := RunConvert(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Thumbnails and previews made in a child process: same files, same
// sizes; a child that dies is an error, not a crash.
func TestConvertChild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("SCHUHKARTON_TEST_CONVERT", "1")
	convertMu.Lock()
	ConvertCommand = []string{exe}
	convertMu.Unlock()
	defer func() { convertMu.Lock(); ConvertCommand = nil; convertMu.Unlock() }()

	root := t.TempDir()
	saveJPEG(t, filepath.Join(root, "2020/IMG_20200101_120000.jpg"), picture(5, 1600, 1200, 0), 90)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, Previews: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	l.GenerateAll(ctx)
	it := l.Items()[0]
	for v, want := range map[string]image.Point{VarSmall: {480, 360}, VarLarge: {1600, 1200}} {
		img, err := decodeFile(l.CachePath(it, v))
		if err != nil || img.Bounds().Size() != want {
			t.Errorf("%s: %v %v", v, err, img)
		}
	}
	if it.Aspect < 1.33 || it.Aspect > 1.34 {
		t.Errorf("aspect %v", it.Aspect)
	}
	b, err := l.renderJPEG(ctx, it, 400, true, 80, true)
	if img, _, err2 := image.Decode(bytes.NewReader(b)); err != nil || err2 != nil || img.Bounds().Dx() != 400 {
		t.Errorf("job image: %v %v", err, err2)
	}

	convertMu.Lock()
	ConvertCommand = []string{"/bin/false"} // a child that dies
	convertMu.Unlock()
	if _, err := l.convert(ctx, ConvertRequest{Src: l.Path(it), Outputs: []ConvertOutput{{Path: filepath.Join(t.TempDir(), "x.jpg"), Quality: 80}}}, true); err == nil {
		t.Error("a dead child was no error")
	}
}

// A child that takes too long is stopped: an error the callers fall back
// from (not a context error), soon after the limit.
func TestConvertTimeout(t *testing.T) {
	convertMu.Lock()
	ConvertCommand = []string{"/bin/sh", "-c", "exec sleep 30"}
	oldLimit := convertTimeout
	convertTimeout = 200 * time.Millisecond
	convertMu.Unlock()
	defer func() {
		convertMu.Lock()
		ConvertCommand, convertTimeout = nil, oldLimit
		convertMu.Unlock()
	}()
	l := &Library{opt: Options{CacheDir: t.TempDir()}}
	start := time.Now()
	_, err := l.runConvert(context.Background(), ConvertRequest{Src: "x.jpg"})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no result after") {
		t.Fatalf("error %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("stopped after %s", d)
	}
}
