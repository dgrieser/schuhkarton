package gallery

import (
	"context"
	"path/filepath"
	"testing"
)

// Naming a face offers the named people it looks like, best first; a box
// marked by hand gets them before it is saved, and saving it then takes
// the embedding found for the suggestions.
func TestSuggestNames(t *testing.T) {
	root := t.TempDir()
	saveJPEG(t, filepath.Join(root, "2020/IMG_20200101_120000.jpg"), picture(5, 800, 600, 0), 90)
	saveJPEG(t, filepath.Join(root, "2020/IMG_20200102_120000.jpg"), picture(6, 800, 600, 0), 90)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	items := l.Items()
	a, b := items[0], items[1]
	// b: Anna (like what the fake service finds) and Bob (not alike at all)
	l.mu.Lock()
	b.Faces = []Face{
		{X: .1, Y: .1, W: .2, H: .2, Score: .9, Emb: []float32{1, 0}},
		{X: .6, Y: .1, W: .2, H: .2, Score: .9, Emb: []float32{0, 1}},
	}
	l.mu.Unlock()
	if _, err := l.LabelFace(b.ID, 0, "Anna"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.LabelFace(b.ID, 1, "Bob"); err != nil {
		t.Fatal(err)
	}

	box := FaceBox{X: .4, Y: .3, W: .2, H: .25}
	if _, err := l.SuggestForBox(ctx, a.ID, box, 0); err != ErrNoRecognition {
		t.Errorf("without a face service: %v", err)
	}
	fake := &regionFaces{}
	l.faces = fake
	sg, err := l.SuggestForBox(ctx, a.ID, box, 0)
	if err != nil || len(sg) != 1 || sg[0].Name != "Anna" || sg[0].Level != "high" {
		t.Fatalf("suggestions %+v %v", sg, err)
	}
	calls := fake.calls.Load()
	// the same box again: asked once
	if sg, _ := l.SuggestForBox(ctx, a.ID, box, 0); len(sg) != 1 || fake.calls.Load() != calls {
		t.Errorf("asked again: %d calls", fake.calls.Load())
	}
	idx, err := l.AddFace(ctx, a.ID, box, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if f := l.ItemFaces(a)[idx]; f.Emb == nil || f.NoEmb {
		t.Fatalf("the suggestions' embedding was not taken: %+v", f)
	}
	// a face that was saved: the same, from its own embedding
	if sg, err := l.SuggestForFace(a.ID, idx); err != nil || len(sg) != 1 || sg[0].Name != "Anna" {
		t.Errorf("for the face: %+v %v", sg, err)
	}
	// said not to be Anna: Anna is no suggestion any more
	if _, err := l.LabelFace(a.ID, idx, "Anna"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.LabelFace(a.ID, idx, ""); err != nil {
		t.Fatal(err)
	}
	if sg, _ := l.SuggestForFace(a.ID, idx); len(sg) != 0 {
		t.Errorf("a rejected name is suggested: %+v", sg)
	}
	// Anna's other face in b: Anna is in that picture already
	l.mu.Lock()
	b.Faces = append(append([]Face(nil), b.Faces...), Face{X: .4, Y: .6, W: .2, H: .2, Score: .9, Emb: []float32{1, 0}})
	l.mu.Unlock()
	l.recluster()
	if sg, _ := l.SuggestForFace(b.ID, 2); len(sg) != 0 {
		t.Errorf("a name already in the picture is suggested: %+v", sg)
	}
}

func TestSuggestLevels(t *testing.T) {
	s := newPeopleStore(filepath.Join(t.TempDir(), "p.json"), .48, .42, .75)
	l := &Library{people: s, byID: map[string]*Item{}}
	// centroid of Carla's faces at (0.6, 0.8): a face at (1, 0) is 0.6 alike
	s.people = []*Person{{ID: "n-carla", Name: "Carla", cent: []float32{.6, .8}}}
	sg := l.suggest([]float32{1, 0}, nil, frame{"x", 0}, FaceRef{"x", -1}, "")
	if len(sg) != 1 || sg[0].Level != "high" {
		t.Errorf("%+v", sg)
	}
	s.people[0].cent = []float32{.35, .9367}
	if sg := l.suggest([]float32{1, 0}, nil, frame{"x", 0}, FaceRef{"x", -1}, ""); len(sg) != 1 || sg[0].Level != "low" {
		t.Errorf("%+v", sg)
	}
	s.people[0].cent = []float32{0, 1}
	if sg := l.suggest([]float32{1, 0}, nil, frame{"x", 0}, FaceRef{"x", -1}, ""); len(sg) != 0 {
		t.Errorf("%+v", sg)
	}
}
