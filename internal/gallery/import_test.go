package gallery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func importLib(t *testing.T, files ...string) (*Library, string, string) {
	t.Helper()
	l, root := testLib(t, files...)
	dir := t.TempDir()
	l.opt.Import = ImportOptions{Dir: dir, Every: time.Hour, Settle: time.Nanosecond}
	return l, root, dir
}

func put(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if filepath.Ext(rel) == ".jpg" || filepath.Ext(rel) == ".JPG" {
		writeTestJPEG(t, p, 40, 30)
		return
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportWithoutRules(t *testing.T) {
	l, root, dir := importLib(t, "b.jpg")
	put(t, dir, "Urlaub 2026/a.jpg")
	put(t, dir, "b.jpg")     // the name is taken in the library
	put(t, dir, "IMG_1.JPG") // a Live Photo pair keeps one name
	put(t, dir, "IMG_1.MOV")
	put(t, dir, "IMG_1.AAE")    // sidecar
	put(t, dir, "liesmich.txt") // no photo: stays
	put(t, dir, "neu.jpg.part") // still downloading: stays
	ctx := context.Background()
	if err := l.importOnce(ctx); err != nil { // first look: not ready yet
		t.Fatal(err)
	}
	if st := l.ImportInfo(); st.Waiting != 4 || st.Done != 0 || st.Other != 1 {
		t.Fatalf("first look: %+v", st)
	}
	if err := l.importOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"Urlaub 2026/a.jpg", "b (2).jpg", "IMG_1.JPG", "IMG_1.MOV", "IMG_1.AAE"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s not in the library: %v", rel, err)
		}
	}
	if byRel(l, "Urlaub 2026/a.jpg") == nil || byRel(l, "b (2).jpg") == nil {
		t.Fatalf("imported files not indexed: %v", rels(l))
	}
	if _, err := os.Stat(filepath.Join(dir, "Urlaub 2026")); !os.IsNotExist(err) {
		t.Errorf("emptied subfolder left: %v", err)
	}
	for _, rel := range []string{"liesmich.txt", "neu.jpg.part"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s should stay: %v", rel, err)
		}
	}
	if st := l.ImportInfo(); st.Done != 4 || st.Waiting != 0 {
		t.Fatalf("status: %+v", st)
	}
}

func TestImportWithRules(t *testing.T) {
	l, root, dir := importLib(t)
	// auto off: imports are filed anyway
	l.SetRules(RuleSet{Locale: "de", Auto: false, List: []Rule{{Path: "Sortiert/{jahr}"}}})
	put(t, dir, "Handy/x.jpg")
	ctx := context.Background()
	l.importOnce(ctx)
	if err := l.importOnce(ctx); err != nil {
		t.Fatal(err)
	}
	waitRules(t, l)
	year := time.Now().Format("2006")
	if byRel(l, "Sortiert/"+year+"/x.jpg") == nil {
		t.Fatalf("not filed by the rules: %v", rels(l))
	}
	if _, err := os.Stat(filepath.Join(root, "Handy")); !os.IsNotExist(err) {
		t.Errorf("first landing folder left: %v", err)
	}
}

func TestImportWaitsForWriting(t *testing.T) {
	l, root, dir := importLib(t)
	put(t, dir, "a.jpg")
	ctx := context.Background()
	l.importOnce(ctx)
	// the file grows between two looks: it stays
	f, _ := os.OpenFile(filepath.Join(dir, "a.jpg"), os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte("more"))
	f.Close()
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(root, "a.jpg")); !os.IsNotExist(err) {
		t.Fatal("moved while still being written")
	}
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(root, "a.jpg")); err != nil {
		t.Fatalf("not moved once complete: %v", err)
	}
}

func TestImportRefusesLibraryLink(t *testing.T) {
	l, root, _ := importLib(t, "a.jpg")
	link := filepath.Join(t.TempDir(), "import")
	if err := os.Symlink(root, link); err != nil {
		t.Skip(err)
	}
	l.opt.Import.Dir = link
	ctx := context.Background()
	l.importOnce(ctx)
	if err := l.importOnce(ctx); err == nil {
		t.Fatal("imported from the library itself")
	}
	if byRel(l, "a.jpg") == nil {
		t.Fatalf("library file renamed: %v", rels(l))
	}
}

func TestImportSidecars(t *testing.T) {
	l, root, dir := importLib(t)
	os.WriteFile(filepath.Join(root, "a.xmp"), []byte("orphan"), 0o644) // a taken sidecar name
	put(t, dir, "a.jpg")
	put(t, dir, "a.xmp")
	put(t, dir, "b.jpg")
	put(t, dir, "b.xmp")
	ctx := context.Background()
	l.importOnce(ctx)
	// b's sidecar is still being written: b waits, a goes
	f, _ := os.OpenFile(filepath.Join(dir, "b.xmp"), os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte("more"))
	f.Close()
	l.importOnce(ctx)
	for _, rel := range []string{"a (2).jpg", "a (2).xmp", "a.xmp"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "b.jpg")); !os.IsNotExist(err) {
		t.Error("b moved while its sidecar was being written")
	}
	l.importOnce(ctx)
	for _, rel := range []string{"b.jpg", "b.xmp"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestImportStaysInLibrary(t *testing.T) {
	l, root, dir := importLib(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Ausland")); err != nil {
		t.Skip(err)
	}
	put(t, dir, "Ausland/x.jpg")
	put(t, dir, "Ausland/Tief/y.jpg")
	ctx := context.Background()
	l.importOnce(ctx)
	if err := l.importOnce(ctx); err == nil {
		t.Fatal("no error for a destination outside the library")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.jpg")); !os.IsNotExist(err) {
		t.Fatal("file moved out of the library through a link")
	}
	if _, err := os.Stat(filepath.Join(dir, "Ausland/x.jpg")); err != nil {
		t.Fatalf("file should stay in the import folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "Tief")); !os.IsNotExist(err) {
		t.Fatal("folder created outside the library")
	}
}

func TestImportReportsUnreadableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	l, _, dir := importLib(t)
	put(t, dir, "zu/x.jpg")
	os.Chmod(filepath.Join(dir, "zu"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "zu"), 0o755) })
	if err := l.importOnce(context.Background()); err == nil {
		t.Fatal("unreadable folder not reported")
	}
}

func TestImportPairAcrossPasses(t *testing.T) {
	l, root, dir := importLib(t)
	os.WriteFile(filepath.Join(root, "IMG_1.MOV"), []byte("other video"), 0o644)
	ctx := context.Background()
	put(t, dir, "IMG_1.JPG") // the photo comes first …
	l.importOnce(ctx)
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(root, "IMG_1 (2).JPG")); err != nil {
		t.Fatalf("photo should not pair with the other video: %v", err)
	}
	// a restart in between: the choice comes back from import.json
	l.saveImportStems()
	l.importJob.stems = nil
	l.loadImportStems()
	put(t, dir, "IMG_1.MOV") // … its video later
	l.importOnce(ctx)
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(root, "IMG_1 (2).MOV")); err != nil {
		t.Fatalf("video did not join its photo: %v", err)
	}
}

func TestImportSkipsLinks(t *testing.T) {
	l, root, dir := importLib(t)
	secret := filepath.Join(t.TempDir(), "geheim.txt")
	os.WriteFile(secret, []byte("geheim"), 0o600)
	if err := os.Symlink(secret, filepath.Join(dir, "secret.jpg")); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	l.importOnce(ctx)
	l.importOnce(ctx)
	if _, err := os.Lstat(filepath.Join(root, "secret.jpg")); !os.IsNotExist(err) {
		t.Fatal("a link was imported")
	}
}

func TestImportLateHalves(t *testing.T) {
	l, root, dir := importLib(t)
	l.SetRules(RuleSet{Locale: "de", List: []Rule{{Path: "Sortiert"}}})
	ctx := context.Background()
	put(t, dir, "IMG_7.JPG")
	l.importOnce(ctx)
	l.importOnce(ctx)
	waitRules(t, l)
	if byRel(l, "Sortiert/IMG_7.JPG") == nil {
		t.Fatalf("photo not filed: %v", rels(l))
	}
	// its video and a sidecar come later: they join the filed photo
	put(t, dir, "IMG_7.MOV")
	put(t, dir, "IMG_7.AAE")
	l.importOnce(ctx)
	l.importOnce(ctx)
	waitRules(t, l)
	for _, rel := range []string{"Sortiert/IMG_7.MOV", "Sortiert/IMG_7.AAE"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s did not join its photo: %v", rel, err)
		}
	}
	// a sidecar on its own, even later
	put(t, dir, "IMG_7.jpg.xmp") // other case than the photo
	l.importOnce(ctx)
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(root, "Sortiert/IMG_7.JPG.xmp")); err != nil {
		t.Errorf("late sidecar did not join its photo: %v", err)
	}
}

func TestImportSkipsLinkedSidecar(t *testing.T) {
	l, root, dir := importLib(t)
	secret := filepath.Join(t.TempDir(), "geheim.txt")
	os.WriteFile(secret, []byte("geheim"), 0o600)
	put(t, dir, "a.jpg")
	if err := os.Symlink(secret, filepath.Join(dir, "a.xmp")); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	l.importOnce(ctx)
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(root, "a.jpg")); err != nil {
		t.Fatalf("photo not imported: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "a.xmp")); !os.IsNotExist(err) {
		t.Fatal("a linked sidecar was imported")
	}
}

func TestImportCaseOnlyNames(t *testing.T) {
	l, root, dir := importLib(t)
	put(t, dir, "a.jpg")
	put(t, dir, "A.jpg")
	if _, err := os.Stat(filepath.Join(dir, "A.jpg")); err != nil {
		t.Skip("file system ignores case")
	}
	ctx := context.Background()
	l.importOnce(ctx)
	if err := l.importOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.jpg", "A.jpg"} {
		if _, err := os.Stat(filepath.Join(root, n)); err != nil {
			t.Fatalf("both files should be imported: %v", err)
		}
	}
	if es2, _ := os.ReadDir(dir); len(es2) != 0 {
		t.Fatalf("left in the import folder: %v", es2)
	}
}

func TestImportFolderIsLink(t *testing.T) {
	l, root, dir := importLib(t)
	link := filepath.Join(t.TempDir(), "import")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip(err)
	}
	l.opt.Import.Dir = link
	put(t, dir, "Sub/a.jpg")
	ctx := context.Background()
	l.importOnce(ctx)
	if err := l.importOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Sub/a.jpg")); err != nil {
		t.Fatalf("not imported through the link: %v", err)
	}
	// a late sidecar in a subfolder: imported, and the folder goes
	put(t, dir, "Sub/a.xmp")
	l.importOnce(ctx)
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(root, "Sub/a.xmp")); err != nil {
		t.Fatalf("late sidecar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Sub")); !os.IsNotExist(err) {
		t.Fatalf("empty subfolder left after the sidecar: %v", err)
	}
}

func TestImportLateHalfOtherRoot(t *testing.T) {
	l, _, dir := importLib(t)
	second := t.TempDir()
	l.opt.Roots = append(l.opt.Roots, Root{Name: "Zwei", Path: second})
	ctx := context.Background()
	put(t, dir, "IMG_9.JPG")
	l.importOnce(ctx)
	l.importOnce(ctx)
	photo := byRel(l, "IMG_9.JPG")
	if photo == nil {
		t.Fatalf("photo not imported: %v", rels(l))
	}
	if _, err := l.Move([]string{photo.ID}, "Zwei", "Album"); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "IMG_9.MOV")
	l.importOnce(ctx)
	l.importOnce(ctx)
	if _, err := os.Stat(filepath.Join(second, "Album/IMG_9.MOV")); err != nil {
		t.Fatalf("video did not follow its photo to the other folder: %v", err)
	}
}

func TestImportRefusesLinkInsideLibrary(t *testing.T) {
	l, root, dir := importLib(t)
	os.MkdirAll(filepath.Join(root, "Echt"), 0o755)
	if err := os.Symlink(filepath.Join(root, "Echt"), filepath.Join(root, "Verweis")); err != nil {
		t.Skip(err)
	}
	put(t, dir, "Verweis/a.jpg")
	ctx := context.Background()
	l.importOnce(ctx)
	if err := l.importOnce(ctx); err == nil {
		t.Fatal("no error for a destination through a link")
	}
	if _, err := os.Stat(filepath.Join(root, "Echt/a.jpg")); !os.IsNotExist(err) {
		t.Fatal("file moved through a link")
	}
}
