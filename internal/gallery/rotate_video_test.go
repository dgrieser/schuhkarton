package gallery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotateVideo turns a small MP4 through its track header rotation; it
// needs exiftool and ffmpeg and is skipped without them.
func TestRotateVideo(t *testing.T) {
	et, ff := FindExifTool(""), FindFFmpeg("")
	if et == "" || ff == "" {
		t.Skip("exiftool or ffmpeg missing")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=64x48:rate=5:duration=1", "-pix_fmt", "yuv420p", src).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	st, _ := os.Stat(src)
	l := &Library{opt: Options{Roots: []Root{{Name: "B", Path: dir}}, CacheDir: t.TempDir(), ExifTool: et, WriteFiles: true}}
	it := &Item{Root: 0, Rel: "clip.mp4", Kind: Video, W: 64, H: 48, Aspect: 64.0 / 48, Size: st.Size(), MTime: st.ModTime().Unix()}
	it.key = l.keyOf(it)
	old := it.key
	if err := l.rotateVideo(it, 90); err != nil {
		t.Fatal(err)
	}
	out, _ := exifToolCmd(et, "-s3", "-n", "-Rotation", src).Output()
	if strings.TrimSpace(string(out)) != "90" {
		t.Fatalf("rotation %q, want 90", out)
	}
	if it.W != 48 || it.H != 64 || it.key == old {
		t.Fatalf("item not updated: %dx%d key changed %v", it.W, it.H, it.key != old)
	}
	if err := l.rotateVideo(it, 270); err != nil {
		t.Fatal(err)
	}
	out, _ = exifToolCmd(et, "-s3", "-n", "-Rotation", src).Output()
	if strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("rotation %q, want 0", out)
	}
	l.opt.WriteFiles = false
	if err := l.rotateVideo(it, 90); err == nil {
		t.Fatal("without writing files the video must stay as it is")
	}
}
