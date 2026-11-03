package gallery

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/gob"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStoredBox(t *testing.T) {
	stored := Face{X: 0.1, Y: 0.2, W: 0.3, H: 0.1}
	for o := 1; o <= 8; o++ {
		back := storedBox(shownBox(stored, o), o)
		if !near(back.X, stored.X) || !near(back.Y, stored.Y) || !near(back.W, stored.W) || !near(back.H, stored.H) {
			t.Errorf("orientation %d: %+v", o, back)
		}
	}
	// orientation 6 (turn 90° clockwise to show): a face at the top left of
	// the stored pixels shows at the top right
	if s := shownBox(stored, 6); !near(s.X, 0.7) || !near(s.Y, 0.1) {
		t.Errorf("orientation 6 shown: %+v", s)
	}
}

func TestMergeList(t *testing.T) {
	// the user's keyword stays, the old AI keyword goes, a new one comes
	all, ours, ok := mergeList([]string{"Urlaub", "hund"}, []string{"hund"}, []string{"Strand", "urlaub"})
	if !ok || strings.Join(all, ",") != "Urlaub,Strand" || strings.Join(ours, ",") != "Strand" {
		t.Errorf("merge: %v %v %v", all, ours, ok)
	}
	if _, _, ok := mergeList([]string{"Urlaub", "Strand"}, []string{"Strand"}, []string{"Strand"}); ok {
		t.Error("nothing changed, nothing to write")
	}
}

func TestEscE(t *testing.T) {
	if got := escE(" a & b <c>\nd "); got != "&#x20;a &amp; b &lt;c&gt;&#xa;d&#x20;" {
		t.Errorf("escE: %q", got)
	}
	if got := escStruct("Anna, B. {x}"); got != "Anna|, B. {x|}" {
		t.Errorf("escStruct: %q", got)
	}
}

// TestXMPWriteAndReadBack writes descriptions, texts and names into a JPEG
// and a sidecar with the real exiftool, then reads them back into a new
// cache.
func TestXMPWriteAndReadBack(t *testing.T) {
	et := FindExifTool("")
	if et == "" {
		t.Skip("exiftool not installed")
	}
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 40, 30)
	// a keyword and a face region (Bob) from another program
	if out, err := exifToolCmd(et, "-overwrite_original", "-XMP-dc:Subject=Urlaub",
		"-XMP-mwg-rs:RegionInfo={AppliedToDimensions={W=40,H=30,Unit=pixel},RegionList=[{Area={X=0.8,Y=0.2,W=0.1,H=0.1,Unit=normalized},Name=Bob,Type=Face}]}",
		filepath.Join(root, "a.jpg")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "b.avi"), []byte("RIFF....AVI "), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, ExifTool: et, WriteMetadata: true}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := byRel(l, "a.jpg"), byRel(l, "b.avi")
	if a == nil || b == nil {
		t.Fatalf("items: %v %v", a, b)
	}
	l.setAI(a, &AIInfo{De: "Ein Hund & ein Ball.", En: "A dog and a ball.", TagsDe: []string{"hund", "ball"},
		TagsEn: []string{"dog", "ball"}, People: 0, Model: "vision-1", At: 1700000000})
	l.setOCR(a, &OCRInfo{Text: "Zeile 1\n  Zeile 2", Model: "GLM-OCR", At: 1700000001})
	l.setAI(b, &AIInfo{De: "Ein Film.", En: "A film.", Model: "vision-1", At: 1700000002})
	l.setSpeech(b, &SpeechInfo{Text: "Hallo Oma!", Lang: "de", Model: "whisper", At: 1700000003})
	face := Face{X: 0.1, Y: 0.2, W: 0.3, H: 0.4, Score: 0.9, Emb: []float32{1, 0, 0}}
	l.mu.Lock()
	a.Faces, a.FacesDone = []Face{face}, true
	l.mu.Unlock()
	l.people.labels[FaceRef{a.ID, 0}.key()] = "Anna"
	l.recluster()
	if err := l.jobPass(ctx, &l.xmpJob, l.xmpTask()); err != nil {
		t.Fatal(err)
	}
	l.closeXMPTools()
	if d, n, f := l.xmpJob.done.Load(), l.xmpJob.todo.Load(), l.xmpJob.fail.Load(); d != 2 || n != 2 || f != 0 {
		t.Fatalf("written %d of %d, %d failed", d, n, f)
	}
	if a.MetaSig == "" || b.MetaSig == "" {
		t.Fatal("signatures not kept")
	}
	if !exists(filepath.Join(root, "b.avi.xmp")) {
		t.Fatal("no sidecar for the AVI")
	}
	tool, err := startExifTool(et, l.exifCfg)
	if err != nil {
		t.Fatal(err)
	}
	m, err := tool.readOne(filepath.Join(root, "a.jpg"), append([]string{"-XMP-dc:Subject"}, xmpReadTags...))
	tool.close()
	if err != nil {
		t.Fatal(err)
	}
	if got := list(m, "Subject"); !slices.Equal(got, []string{"Urlaub", "hund", "ball"}) {
		t.Errorf("dc:Subject: %v", got)
	}
	if got := str(m, "AltTextAccessibility-de"); got != "Ein Hund & ein Ball." {
		t.Errorf("alt text: %q", got)
	}
	if got := m["SkText"]; got != "Zeile 1\n  Zeile 2" {
		t.Errorf("text: %q", got)
	}
	if _, regs := mwgList(m); len(regs) != 2 || regs[0].name != "Bob" || regs[1].name != "Anna" {
		t.Errorf("regions: %+v", regs)
	}
	// a second pass finds nothing to do
	before := l.xmpJob.done.Load()
	if err := l.jobPass(ctx, &l.xmpJob, l.xmpTask()); err != nil {
		t.Fatal(err)
	}
	if l.xmpJob.done.Load() != before {
		t.Error("written again without a change")
	}
	l.Close()

	// a new cache reads it all back, keeps the user's keywords apart and
	// does not write again
	opt.CacheDir = t.TempDir()
	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if err := l2.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	a2, b2 := byRel(l2, "a.jpg"), byRel(l2, "b.avi")
	if ai := l2.AIOf(a2); ai == nil || ai.De != "Ein Hund & ein Ball." || ai.En != "A dog and a ball." ||
		strings.Join(ai.TagsDe, ",") != "hund,ball" || ai.Model != "vision-1" || ai.At != 1700000000 {
		t.Errorf("AI back: %+v", ai)
	}
	if got := l2.OCRText(a2); got != "Zeile 1\n  Zeile 2" {
		t.Errorf("OCR back: %q", got)
	}
	if !slices.Equal(a2.Keywords, []string{"Urlaub"}) {
		t.Errorf("keywords: %v", a2.Keywords)
	}
	if got := l2.SpeechText(b2); got != "Hallo Oma!" {
		t.Errorf("speech back from the sidecar: %q", got)
	}
	// the name comes back once the face is detected again
	if len(a2.MetaFaces) != 1 || a2.MetaFaces[0].Name != "Anna" {
		t.Fatalf("faces from the file: %+v", a2.MetaFaces)
	}
	// while the name waits for the face, the file stays as it is
	if p := l2.xmpPlanFor(a2); p.sig() != a2.MetaSig {
		t.Errorf("waiting names would be written over: %+v", p)
	}
	l2.mu.Lock()
	a2.Faces, a2.FacesDone = []Face{face}, true
	l2.mu.Unlock()
	if !l2.applyMetaFaces(a2) {
		t.Fatal("face not named from the file")
	}
	l2.recluster()
	if p := l2.Person(l2.PersonOf(a2.ID, 0)); p == nil || p.Name != "Anna" {
		t.Errorf("person back: %+v", p)
	}
	if len(a2.MetaFaces) != 0 {
		t.Errorf("taken over, still waiting: %+v", a2.MetaFaces)
	}
	if p := l2.xmpPlanFor(a2); len(p.Regions) != 1 || len(p.People) != 1 {
		t.Errorf("plan after taking over: %+v", p)
	}
	if err := l2.jobPass(ctx, &l2.xmpJob, l2.xmpTask()); err != nil {
		t.Fatal(err)
	}
	l2.closeXMPTools()
	if d, n := l2.xmpJob.done.Load(), l2.xmpJob.todo.Load(); d != n || n != 2 {
		t.Errorf("after reading back: %d of %d", d, n)
	}
	// a changed description is written over the old one
	l2.setAI(a2, &AIInfo{De: "Eine Katze.", En: "A cat.", TagsDe: []string{"katze"}, Model: "vision-1", At: 1700000100})
	if err := l2.jobPass(ctx, &l2.xmpJob, l2.xmpTask()); err != nil {
		t.Fatal(err)
	}
	l2.closeXMPTools()
	tool, _ = startExifTool(et, l2.exifCfg)
	m, _ = tool.readOne(l2.Path(a2), append([]string{"-XMP-dc:Subject"}, xmpReadTags...))
	tool.close()
	if got := list(m, "Subject"); !slices.Equal(got, []string{"Urlaub", "katze"}) {
		t.Errorf("dc:Subject after a new description: %v", got)
	}
}

func TestXMPPlanRegions(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	it := byRel(l, "a.jpg")
	it.Faces = []Face{{X: 0.1, Y: 0.2, W: 0.2, H: 0.2, Score: 0.9, Emb: []float32{1, 0, 0}}}
	it.Orient = 6 // stored landscape, shown portrait
	it.W, it.H = 30, 40
	l.people.labels[FaceRef{it.ID, 0}.key()] = "Anna"
	l.recluster()
	p := l.xmpPlanFor(it)
	if !slices.Equal(p.People, []string{"Anna"}) || len(p.Regions) != 1 {
		t.Fatalf("plan: %+v", p)
	}
	if p.W != 40 || p.H != 30 {
		t.Errorf("stored size %dx%d", p.W, p.H)
	}
	r := p.Regions[0]
	// shown box x 0.1–0.3, y 0.2–0.4 turned back by 90°: x 0.2–0.4, y 0.7–0.9
	if r.X != 0.3 || r.Y != 0.8 || r.W != 0.2 || r.H != 0.2 {
		t.Errorf("region %+v", r)
	}
	if info, sigs, ok := mergeRegions(&p, map[string]any{}); !ok || len(sigs) != 1 ||
		!strings.Contains(info, "Name=Anna,Type=Face") || !strings.Contains(info, "{W=40,H=30,Unit=pixel}") {
		t.Errorf("mwg: %s %v", info, sigs)
	}
}

// A leftover "_exiftool_tmp" from a stopped write goes; one that is still
// being written stays.
func TestExifToolStaleTmp(t *testing.T) {
	et := FindExifTool("")
	if et == "" {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jpg")
	saveJPEG(t, p, picture(3, 64, 48, 0), 90)
	tool, err := startExifTool(et, "")
	if err != nil {
		t.Fatal(err)
	}
	defer tool.close()
	tmp := p + "_exiftool_tmp"
	os.WriteFile(tmp, []byte("half"), 0o644)
	if err := tool.write(p, []string{"-XMP-dc:Description=frisch"}); err == nil || exists(tmp) == false {
		t.Errorf("a fresh temporary file was removed: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(tmp, old, old)
	if err := tool.write(p, []string{"-XMP-dc:Description=alt"}); err != nil || exists(tmp) {
		t.Errorf("leftover not removed: %v", err)
	}
}

// Videos get a sidecar, the video file itself stays as it is; videos whose
// metadata an older version wrote into them are written again, beside them.
func TestVideoSidecar(t *testing.T) {
	et := FindExifTool("")
	ff, err := exec.LookPath("ffmpeg")
	if et == "" || err != nil {
		t.Skip("exiftool or ffmpeg not installed")
	}
	root := t.TempDir()
	v := filepath.Join(root, "v.mp4")
	if out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=s=160x120:r=10:d=1", "-c:v", "mpeg4", v).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	before, _ := os.Stat(v)
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, ExifTool: et, FFmpeg: ff, WriteMetadata: true}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	it := byRel(l, "v.mp4")
	l.setAI(it, &AIInfo{De: "Ein Testbild.", En: "A test picture.", Model: "vision-1", At: 1700000000})
	if err := l.jobPass(ctx, &l.xmpJob, l.xmpTask()); err != nil {
		t.Fatal(err)
	}
	l.closeXMPTools()
	after, _ := os.Stat(v)
	if !exists(v+".xmp") || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || it.MetaSig == "" {
		t.Fatalf("sidecar %v, video changed %v, sig %q", exists(v+".xmp"), after.Size() != before.Size(), it.MetaSig)
	}
	l.saveIndex()
	l.Close()

	// an index of an older version: the video had its metadata inside
	f, _ := os.Open(filepath.Join(opt.CacheDir, "index.gob.gz"))
	zr, _ := gzip.NewReader(f)
	var idx indexFile
	gob.NewDecoder(zr).Decode(&idx)
	f.Close()
	idx.VideoSidecars = false
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	gob.NewEncoder(zw).Encode(idx)
	zw.Close()
	os.WriteFile(filepath.Join(opt.CacheDir, "index.gob.gz"), buf.Bytes(), 0o644)
	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if it2 := byRel(l2, "v.mp4"); it2 == nil || it2.MetaSig != "" {
		t.Errorf("old video not marked for its sidecar: %+v", it2)
	}
}

// A video an older version wrote into: a new cache takes its descriptions
// over and writes them into a sidecar.
func TestVideoEmbeddedLegacy(t *testing.T) {
	et := FindExifTool("")
	ff, err := exec.LookPath("ffmpeg")
	if et == "" || err != nil {
		t.Skip("exiftool or ffmpeg not installed")
	}
	root := t.TempDir()
	v := filepath.Join(root, "v.mp4")
	if out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=s=160x120:r=10:d=1", "-c:v", "mpeg4", v).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	xmpInPlace[".mp4"] = true // as before
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, ExifTool: et, FFmpeg: ff, WriteMetadata: true}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l.Scan(ctx)
	l.setAI(byRel(l, "v.mp4"), &AIInfo{De: "Ein Testbild.", En: "A test picture.", Model: "vision-1", At: 1700000000})
	l.jobPass(ctx, &l.xmpJob, l.xmpTask())
	l.closeXMPTools()
	l.Close()
	delete(xmpInPlace, ".mp4")
	if exists(v + ".xmp") {
		t.Fatal("sidecar while writing in place")
	}

	opt.CacheDir = t.TempDir() // a new cache
	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	l2.Scan(ctx)
	it := byRel(l2, "v.mp4")
	if ai := l2.AIOf(it); ai == nil || ai.De != "Ein Testbild." || it.MetaSig != "" {
		t.Fatalf("taken over: %+v sig %q", ai, it.MetaSig)
	}
	l2.jobPass(ctx, &l2.xmpJob, l2.xmpTask())
	l2.closeXMPTools()
	if !exists(v+".xmp") || it.MetaSig == "" {
		t.Error("no sidecar written")
	}
}
