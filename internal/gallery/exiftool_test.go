package gallery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestArgFileSafe(t *testing.T) {
	for p, want := range map[string]bool{
		"/fotos/a.jpg":             true,
		"/fotos/a b#c -d.jpg":      true,
		`C:\Fotos\a.jpg`:           true,
		"":                         false,
		"/fotos/a\n-if\n1\nb.jpg":  false,
		"/fotos/a\rb.jpg":          false,
		"/fotos/a\x00b.jpg":        false,
		"-config":                  false,
		"#/fotos/a.jpg":            false,
		"/fotos/x\n/fotos/y/b.jpg": false,
	} {
		if got := argFileSafe(p); got != want {
			t.Errorf("argFileSafe(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestScanNameWithLineBreak: a file name with line breaks must not reach
// exiftool as arguments of its own (-if runs Perl). The file stays in the
// library, without metadata; the others are read as usual.
func TestScanNameWithLineBreak(t *testing.T) {
	et := FindExifTool("")
	if et == "" {
		t.Skip("exiftool not installed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("no line breaks in file names")
	}
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "ok.jpg"), 40, 30)
	if out, err := exifToolCmd(et, "-overwrite_original", "-Model=Kamera 1", filepath.Join(root, "ok.jpg")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	evil := "x\n-if\nopen(F,q(>" + filepath.ToSlash(marker) + "))\ny.jpg"
	// the expression holds slashes: the name spans folders
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, evil)), 0o755); err != nil {
		t.Skip("file system takes no line breaks in names:", err)
	}
	writeTestJPEG(t, filepath.Join(root, evil), 40, 30)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, ExifTool: et})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("exiftool ran an expression from a file name")
	}
	if n := len(l.Items()); n != 2 {
		t.Fatalf("items: %d, want 2", n)
	}
	if ok := byRel(l, "ok.jpg"); ok == nil || ok.Model != "Kamera 1" {
		t.Fatalf("ok.jpg not read: %+v", ok)
	}
}

// TestScanLinks: a link named like a photo is shown only when it leads to a
// file inside the library; one that leads elsewhere could hand out any file.
func TestScanLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 40, 30)
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a.jpg"), filepath.Join(root, "inside.jpg")); err != nil {
		t.Skip("no links here:", err)
	}
	os.Symlink(secret, filepath.Join(root, "out.jpg"))
	os.Symlink(filepath.Join(root, "gone.jpg"), filepath.Join(root, "broken.jpg"))
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if byRel(l, "a.jpg") == nil || byRel(l, "inside.jpg") == nil {
		t.Fatal("the file and the link inside the library belong to it")
	}
	if byRel(l, "out.jpg") != nil || byRel(l, "broken.jpg") != nil {
		t.Fatal("links out of the library and to nowhere must be left out")
	}
	// a file that becomes a link after the scan is not handed out either
	a := byRel(l, "a.jpg")
	if f, err := l.OpenOriginal(a); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	os.Remove(filepath.Join(root, "a.jpg"))
	os.Symlink(secret, filepath.Join(root, "a.jpg"))
	if f, err := l.OpenOriginal(a); err == nil {
		f.Close()
		t.Fatal("a link out of the library was opened")
	}
}
