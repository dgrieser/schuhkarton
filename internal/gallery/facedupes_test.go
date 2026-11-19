package gallery

import (
	"context"
	"testing"
)

func TestDuplicateFaces(t *testing.T) {
	ada, bob := normalize([]float32{1, 0.1, 0}), normalize([]float32{0.1, 1, 0})
	ada2 := normalize([]float32{1, 0.3, 0.1})
	q := func(v ...float32) []int8 { return quant2(v) }
	box := func(x, y, w, h float32) Face { return Face{X: x, Y: y, W: w, H: h, Score: .9} }
	main := box(.30, .30, .20, .25)
	main.Emb, main.Emb2 = ada, q(1, 0, 0)
	shifted := box(.38, .32, .20, .28) // the second search: shifted, taller, the same face
	shifted.Src, shifted.Emb, shifted.Emb2 = "yunet+retina", ada2, q(.9, .3, 0)
	cheek := box(.47, .30, .20, .25) // another person close by
	cheek.Emb, cheek.Emb2 = bob, q(0, 1, 0)
	drawn := box(.25, .25, .32, .36) // drawn by hand around a found face, no embedding yet
	drawn.Manual = true
	inner := box(.31, .31, .18, .22)

	for _, c := range []struct {
		name string
		a, b Face
		want bool
	}{
		{"shifted, alike", main, shifted, true},
		{"close by, not alike", main, cheek, false},
		{"drawn around", drawn, inner, true},
		{"other moment of a video", main, func() Face { f := shifted; f.T = 3; return f }(), false},
		{"apart", main, box(.7, .3, .2, .25), false},
	} {
		if got := dupFace(c.a, c.b); got != c.want {
			s, r := boxShare(c.a, c.b)
			t.Errorf("%s: %v, want %v (share %.2f ratio %.1f)", c.name, got, c.want, s, r)
		}
	}

	// the one less worth keeping goes; named differently stays
	faces := []Face{shifted, main, cheek}
	if d := duplicates(faces, nil); len(d) != 1 || d[0] != 0 {
		t.Errorf("drop %v, want the second search's face", d)
	}
	if d := duplicates(faces, map[int]string{0: "Ada"}); len(d) != 1 || d[0] != 1 {
		t.Errorf("drop %v, want the face not named", d)
	}
	if d := duplicates(faces, map[int]string{0: "Ada", 1: "Bea"}); len(d) != 0 {
		t.Errorf("drop %v, named differently by hand", d)
	}
	if d := duplicates(faces, map[int]string{0: labelNoFace}); len(d) != 0 {
		t.Errorf("drop %v, put aside already", d)
	}
	if d := duplicates([]Face{inner, drawn}, nil); len(d) != 1 || d[0] != 0 {
		t.Errorf("drop %v, want the found face inside the drawn box", d)
	}

	// in a library: put aside as no face when grouping, once
	l, _ := testLib(t, "a.jpg")
	l.faces = &listFaces{}
	a := byRel(l, "a.jpg")
	l.mu.Lock()
	a.Faces, a.FacesDone = []Face{main, shifted, cheek}, true
	l.mu.Unlock()
	l.recluster()
	if !l.IsNoFace(a.ID, 1) || l.IsNoFace(a.ID, 0) || l.IsNoFace(a.ID, 2) {
		t.Error("the face found twice should be no face, only that one")
	}
	if n := l.dropDuplicateFaces(); n != 0 {
		t.Errorf("dropped %d again", n)
	}
	// undone in the history: the face stays, also when grouped again
	ch, _, _ := l.History("", 10, "", "")
	id := ""
	for _, c := range ch {
		if c.Info["op"] == "dupes" {
			id = c.ID
		}
	}
	if id == "" {
		t.Fatal("not in the history")
	}
	if _, err := l.UndoChange(id, "tester"); err != nil {
		t.Fatal(err)
	}
	l.recluster()
	if l.IsNoFace(a.ID, 1) || !l.ItemFaces(a)[1].NotDup {
		t.Error("undone, but put aside again")
	}

	// two found boxes inside each other, clearly not alike: two faces
	front := box(.32, .32, .16, .2)
	front.Emb, front.Emb2 = bob, q(0, 1, 0)
	if dupFace(main, front) {
		t.Error("a child in front of a grown-up taken for the same face")
	}
	// keepers first: B is the twin of A and of C, A and C are not twins: only B goes
	A, B, C := box(.1, .3, .2, .25), box(.18, .3, .2, .25), box(.26, .3, .2, .25)
	A.Emb, B.Emb, C.Emb = normalize([]float32{1, 0, 0}), normalize([]float32{1, .6, 0}), normalize([]float32{.3, 1, 0})
	A.Emb2, B.Emb2, C.Emb2 = q(1, 0, 0), q(1, .5, 0), q(.4, 1, 0)
	B.Src = "retina"
	if dupFace(A, C) {
		t.Fatal("A and C should not be twins")
	}
	if d := duplicates([]Face{A, B, C}, nil); len(d) != 1 || d[0] != 1 {
		t.Errorf("drop %v, want only B", d)
	}
	// "not this person" of the twin's name: stays
	if d := duplicates([]Face{main, shifted}, map[int]string{0: "Ada", 1: "!Ada"}); len(d) != 0 {
		t.Errorf("drop %v, said not to be Ada", d)
	}
	// a rescan finds both again: the twin is decided afresh, put aside again, no face more
	{
		l2, _ := testLib(t, "b.jpg")
		named := main
		named.OK = true
		again := shifted
		again.Src = "" // both from the main search here
		l2.faces = &listFaces{found: []Face{main, again}}
		b := byRel(l2, "b.jpg")
		l2.scanFaces(context.Background(), b)
		l2.mu.Lock()
		b.Faces[0].OK = true
		l2.mu.Unlock()
		l2.recluster()
		for range 2 {
			l2.mu.Lock()
			b.FaceRedo = true
			l2.mu.Unlock()
			l2.scanFaces(context.Background(), b)
			l2.recluster()
		}
		fs := l2.ItemFaces(b)
		if len(fs) != 2 || !fs[1].Twin || !l2.IsNoFace(b.ID, 1) || l2.IsNoFace(b.ID, 0) {
			t.Errorf("after rescans: %d faces, twin %v, no face %v/%v", len(fs), len(fs) > 1 && fs[1].Twin, l2.IsNoFace(b.ID, 0), l2.IsNoFace(b.ID, 1))
		}
	}
	// approved as a whole: nothing goes
	b2 := byRel(l, "a.jpg")
	l.mu.Lock()
	b2.Faces = []Face{main, shifted}
	b2.FacesOK = true
	l.mu.Unlock()
	if n := l.dropDuplicateFaces(); n != 0 {
		t.Errorf("dropped %d of a photo approved as a whole", n)
	}
}
