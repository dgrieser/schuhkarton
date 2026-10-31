package gallery

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestJPEG(t *testing.T, p string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// testLib opens a library over a temporary root with a few photos.
func testLib(t *testing.T, files ...string) (*Library, string) {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		writeTestJPEG(t, filepath.Join(root, f), 40, 30)
	}
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return l, root
}

func byRel(l *Library, rel string) *Item {
	for _, it := range l.Items() {
		if it.Rel == rel {
			return it
		}
	}
	return nil
}

func TestOverridePrecedence(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg")
	it := byRel(l, "2019/06 Juni Lübbecke/a.jpg")
	if it == nil || it.Place != "Lübbecke" {
		t.Fatalf("folder place: %+v", it)
	}
	when := time.Date(2018, 3, 4, 15, 16, 0, 0, time.UTC).Unix()
	place, cat := "Berlin", "Urlaub"
	if _, err := l.EditItems([]string{it.ID}, Edit{Taken: &when, Place: &place, Category: &cat, GPS: &GPSEdit{Lat: 52.5, Lon: 13.4}}); err != nil {
		t.Fatal(err)
	}
	it = byRel(l, "2019/06 Juni Lübbecke/a.jpg")
	if it.Place != "Berlin" || it.Category != "Urlaub" || it.Taken != when || it.TakenSrc != "edit" || !it.GPS || it.Lat != 52.5 {
		t.Fatalf("edit not applied: place=%q cat=%q taken=%d src=%s gps=%v", it.Place, it.Category, it.Taken, it.TakenSrc, it.GPS)
	}
	// a rescan keeps the edit (the folder says June 2019 and Lübbecke)
	l.finishItem(it)
	if it.Place != "Berlin" || it.Taken != when {
		t.Fatalf("rescan lost the edit: %q %d", it.Place, it.Taken)
	}
	if got := l.EditedFields(it); len(got) != 4 {
		t.Errorf("edited fields: %v", got)
	}
	// edits survive a restart
	l2 := &Library{opt: l.opt}
	l2.loadEdits()
	if o := l2.edits["B/2019/06 Juni Lübbecke/a.jpg"]; o == nil || o.Place == nil || *o.Place != "Berlin" {
		t.Fatalf("edits.json: %+v", o)
	}
	// shift moves the time
	if _, err := l.EditItems([]string{it.ID}, Edit{Shift: 3600}); err != nil {
		t.Fatal(err)
	}
	if it = byRel(l, "2019/06 Juni Lübbecke/a.jpg"); it.Taken != when+3600 {
		t.Errorf("shift: %d", it.Taken-when)
	}
}

func TestIgnoreFolders(t *testing.T) {
	l := &Library{opt: Options{Roots: []Root{{Name: "B", Path: "/x"}}, IgnoreFolders: true}}
	it := &Item{Rel: "2019/06 Juni Lübbecke/a.jpg", Kind: Photo, MetaTaken: time.Date(2000, 1, 1, 10, 0, 0, 0, time.UTC).Unix(), MetaPlace: "Bonn"}
	l.finishItem(it)
	if it.Place != "Bonn" || time.Unix(it.Taken, 0).UTC().Year() != 2000 {
		t.Errorf("folders must not count: place=%q year=%d", it.Place, time.Unix(it.Taken, 0).UTC().Year())
	}
	l.opt.IgnoreFolders = false
	it.MetaPlace = "Bonn"
	l.finishItem(it)
	if it.Place != "Lübbecke" || time.Unix(it.Taken, 0).UTC().Year() != 2019 {
		t.Errorf("folder heuristics: place=%q", it.Place)
	}
}

func TestOrientationAndBoxes(t *testing.T) {
	cases := [][3]int{{1, 90, 6}, {6, 90, 3}, {6, 270, 1}, {3, 180, 1}, {8, 90, 1}, {2, 90, 7}, {5, 90, 2}, {1, 270, 8}}
	for _, c := range cases {
		if got := composeOrient(c[0], c[1]); got != c[2] {
			t.Errorf("composeOrient(%d,%d)=%d want %d", c[0], c[1], got, c[2])
		}
	}
	f := Face{X: 0.1, Y: 0.2, W: 0.3, H: 0.1}
	r := rotateBox(f, 90)
	if !near(r.X, 0.7) || !near(r.Y, 0.1) || !near(r.W, 0.1) || !near(r.H, 0.3) {
		t.Errorf("90°: %+v", r)
	}
	back := rotateBox(rotateBox(rotateBox(r, 90), 90), 90)
	if !near(back.X, f.X) || !near(back.Y, f.Y) || !near(back.W, f.W) || !near(back.H, f.H) {
		t.Errorf("4×90° should be identity: %+v", back)
	}
	if b := rotateBox(rotateBox(f, 90), 270); !near(b.X, f.X) || !near(b.Y, f.Y) {
		t.Errorf("90+270: %+v", b)
	}
}

func near(a, b float32) bool { return a-b < 1e-5 && b-a < 1e-5 }

func TestRotateOverride(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	it := byRel(l, "a.jpg")
	if _, err := l.Ensure(context.Background(), it, VarSmall); err != nil {
		t.Fatal(err)
	}
	it.Faces = []Face{{X: 0.1, Y: 0.2, W: 0.3, H: 0.1}}
	oldKey := it.Key()
	w, h := it.W, it.H
	// no exiftool, no writing: the rotation stays in Schuhkarton
	if _, err := l.Rotate([]string{it.ID}, 90, "test"); err != nil {
		t.Fatal(err)
	}
	it = byRel(l, "a.jpg")
	if it.Rot != 90 || it.Key() == oldKey {
		t.Fatalf("rot=%d key changed=%v", it.Rot, it.Key() != oldKey)
	}
	if w != 0 && (it.W != h || it.H != w) {
		t.Errorf("size not swapped: %dx%d -> %dx%d", w, h, it.W, it.H)
	}
	if !near(it.Faces[0].X, 0.7) {
		t.Errorf("face box not turned: %+v", it.Faces[0])
	}
	p := l.CachePath(it, VarSmall)
	img, err := decodeFile(p)
	if err != nil {
		t.Fatalf("turned thumbnail missing: %v", err)
	}
	if b := img.Bounds(); b.Dx() >= b.Dy() {
		t.Errorf("thumbnail not portrait after 90°: %v", b)
	}
	if exists(l.cacheFile(oldKey, VarSmall)) {
		t.Error("old thumbnail left behind")
	}
	// a rescan keeps the rotation and does not swap again
	l.finishItem(it)
	if it.Rot != 90 || (w != 0 && it.W != h) {
		t.Errorf("rescan: rot=%d %dx%d", it.Rot, it.W, it.H)
	}
	if _, err := l.Rotate([]string{it.ID}, 270, "test"); err != nil {
		t.Fatal(err)
	}
	if it = byRel(l, "a.jpg"); it.Rot != 0 || it.Key() != oldKey {
		t.Errorf("back to 0: rot=%d", it.Rot)
	}
}

func TestMoveMigratesState(t *testing.T) {
	l, root := testLib(t, "2019/06 Juni Lübbecke/a.jpg", "Neu/a.jpg")
	it := byRel(l, "2019/06 Juni Lübbecke/a.jpg")
	oldID := it.ID
	l.Ensure(context.Background(), it, VarSmall)
	if err := l.SetFav("anna", []string{it.ID}, true); err != nil {
		t.Fatal(err)
	}
	l.people.labels[oldID+":0"] = "Anna"
	desc := "Hallo"
	l.EditItems([]string{it.ID}, Edit{Desc: &desc})
	os.WriteFile(filepath.Join(root, "2019/06 Juni Lübbecke/a.xmp"), []byte("x"), 0o644)

	res, err := l.Move([]string{it.ID}, "B", "Neu")
	if err != nil || res.Changed != 1 {
		t.Fatalf("move: %v %+v", err, res)
	}
	moved := byRel(l, "Neu/a (2).jpg")
	if moved == nil {
		t.Fatalf("not at the new place; items: %v", relsOf(l))
	}
	if moved.ID == oldID || l.Get(oldID) != nil {
		t.Error("id not updated")
	}
	if !exists(filepath.Join(root, "Neu/a (2).jpg")) || exists(filepath.Join(root, "2019/06 Juni Lübbecke/a.jpg")) {
		t.Error("file not moved")
	}
	if !exists(filepath.Join(root, "Neu/a (2).xmp")) {
		t.Error("sidecar not moved")
	}
	if !l.IsFav("anna", moved) {
		t.Error("favorite lost")
	}
	if l.people.labels[moved.ID+":0"] != "Anna" || l.people.labels[oldID+":0"] != "" {
		t.Errorf("face name not moved: %v", l.people.labels)
	}
	if moved.Desc != "Hallo" {
		t.Errorf("edit lost: %q", moved.Desc)
	}
	if moved.Place != "" {
		t.Errorf("place should follow the new folder: %q", moved.Place)
	}
	if !exists(l.CachePath(moved, VarSmall)) {
		t.Error("thumbnail not carried along")
	}
	if err := l.MakeFolder("B", "../x"); err == nil {
		t.Error("folder outside the root accepted")
	}
}

func relsOf(l *Library) []string {
	var out []string
	for _, it := range l.Items() {
		out = append(out, it.Rel)
	}
	return out
}

func TestTrashRestoreEmpty(t *testing.T) {
	l, root := testLib(t, "x/a.jpg", "x/b.jpg")
	a := byRel(l, "x/a.jpg")
	id := a.ID
	l.Ensure(context.Background(), a, VarSmall)
	l.SetFav("anna", []string{id}, true)
	if _, err := l.Trash([]string{id}, "admin"); err != nil {
		t.Fatal(err)
	}
	if l.Get(id) != nil || exists(filepath.Join(root, "x/a.jpg")) || !exists(filepath.Join(root, TrashDir, "x/a.jpg")) {
		t.Fatal("not in the trash")
	}
	list := l.TrashList()
	if len(list) != 1 || l.TrashThumb(list[0].ID) == "" {
		t.Fatalf("trash list: %+v", list)
	}
	// a rescan does not bring it back
	l.Scan(context.Background())
	if l.Get(id) != nil {
		t.Fatal("trash was scanned")
	}
	if _, err := l.Restore([]string{list[0].ID}); err != nil {
		t.Fatal(err)
	}
	back := l.Get(id)
	if back == nil || !exists(filepath.Join(root, "x/a.jpg")) || !l.IsFav("anna", back) {
		t.Fatal("restore failed")
	}
	if len(l.TrashList()) != 0 {
		t.Fatal("entry left in the trash")
	}
	// delete again and empty for good
	l.Trash([]string{id}, "admin")
	if _, err := l.EmptyTrash(nil); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, TrashDir, "x/a.jpg")) || len(l.TrashList()) != 0 {
		t.Fatal("not emptied")
	}
	if exists(filepath.Join(root, TrashDir)) {
		t.Error("empty trash folder left")
	}
	if l.IsFav("anna", &Item{Root: 0, Rel: "x/a.jpg"}) {
		t.Error("favorite of a deleted file kept")
	}
}
