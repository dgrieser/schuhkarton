package gallery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestQuant2(t *testing.T) {
	a := normalize([]float32{0.3, -0.5, 0.8, 0.1})
	b := normalize([]float32{0.2, -0.4, 0.9, -0.2})
	if d := cos2(quant2(a), quant2(b)) - dot(a, b); d > 0.01 || d < -0.01 {
		t.Errorf("quantized likeness off by %.3f", d)
	}
	if v := unit2(quant2(a)); dot(v, a) < 0.999 {
		t.Errorf("unit2: %v", v)
	}
	if quant2(nil) != nil || cos2(nil, quant2(a)) != -2 {
		t.Error("empty")
	}
}

// fused likeness: both systems where both know the face, one where one does
func TestFuse(t *testing.T) {
	if got := fuse(.4, .4); got < .439 || got > .441 {
		t.Errorf("mean of 0.4 and 1.2*0.4: %.3f", got)
	}
	if fuse(-2, .4) < .479 || fuse(.4, -2) != .4 || fuse(-2, -2) != -2 {
		t.Error("one system")
	}
}

// grouping with both systems: a face both find like Anna joins her; one the
// first system cannot tell from Ben's joins nobody; one only the second
// system knows (a box drawn by hand) joins by it; a face taken in from the
// second search joins its person; an unnamed group both find like Anna goes
// into her.
func TestGroupingWithSecondSystem(t *testing.T) {
	a1, b1 := normalize([]float32{1, 0, 0, 0}), normalize([]float32{0, 1, 0, 0})
	a2, b2 := quant2(normalize([]float32{1, 0.1, 0, 0})), quant2(normalize([]float32{0, 1, 0.1, 0}))
	face := func(e []float32, e2 []int8) Face { return Face{Emb: e, Emb2: e2, Score: .95, W: .2, H: .2} }
	snap := []faceSnap{
		{id: "a1", faces: []Face{face(a1, a2)}, w: 1000, h: 1000},
		{id: "a2", faces: []Face{face(a1, a2)}, w: 1000, h: 1000},
		{id: "a3", faces: []Face{face(a1, a2)}, w: 1000, h: 1000},
		{id: "b1", faces: []Face{face(b1, b2)}, w: 1000, h: 1000},
		{id: "b2", faces: []Face{face(b1, b2)}, w: 1000, h: 1000},
		{id: "b3", faces: []Face{face(b1, b2)}, w: 1000, h: 1000},
		// both like Anna
		{id: "q1", faces: []Face{face(normalize([]float32{0.9, 0.2, 0.3, 0}), a2)}, w: 1000, h: 1000},
		// the first system: Anna and Ben alike; the second: too
		{id: "q2", faces: []Face{face(normalize([]float32{0.6, 0.6, 0.5, 0}), quant2(normalize([]float32{0.6, 0.6, 0.5, 0})))}, w: 1000, h: 1000},
		// only the second system knows it
		{id: "q3", faces: []Face{{Emb2: a2, Manual: true, Score: 1, W: .2, H: .2}}, w: 1000, h: 1000},
		// taken in for Ben, though the first system finds nobody
		{id: "q4", faces: []Face{{Emb: normalize([]float32{0, 0, 1, 0}), Emb2: quant2(normalize([]float32{0, 0, 1, 0})), Hint: "Ben", Score: .7, W: .1, H: .1}}, w: 1000, h: 1000},
	}
	labels := map[string]string{"a1:0": "Anna", "a2:0": "Anna", "a3:0": "Anna", "b1:0": "Ben", "b2:0": "Ben", "b3:0": "Ben"}
	people, _ := buildPeople(snap, labels, .48, .42, .75, true)
	who := map[string]string{}
	for _, p := range people {
		for _, r := range p.refs {
			who[r.key()] = p.Name
		}
	}
	for k, want := range map[string]string{"q1:0": "Anna", "q2:0": "", "q3:0": "Anna", "q4:0": "Ben"} {
		if who[k] != want {
			t.Errorf("%s is %q, want %q", k, who[k], want)
		}
	}
	// said not to be Ben: the hint does not count
	labels["q4:0"] = "!Ben"
	people, _ = buildPeople(snap, labels, .48, .42, .75, true)
	for _, p := range people {
		for _, r := range p.refs {
			if r.key() == "q4:0" && p.Name == "Ben" {
				t.Error("hint over a rejection")
			}
		}
	}
}

func TestMergeIntoNamed(t *testing.T) {
	rec := func(item string, e []float32, label string) *faceRec {
		return &faceRec{ref: FaceRef{item, 0}, emb: e, emb2: e, label: label, frame: frame{item, 0}}
	}
	anna := &cluster{name: "Anna"}
	for _, it := range []string{"a1", "a2"} {
		anna.add(rec(it, normalize([]float32{1, 0, 0}), "Anna"))
	}
	alike := &cluster{}
	for _, it := range []string{"x1", "x2"} {
		alike.add(rec(it, normalize([]float32{0.9, 0.3, 0}), ""))
	}
	other := &cluster{}
	for _, it := range []string{"y1", "y2"} {
		other.add(rec(it, normalize([]float32{0, 1, 0}), ""))
	}
	said := &cluster{}
	for _, it := range []string{"z1", "z2"} {
		said.add(rec(it, normalize([]float32{0.95, 0.2, 0}), "!Anna"))
	}
	kept := mergeIntoNamed([]*cluster{alike, other, said}, []*cluster{anna})
	if len(kept) != 2 || kept[0] != other || kept[1] != said || len(anna.refs) != 4 {
		t.Errorf("kept %d, Anna %d faces", len(kept), len(anna.refs))
	}
}

// fakeExtra finds one face and, asked for it, one more of the second search.
type fakeExtra struct{ aura bool }

func (f *fakeExtra) probe() FaceInfo {
	return FaceInfo{Mode: "fake", Detection: true, Recognition: true, Aura: f.aura, Extra: true}
}
func (f *fakeExtra) detect(ctx context.Context, l *Library, it *Item, prefix string, extra bool) ([]Face, error) {
	out := []Face{{X: .1, Y: .1, W: .2, H: .2, Score: .95, Emb: normalize([]float32{1, 0}), Emb2: quant2([]float32{1, 0, 0})}}
	if extra {
		out = append(out, Face{X: .6, Y: .1, W: .1, H: .1, Score: .7, Emb: normalize([]float32{0, 1}), Emb2: quant2([]float32{0, 1, 0}), Src: "retina"})
	}
	for i := range out {
		os.WriteFile(fmt.Sprintf("%s_%d.jpg", prefix, i), []byte{byte(i)}, 0o644)
	}
	return out, nil
}
func (f *fakeExtra) detectData(ctx context.Context, l *Library, data []byte, box *[4]float32) ([]Face, error) {
	return nil, nil
}

// A scan keeps the faces of the second search aside, with crops of their
// own; once the second system knows whose a face is, it is taken in.
func TestExtraFacesTakenIn(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg", "c.jpg", "d.jpg")
	l.faces = &fakeExtra{aura: true}
	ctx := context.Background()
	for _, it := range l.Items() {
		if !l.scanFaces(ctx, it) {
			t.Fatal("not scanned")
		}
	}
	a := byRel(l, "a.jpg")
	if len(a.Faces) != 1 || len(a.Extra) != 1 || a.FaceGen != genAura|genExtra {
		t.Fatalf("faces %d extra %d gen %d", len(a.Faces), len(a.Extra), a.FaceGen)
	}
	if b, err := os.ReadFile(l.ExtraPath(a, 0)); err != nil || len(b) != 1 || b[0] != 1 {
		t.Errorf("extra crop: %v %v", b, err)
	}
	if b, _ := os.ReadFile(l.FacePath(a, 0)); len(b) != 1 || b[0] != 0 {
		t.Errorf("main crop: %v", b)
	}
	// nobody named yet: nothing taken in
	if l.promoteExtras(true) {
		t.Fatal("taken in without a named person")
	}
	// the extras of b, c, d are named Cleo (as found faces moved there by hand: here, as labels of real faces)
	for _, rel := range []string{"b.jpg", "c.jpg", "d.jpg"} {
		it := byRel(l, rel)
		l.mu.Lock()
		it.Faces = append(it.Faces, it.Extra[0])
		it.Extra = nil
		l.mu.Unlock()
		if _, err := l.LabelFace(it.ID, 1, "Cleo"); err != nil {
			t.Fatal(err)
		}
	}
	l.recluster() // a face was named: taken in at once
	a = byRel(l, "a.jpg")
	if len(a.Faces) != 2 || a.Faces[1].Hint != "Cleo" || len(a.Extra[0].Emb2) != 0 {
		t.Fatalf("a: %+v / %+v", a.Faces, a.Extra)
	}
	if b, _ := os.ReadFile(l.FacePath(a, 1)); len(b) != 1 || b[0] != 1 {
		t.Errorf("crop of the taken face: %v", b)
	}
	l.recluster()
	if p := l.Person(l.PersonOf(a.ID, 1)); p == nil || p.Name != "Cleo" {
		t.Errorf("taken face is %+v", p)
	}
	// once more: nothing new
	if l.promoteExtras(true) {
		t.Error("taken in twice")
	}
}

// boxFaces finds no face; asked with a box it embeds the box itself
type boxFaces struct{ fakeExtra }

func (f *boxFaces) detectData(ctx context.Context, l *Library, data []byte, box *[4]float32) ([]Face, error) {
	if box == nil {
		return nil, nil
	}
	return []Face{{X: box[0], Y: box[1], W: box[2], H: box[3], Score: 1, Emb: normalize([]float32{1, 0}), Emb2: quant2([]float32{0, 0, 1}), Src: "box"}}, nil
}

// A face drawn by hand where no search finds one gets the second system's
// embedding of the box itself (the first system needs the face's points).
func TestManualBoxSecondOnly(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	l.faces = &boxFaces{fakeExtra{aura: true}}
	a := byRel(l, "a.jpg")
	emb, emb2, err := l.manualEmbedding(context.Background(), a, Face{X: .3, Y: .3, W: .3, H: .3, Manual: true})
	if err != nil || emb != nil || len(emb2) != 3 {
		t.Fatalf("emb %v emb2 %v err %v", emb, emb2, err)
	}
	var f Face
	setManualEmb(&f, emb, emb2, true)
	if !f.NoEmb || f.NoEmb2 || f.Emb2 == nil || f.wantsEmb(true) {
		t.Errorf("%+v", f)
	}
}

// What goes to the face container: the large preview when it is there, else
// the original as it is, else a JPEG made here (a photo turned by hand).
func TestFaceUpload(t *testing.T) {
	l, root := testLib(t, "a.jpg", "b.jpg", "c.jpg")
	ctx := context.Background()
	a, b, c := byRel(l, "a.jpg"), byRel(l, "b.jpg"), byRel(l, "c.jpg")
	orig, _ := os.ReadFile(root + "/b.jpg")
	if got, name, err := l.faceUpload(ctx, b); err != nil || name != "face.jpg" || len(got) != len(orig) {
		t.Errorf("original: %d bytes %q %v, want %d", len(got), name, err, len(orig))
	}
	p := l.CachePath(a, VarLarge)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("preview"), 0o644)
	if got, _, _ := l.faceUpload(ctx, a); string(got) != "preview" {
		t.Errorf("preview not taken: %d bytes", len(got))
	}
	if _, err := l.Rotate([]string{c.ID}, 90, "anna"); err != nil {
		t.Fatal(err)
	}
	c = byRel(l, "c.jpg")
	if got, _, err := l.faceUpload(ctx, c); err != nil || len(got) == len(orig) {
		t.Errorf("turned photo sent as it is: %d bytes %v", len(got), err)
	}
}
