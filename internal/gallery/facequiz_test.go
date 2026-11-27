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
	l, _ := testLib(t, append(files, "lone.jpg", "blur.jpg")...)
	l.faces = &listFaces{}
	x, y := []float32{1, 0, 0, 0}, []float32{0, 1, 0, 0}
	wobble := func(e []float32, k int) []float32 {
		return normalize([]float32{e[0] + .02*float32(k), e[1] + .015*float32(k%3), e[2] + .01*float32(k), e[3]})
	}
	items := map[string]*Item{}
	for _, f := range files {
		items[f] = byRel(l, f)
	}
	lone, blur := byRel(l, "lone.jpg"), byRel(l, "blur.jpg")
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
	// a face alike no other, clear; and one too small to ask for
	lone.W, lone.H = 1000, 1000
	lone.Faces = []Face{{X: .3, Y: .3, W: .3, H: .3, Score: .99, Emb: []float32{0, 0, 1, 0}}}
	blur.W, blur.H = 1000, 1000
	blur.Faces = []Face{{X: .3, Y: .3, W: .02, H: .02, Score: .5, Emb: []float32{0, 0, 0, 1}}}
	lone.FacesDone, blur.FacesDone = true, true
	l.mu.Unlock()
	l.recluster()

	q, err := l.QuizFaces("name", 5)
	if err != nil {
		t.Fatal(err)
	}
	// the groups first, then the round filled with the clear face alike no other
	if len(q) != 3 || q[0].Photos != 4 || q[1].Photos != 3 || q[2].Item != lone.ID || q[2].Person != "" {
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
	if q, _ := l.QuizFaces("name", 5); len(q) != 2 || q[0].Photos != 3 || q[1].Item != lone.ID {
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
	// the next round comes from the ranking kept (no waiting), without the face answered
	l.quizMu.Lock()
	kept := l.quizRank
	l.quizMu.Unlock()
	if kept == nil {
		t.Fatal("no ranking kept")
	}
	for _, f := range l.pickQuiz(kept, 10) {
		if f.Item == no.Item && f.Idx == no.Idx {
			t.Error("face answered comes again")
		}
	}
	if _, err := l.RejectFace(first.Item, first.Idx, "Yvonne"); err == nil {
		t.Error("a face named otherwise said not to be somebody else")
	}
	// the other group is none of the named: all its faces, not approved, matched to nobody,
	// and no more asked about in a naming round
	if q, _ := l.QuizFaces("name", 5); len(q) == 1 {
		y := q[0]
		prev, err := l.RejectNamed(y.Item, y.Idx, true)
		if err != nil || len(prev) != 3 {
			t.Fatalf("none of the named: %v %v", prev, err)
		}
		for k := range prev {
			var id string
			var i int
			fmt.Sscanf(k, "%12s:%d", &id, &i)
			if _, name := l.FacePerson(id, i); name != "" {
				t.Errorf("%s matched to %q", k, name)
			}
			if f, _ := l.FaceApproval(l.Get(id)); f[i] {
				t.Errorf("%s approved", k)
			}
		}
		if q, _ := l.QuizFaces("name", 5); len(q) != 0 {
			t.Errorf("asked again: %+v", q)
		}
		if err := l.RestoreFaceLabels(prev); err != nil {
			t.Fatal(err)
		}
		if q, _ := l.QuizFaces("name", 5); len(q) != 1 {
			t.Errorf("after undo: %+v", q)
		}
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

// TestQuizApproveAlt: a face to approve comes with the other named person it
// is most alike, never one it was said not to be.
func TestQuizApproveAlt(t *testing.T) {
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
	var items []*Item
	for _, f := range files {
		items = append(items, byRel(l, f))
	}
	l.mu.Lock()
	for i, it := range items {
		e := x
		if i >= 4 {
			e = y
		}
		it.Faces = []Face{{X: .3, Y: .3, W: .3, H: .3, Score: .99, Emb: wobble(e, i)}}
		it.FacesDone = true
	}
	l.mu.Unlock()
	l.recluster()
	q, _ := l.QuizFaces("name", 5)
	if len(q) != 2 {
		t.Fatalf("name round: %+v", q)
	}
	for k, name := range []string{"Xaver", "Yvonne"} {
		f := q[k]
		if _, err := l.HintGroup(f.Person, name, FaceRef{f.Item, f.Idx}); err != nil {
			t.Fatal(err)
		}
		if _, err := l.LabelFace(f.Item, f.Idx, name); err != nil {
			t.Fatal(err)
		}
	}
	a, err := l.QuizFaces("approve", 10)
	if err != nil || len(a) == 0 {
		t.Fatalf("approve round: %v %v", a, err)
	}
	for _, f := range a {
		want := map[string]string{"Xaver": "Yvonne", "Yvonne": "Xaver"}[f.Name]
		if f.Alt != want {
			t.Errorf("%s: alt %q, want %q", f.Name, f.Alt, want)
		}
	}
	// said not to be the other one: not offered
	f := a[0]
	l.people.mu.Lock()
	l.people.labels[FaceRef{f.Item, f.Idx}.key()] = "!" + f.Alt
	l.people.mu.Unlock()
	for _, g := range must(l.QuizFaces("approve", 10)) {
		if g.Item == f.Item && g.Alt != "" {
			t.Errorf("alt %q offered although rejected", g.Alt)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
