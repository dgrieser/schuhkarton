package gallery

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestAddFaceByHand(t *testing.T) {
	root := t.TempDir()
	saveJPEG(t, filepath.Join(root, "2020/IMG_20200101_120000.jpg"), picture(5, 800, 600, 0), 90)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	it := l.Items()[0]
	if _, err := l.AddFace(ctx, it.ID, FaceBox{X: .7, Y: .3, W: .5, H: .2}, 0, ""); err == nil {
		t.Error("a box outside the picture was taken")
	}
	idx, err := l.AddFace(ctx, it.ID, FaceBox{X: .4, Y: .3, W: .2, H: .25}, 0, "Anna")
	if err != nil {
		t.Fatal(err)
	}
	faces := l.ItemFaces(it)
	if idx != 0 || len(faces) != 1 || !faces[0].Manual || faces[0].W != .2 {
		t.Fatalf("faces %+v", faces)
	}
	if !exists(l.FacePath(it, idx)) {
		t.Error("no crop")
	}
	// named without an embedding: a member of its person
	p := l.Person(l.PersonOf(it.ID, idx))
	if p == nil || p.Name != "Anna" {
		t.Fatalf("person %+v", p)
	}
	// "no face" takes it out again
	if _, err := l.LabelFace(it.ID, idx, labelNoFace); err != nil || l.PersonOf(it.ID, idx) != "" || !l.IsNoFace(it.ID, idx) {
		t.Errorf("no face: %v %q", err, l.PersonOf(it.ID, idx))
	}
}

func TestMergeFacesKeepsManual(t *testing.T) {
	auto := func(x float32) Face { return Face{X: x, Y: .1, W: .1, H: .1} }
	man := Face{X: .5, Y: .1, W: .1, H: .1, Manual: true}
	old := []Face{auto(.1), man, auto(.8)}
	got, place := mergeFaces(old, []Face{auto(.2), auto(.51), auto(.3), auto(.7)}, nil, .48)
	// manual stays at 1; the found face over it is dropped; others fill 0, 2, then follow
	if len(got) != 4 || !got[1].Manual || got[0].X != .2 || got[2].X != .3 || got[3].X != .7 {
		t.Fatalf("%+v", got)
	}
	if want := []int{0, -1, 2, 3}; fmt.Sprint(place) != fmt.Sprint(want) {
		t.Errorf("places %v, want %v", place, want)
	}
	// names stay with the face where it is: automatic Anna at 0 is covered by
	// manual Anna at 2 now, Bob found again keeps index 1
	anna, bob, manAnna := auto(.1), auto(.4), Face{X: .1, Y: .1, W: .1, H: .1, Manual: true}
	got, place = mergeFaces([]Face{anna, bob, manAnna}, []Face{auto(.1), auto(.4)}, nil, .48)
	if got[1].X != .4 || place[1] != 1 || place[0] != -1 || got[0].X != .1 || len(got) != 3 {
		t.Errorf("geometric places: %+v %v", got, place)
	}
	// a manual face without an embedding takes the one of a face found over it
	withEmb := auto(.5)
	withEmb.Emb = []float32{1, 0}
	if got, _ := mergeFaces([]Face{man}, []Face{withEmb}, nil, .48); len(got) != 1 || got[0].Emb == nil || !got[0].Manual {
		t.Errorf("embedding not lent: %+v", got)
	}
	if needsEmbedding([]Face{man}) || !needsEmbedding([]Face{man, auto(.1)}) {
		t.Error("needsEmbedding")
	}
	// a video: a face marked by hand at 3 s; the same spot at 1 s is
	// somebody else, the same person elsewhere at 5 s is not added again
	manV := Face{X: .1, Y: .1, W: .1, H: .1, Manual: true, T: 3, Emb: []float32{1, 0}}
	other := Face{X: .1, Y: .1, W: .1, H: .1, T: 1, Emb: []float32{0, 1}}
	again := Face{X: .6, Y: .1, W: .1, H: .1, T: 5, Emb: []float32{1, 0}}
	if got, place := mergeFaces([]Face{manV}, []Face{other, again}, nil, .48); len(got) != 2 || got[1].T != 1 || place[1] != -1 || got[0].Emb[0] != 1 {
		t.Errorf("video: %+v %v", got, place)
	}
	// a video scanned again picks other frames: a named face keeps its index,
	// the same person found elsewhere is not added, somebody new follows
	named := Face{X: .1, Y: .1, W: .1, H: .1, T: 2, Emb: []float32{1, 0}}
	unnamed := Face{X: .5, Y: .1, W: .1, H: .1, T: 2, Emb: []float32{0, 1}}
	got, place = mergeFaces([]Face{unnamed, named}, []Face{
		{X: .3, Y: .1, W: .1, H: .1, T: 4, Emb: []float32{1, 0}},   // the named person
		{X: .1, Y: .1, W: .1, H: .1, T: 6, Emb: []float32{0, 1}},   // the other one
		{X: .7, Y: .1, W: .1, H: .1, T: 6, Emb: []float32{.6, .8}}, // somebody new
	}, map[int]bool{1: true}, .9)
	if len(got) != 3 || got[1].T != 2 || place[0] != -1 || place[1] != 0 || place[2] != 2 {
		t.Errorf("video again: %+v %v", got, place)
	}
	// the same spot in the same frame, but clearly two people: both stay
	got, _ = mergeFaces([]Face{{X: .1, Y: .1, W: .1, H: .1, Manual: true, Emb: []float32{1, 0}}},
		[]Face{{X: .1, Y: .1, W: .1, H: .1, Emb: []float32{0, 1}}}, nil, .48)
	if len(got) != 2 {
		t.Errorf("two people on one spot: %+v", got)
	}
	if got, place := mergeFaces([]Face{auto(.1)}, []Face{auto(.4)}, nil, .48); len(got) != 1 || got[0].X != .4 || place[0] != 0 {
		t.Errorf("without manual faces: %+v %v", got, place)
	}
}
