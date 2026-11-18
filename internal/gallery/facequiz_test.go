package gallery

import (
	"fmt"
	"testing"
)

// TestQuizRounds: naming shows one face per unnamed group, the largest
// first; a name given with the group's hint names the group (not
// approved); approving then shows those faces, no photo twice.
func TestQuizRounds(t *testing.T) {
	var files []string
	for i := range 7 {
		files = append(files, fmt.Sprintf("p%d.jpg", i))
	}
	l, _ := testLib(t, files...)
	l.faces = &listFaces{}
	x, y := []float32{1, 0, 0, 0}, []float32{0, 1, 0, 0}
	wobble := func(e []float32, k int) []float32 {
		return normalize([]float32{e[0] + .02*float32(k), e[1] + .015*float32(k%3), e[2] + .01*float32(k), e[3]})
	}
	items := map[string]*Item{}
	for _, f := range files {
		items[f] = byRel(l, f)
	}
	l.mu.Lock()
	for i, f := range files {
		e := x // four photos of one person, three of another
		if i >= 4 {
			e = y
		}
		it := items[f]
		it.Faces = []Face{{X: .3, Y: .3, W: .3, H: .3, Score: .99, Emb: wobble(e, i)}}
		it.FacesDone = true
	}
	l.mu.Unlock()
	l.recluster()

	q, err := l.QuizFaces("name", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 2 || q[0].Photos != 4 || q[1].Photos != 3 {
		t.Fatalf("name round: %+v", q)
	}
	// named with the group: the other faces of the group follow, not approved
	first := q[0]
	hints, err := l.HintGroup(first.Person, "Xaver", FaceRef{first.Item, first.Idx})
	if err != nil || len(hints) != 3 {
		t.Fatalf("hints %v %v", hints, err)
	}
	if _, err := l.LabelFace(first.Item, first.Idx, "Xaver"); err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, it := range items {
		if _, name := l.FacePerson(it.ID, 0); name == "Xaver" {
			named++
			if f, _ := l.FaceApproval(it); f[0] && it.ID != first.Item {
				t.Errorf("%s approved by the group's name", it.Rel)
			}
		}
	}
	if named != 4 {
		t.Errorf("%d faces named Xaver, want 4", named)
	}
	if q, _ := l.QuizFaces("name", 5); len(q) != 1 || q[0].Photos != 3 {
		t.Errorf("name round after naming: %+v", q)
	}

	a, err := l.QuizFaces("approve", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) == 0 {
		t.Fatal("nothing to approve")
	}
	seen := map[string]bool{}
	for _, f := range a {
		if f.Name != "Xaver" || f.Item == first.Item || seen[f.Item] {
			t.Errorf("approve round: %+v", f)
		}
		seen[f.Item] = true
	}
	// "no" to the name shown: the face is not that person any more, not approved
	no := a[0]
	if prev, err := l.RejectFace(no.Item, no.Idx, "Xaver"); err != nil || prev != "" {
		t.Fatalf("reject: %q %v", prev, err)
	}
	if _, name := l.FacePerson(no.Item, no.Idx); name == "Xaver" {
		t.Error("rejected face still Xaver")
	}
	if f, _ := l.FaceApproval(l.Get(no.Item)); f[no.Idx] {
		t.Error("rejected face approved")
	}
	if _, err := l.RejectFace(first.Item, first.Idx, "Yvonne"); err == nil {
		t.Error("a face named otherwise said not to be somebody else")
	}
	// undo of the group's hints
	l.RestoreHints(hints)
	for k := range hints {
		var id string
		var i int
		fmt.Sscanf(k, "%12s:%d", &id, &i)
		if h := l.ItemFaces(l.Get(id))[0].Hint; h != "" {
			t.Errorf("hint of %s kept: %q", k, h)
		}
	}
	if _, err := l.QuizFaces("other", 3); err == nil {
		t.Error("unknown mode accepted")
	}
}
