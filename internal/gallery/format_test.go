package gallery

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSniffExt(t *testing.T) {
	// the start of an iPhone HEIC saved as IMG_0253.jpeg
	heic := []byte("\x00\x00\x00\x24ftypheic\x00\x00\x00\x00mif1MiPrmiafMiHBheic")
	avif := []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf")
	avifMif1 := []byte("\x00\x00\x00\x1cftypmif1\x00\x00\x00\x00mif1avifmiaf")
	mov := []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00qt  ")
	for want, head := range map[string][]byte{
		".heic": heic, ".avif": avif, ".mov": mov,
		".jpg": {0xFF, 0xD8, 0xFF, 0xE1}, ".png": []byte("\x89PNG\r\n\x1a\n...."),
		".webp": []byte("RIFF....WEBPVP8 "), ".tif": []byte("II*\x00...."), "": []byte("hello"),
	} {
		if got := sniffExt(head); got != want {
			t.Errorf("sniffExt(%q) = %q, want %q", head, got, want)
		}
	}
	if got := sniffExt(avifMif1); got != ".avif" {
		t.Errorf("mif1 with avif brand: %q", got)
	}
	if got := sniffExt([]byte("\x00\x00\x00\x00ftypisom")); got != ".mp4" {
		t.Errorf("short box: %q", got)
	}
}

func TestDisplayOrientByContent(t *testing.T) {
	l, _ := testLib(t)
	it := &Item{Rel: "IMG_0253.jpeg", Kind: Photo, Orient: 6}
	if o := l.displayOrient(it); o != 6 {
		t.Errorf("a JPEG is turned by its EXIF orientation: %d", o)
	}
	// the HEIC decoder turns the picture itself
	it.Format = ".heic"
	if o := l.displayOrient(it); o != 1 {
		t.Errorf("HEIC inside a .jpeg turned twice: %d", o)
	}
	if it.Ext() != ".heic" || orientWritable(it.Ext()) {
		t.Errorf("ext %q", it.Ext())
	}
}

// TestMisnamedFile: a PNG saved as .jpg keeps its name, is served as what it
// is, takes the metadata although exiftool picks formats by name, and an
// older index learns its format on the next scan.
func TestMisnamedFile(t *testing.T) {
	et := FindExifTool("")
	if et == "" {
		t.Skip("exiftool not installed")
	}
	root := t.TempDir()
	p := filepath.Join(root, "scan.jpg")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	f.Close()
	os.Chmod(p, 0o600) // a private file stays private
	writeTestJPEG(t, filepath.Join(root, "ok.jpeg"), 40, 30)
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, ExifTool: et, WriteMetadata: true}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	it, ok := byRel(l, "scan.jpg"), byRel(l, "ok.jpeg")
	if it.Format != ".png" || ok.Format != "" || it.Ext() != ".png" || ok.Ext() != ".jpeg" {
		t.Fatalf("formats %q %q", it.Format, ok.Format)
	}
	l.setOCR(it, &OCRInfo{Text: "Rechnung 42", Model: "GLM-OCR", At: 1700000001})
	if err := l.jobPass(ctx, &l.xmpJob, l.xmpTask()); err != nil {
		t.Fatal(err)
	}
	l.closeXMPTools()
	if d, f := l.xmpJob.done.Load(), l.xmpJob.fail.Load(); d != 1 || f != 0 { // ok.jpeg has nothing to take
		t.Fatalf("written %d, failed %d", d, f)
	}
	tool, err := startExifTool(et, l.exifCfg)
	if err != nil {
		t.Fatal(err)
	}
	m, err := tool.readOne(p, xmpReadTags)
	tool.close()
	if err != nil || m["SkText"] != "Rechnung 42" {
		t.Errorf("text in the file: %v %v", m["SkText"], err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("name and permissions kept: %v %v", st.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(opt.CacheDir, "write-*")); len(left) > 0 {
		t.Errorf("copies left behind: %v", left)
	}
	if b, _ := os.ReadFile(p); sniffExt(b) != ".png" {
		t.Error("still a PNG")
	}
	// a link stays a link, the file behind it takes the change
	target := filepath.Join(t.TempDir(), "elsewhere.jpg")
	b, _ := os.ReadFile(p)
	os.WriteFile(target, b, 0o640)
	link := filepath.Join(root, "link.jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	if err := l.withRealName(&Item{Format: ".png"}, link, func(p string) error {
		return exifToolWrite(et, []string{"-m", "-overwrite_original", "-XMP-dc:Title=Verlinkt", p})
	}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v %v", fi.Mode(), err)
	}
	if after, _ := os.Stat(target); !os.SameFile(before, after) || after.Mode().Perm() != 0o640 {
		t.Errorf("target replaced or permissions changed: %v", after.Mode())
	}
	if out, _ := exifToolCmd(et, "-s3", "-XMP-dc:Title", target).Output(); strings.TrimSpace(string(out)) != "Verlinkt" {
		t.Errorf("target not written: %q", out)
	}
	os.Remove(link)
	// an edit through the one-shot exiftool works too
	if err := l.writeTags(it, []string{"-XMP-photoshop:City=Lübbecke"}); err != nil {
		t.Errorf("edit: %v", err)
	}
	l.afterWrite(it, 0)
	l.saveIndex()
	l.Close()

	// an index from before the format check learns it on the next scan
	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	for _, it := range l2.Items() {
		it.Format = ""
	}
	sig := byRel(l2, "scan.jpg").MetaSig
	l2.formatsKnown.Store(false)
	if err := l2.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if it := byRel(l2, "scan.jpg"); it.Format != ".png" || !l2.formatsKnown.Load() || it.MetaSig != sig || sig == "" {
		t.Errorf("after the check: %q %v, sig %q (was %q)", it.Format, l2.formatsKnown.Load(), it.MetaSig, sig)
	}
	// a PNG named .bmp had its metadata beside it; now it goes into the file
	b, _ = os.ReadFile(p)
	if err := os.WriteFile(filepath.Join(root, "x.bmp"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	bmp := &Item{ID: "x", Root: 0, Rel: "x.bmp", Kind: Photo, MetaSig: "v1-1"}
	if _, all := l2.checkFormats(ctx, []*Item{bmp}); !all || bmp.Format != ".png" || bmp.MetaSig != "" {
		t.Errorf("sidecar to file: %q, sig %q, all read %v", bmp.Format, bmp.MetaSig, all)
	}
	// a video inside a .jpg is no photo format
	if err := os.WriteFile(filepath.Join(root, "clip.jpg"), []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00qt  "), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectFormat(filepath.Join(root, "clip.jpg")); got != "" {
		t.Errorf("video inside a photo name: %q", got)
	}
	// a file that cannot be read now is checked again next time
	gone := &Item{ID: "y", Root: 0, Rel: "gone.jpg", Kind: Photo}
	if _, all := l2.checkFormats(ctx, []*Item{gone}); all {
		t.Error("an unreadable file counts as checked")
	}
}

// TestMisnamedOldSidecar: a PNG named .bmp got its metadata beside it while
// the name decided; a new cache reads that sidecar and writes it into the file.
func TestMisnamedOldSidecar(t *testing.T) {
	et := FindExifTool("")
	if et == "" {
		t.Skip("exiftool not installed")
	}
	root := t.TempDir()
	p := filepath.Join(root, "x.bmp")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	f.Close()
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, ExifTool: et, WriteMetadata: true}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	side := p + ".xmp"
	os.WriteFile(side, []byte(emptyXMP), 0o644)
	if out, err := exifToolCmd(et, "-config", l.exifCfg, "-overwrite_original", "-XMP-schuhkarton:SkSig=v1-old",
		"-XMP-schuhkarton:SkAIModel=vision-1", "-XMP-iptcCore:AltTextAccessibility-de=Ein leeres Bild", side).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	it := byRel(l, "x.bmp")
	if it.Format != ".png" || it.MetaSig != "" {
		t.Fatalf("format %q, sig %q", it.Format, it.MetaSig)
	}
	if a := l.AIOf(it); a == nil || a.De != "Ein leeres Bild" {
		t.Fatalf("description from the old sidecar: %+v", a)
	}
	if err := l.jobPass(ctx, &l.xmpJob, l.xmpTask()); err != nil {
		t.Fatal(err)
	}
	l.closeXMPTools()
	if out, _ := exifToolCmd(et, "-s3", "-XMP-iptcCore:AltTextAccessibility-de", p).Output(); strings.TrimSpace(string(out)) != "Ein leeres Bild" {
		t.Errorf("not in the file: %q", out)
	}
}
