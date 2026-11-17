package gallery

import "testing"

// TestRematchNames: faces not approved get the named person they are most
// alike by the approved faces (as their hint), once per picture; approved
// faces keep theirs, and a face like nobody loses its hint.
func TestRematchNames(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg", "f.jpg")
	l.faces = &listFaces{}
	ada, bob, nobody, other := []float32{1, 0, 0, 0}, []float32{0, 1, 0, 0}, []float32{0, 0, 1, 0}, []float32{0, 0, 0, 1}
	near := func(e []float32) []float32 {
		return []float32{e[0]*.99 + .01, e[1]*.99 + .01, e[2]*.99 + .01, e[3]*.99 + .01}
	}
	box := func(x float32, e []float32, hint string) Face {
		return Face{X: x, Y: .1, W: .3, H: .3, Score: .99, Emb: normalize(e), Hint: hint}
	}
	set := map[string][]Face{
		"a.jpg": {box(0, ada, ""), box(.5, bob, "")},             // named below
		"b.jpg": {box(0, near(ada), "Bob")},                      // wrong hint
		"c.jpg": {box(0, other, "Eve")},                          // approved: stays
		"d.jpg": {box(0, near(ada), ""), box(.5, ada, "")},       // both like Ada: one gets her
		"e.jpg": {box(0, nobody, "Ada")},                         // like nobody: hint goes
		"f.jpg": {box(0, near(bob), ""), box(.5, near(ada), "")}, // Bob there already (named)
	}
	items := map[string]*Item{}
	for rel := range set {
		items[rel] = byRel(l, rel)
	}
	l.mu.Lock()
	for rel, fs := range set {
		items[rel].Faces, items[rel].FacesDone = fs, true
	}
	l.mu.Unlock()
	a, c, f := byRel(l, "a.jpg"), byRel(l, "c.jpg"), byRel(l, "f.jpg")
	for i, n := range []string{"Ada", "Bob"} {
		if _, err := l.LabelFace(a.ID, i, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.LabelFace(f.ID, 0, "Bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApproveFaces(c.ID, []int{0}, false, true); err != nil {
		t.Fatal(err)
	}
	if n := l.RematchCount(); n != 5 {
		t.Fatalf("%d faces to match, want 5", n)
	}
	looked, changed := l.rematch()
	if looked != 5 {
		t.Errorf("looked at %d", looked)
	}
	hint := func(rel string, i int) string { return l.ItemFaces(byRel(l, rel))[i].Hint }
	if h := hint("b.jpg", 0); h != "Ada" {
		t.Errorf("b: %q", h)
	}
	if h := hint("c.jpg", 0); h != "Eve" {
		t.Errorf("approved face changed: %q", h)
	}
	if h0, h1 := hint("d.jpg", 0), hint("d.jpg", 1); h1 != "Ada" || h0 != "" {
		t.Errorf("d: %q %q, the more alike face should get Ada", h0, h1)
	}
	if h := hint("e.jpg", 0); h != "" {
		t.Errorf("e kept %q", h)
	}
	if h := hint("f.jpg", 1); h != "Ada" {
		t.Errorf("f: %q", h)
	}
	if changed != 4 {
		t.Errorf("%d changed, want 4", changed)
	}
	if _, name := l.FacePerson(byRel(l, "b.jpg").ID, 0); name != "Ada" {
		t.Errorf("grouping did not follow: %q", name)
	}
	if _, ok := l.FaceApproval(byRel(l, "b.jpg")); ok {
		t.Error("matching approved a face")
	}
	// nothing changes the second time
	if _, changed := l.rematch(); changed != 0 {
		t.Errorf("second run changed %d", changed)
	}
}
