package gallery

import (
	"path/filepath"
	"testing"
)

// The people of a selection, and naming one of them in it at once.
func TestPeopleInAndLabelPersonIn(t *testing.T) {
	a := normalize([]float32{1, 0, 0, 0})
	a2 := normalize([]float32{0.95, 0.1, 0, 0})
	b := normalize([]float32{0, 1, 0, 0})
	b2 := normalize([]float32{0.1, 0.95, 0, 0})
	face := func(e []float32) Face { return Face{Emb: e, Score: 0.9, W: .2, H: .2} }
	snap := []faceSnap{
		{id: "i1", taken: 1, faces: []Face{face(a), face(b)}},
		{id: "i2", taken: 2, faces: []Face{face(a2), face(b2)}},
		{id: "i3", taken: 3, faces: []Face{face(a)}},
		{id: "i4", taken: 4, faces: []Face{face(b)}},
	}
	s := newPeopleStore(filepath.Join(t.TempDir(), "people.json"), 0.48, 0.42, 0.75)
	s.labels["i1:0"] = "Anna"
	s.labels["i1:1"] = "Ben"
	s.cluster(snap)
	if s.byFace["i2:0"] != "n-anna" || s.byFace["i2:1"] != "n-ben" {
		t.Fatalf("setup: %v", s.byFace)
	}

	got := s.peopleIn([]string{"i1", "i2", "i3", "nope"})
	if len(got) != 2 || got[0].Name != "Anna" || got[0].Items != 3 || got[0].Faces != 3 || got[1].Name != "Ben" || got[1].Items != 2 {
		t.Fatalf("peopleIn: %+v", got)
	}
	if got := s.peopleIn(nil); got == nil || len(got) != 0 {
		t.Fatalf("no items: %+v", got)
	}

	// Anna in i2 and i3 is Ben: only those faces change, i1 keeps its label
	prev, err := s.labelPersonIn([]string{"i2", "i3"}, "n-anna", "Ben")
	if err != nil {
		t.Fatal(err)
	}
	if len(prev) != 2 || prev["i2:0"] != "" || prev["i3:0"] != "" {
		t.Fatalf("prev: %v", prev)
	}
	if s.labels["i2:0"] != "Ben" || s.labels["i3:0"] != "Ben" || s.labels["i1:0"] != "Anna" {
		t.Fatalf("labels: %v", s.labels)
	}

	// not this person: the face remembers which one it is not
	s.cluster(snap)
	if _, err := s.labelPersonIn([]string{"i4"}, "n-ben", ""); err != nil {
		t.Fatal(err)
	}
	if s.labels["i4:0"] != "!Ben" {
		t.Fatalf("not Ben: %q", s.labels["i4:0"])
	}
	if _, err := s.labelPersonIn([]string{"i4"}, "no-such", "x"); err == nil {
		t.Fatal("unknown person accepted")
	}
}

// "Not this person" is remembered for every person a face was said not to
// be: it joins none of them again, also when a group with it gets that name.
func TestNotThisPersonRemembered(t *testing.T) {
	a := normalize([]float32{1, 0, 0, 0})
	near := normalize([]float32{0.9, 0.3, 0, 0}) // alike Anna and a bit Ben
	b := normalize([]float32{0.3, 0.9, 0, 0})
	face := func(e []float32) Face { return Face{Emb: e, Score: 0.9, W: .2, H: .2} }
	snap := []faceSnap{
		{id: "i1", taken: 1, faces: []Face{face(a)}},
		{id: "i2", taken: 2, faces: []Face{face(b)}},
		{id: "i3", taken: 3, faces: []Face{face(near)}},
	}
	s := newPeopleStore(filepath.Join(t.TempDir(), "people.json"), 0.48, 0.3, 0.75)
	s.labels["i1:0"] = "Anna"
	s.labels["i2:0"] = "Ben"
	s.cluster(snap)
	if s.byFace["i3:0"] != "n-anna" {
		t.Fatalf("setup: %v", s.byFace)
	}
	s.mu.Lock()
	s.setLabel("i3:0", "") // not Anna
	s.mu.Unlock()
	s.cluster(snap)
	if s.byFace["i3:0"] != "n-ben" {
		t.Fatalf("not Anna: joins %q", s.byFace["i3:0"])
	}
	s.mu.Lock()
	s.setLabel("i3:0", "") // not Ben either
	s.mu.Unlock()
	if !rejects(s.labels["i3:0"], "Anna") || !rejects(s.labels["i3:0"], "Ben") {
		t.Fatalf("label %q", s.labels["i3:0"])
	}
	s.cluster(snap)
	if id := s.byFace["i3:0"]; id == "n-anna" || id == "n-ben" {
		t.Fatalf("back with %q", id)
	}
	// its group named Anna: the face stays apart
	pid := s.byFace["i3:0"]
	s.mu.Lock()
	s.byID[pid] = &Person{ID: pid, refs: []FaceRef{{"i3", 0}}}
	s.mu.Unlock()
	if _, err := s.labelPersonIn([]string{"i3"}, pid, "Anna"); err != nil {
		t.Fatal(err)
	}
	if !rejects(s.labels["i3:0"], "Anna") {
		t.Fatalf("named over: %q", s.labels["i3:0"])
	}
	// named by hand: that wins
	s.mu.Lock()
	s.setLabel("i3:0", "Anna")
	s.mu.Unlock()
	if s.labels["i3:0"] != "Anna" {
		t.Fatalf("by hand: %q", s.labels["i3:0"])
	}
}

// A face joins the named person whose most alike faces it is like, also when
// that person's centroid lies between two looks (a child growing up); a
// weak face (unsure detector, small) only when clearly alike.
func TestNamedByNearestFaces(t *testing.T) {
	young := normalize([]float32{1, 0, 0, 0})
	old := normalize([]float32{0, 0, 1, 0})
	ben := normalize([]float32{0.6, 0.1, 0.75, 0.2})
	q := normalize([]float32{0.05, 0.1, 1, 0.1}) // like Anna old
	face := func(e []float32, sc float32) Face { return Face{Emb: e, Score: sc, W: .2, H: .2} }
	snap := []faceSnap{
		{id: "a1", taken: 1, faces: []Face{face(young, .95)}, w: 1000, h: 1000},
		{id: "a2", taken: 2, faces: []Face{face(young, .95)}, w: 1000, h: 1000},
		{id: "a3", taken: 3, faces: []Face{face(old, .95)}, w: 1000, h: 1000},
		{id: "a4", taken: 4, faces: []Face{face(old, .95)}, w: 1000, h: 1000},
		{id: "a5", taken: 4, faces: []Face{face(old, .95)}, w: 1000, h: 1000},
		{id: "b1", taken: 5, faces: []Face{face(ben, .95)}, w: 1000, h: 1000},
		{id: "q", taken: 6, faces: []Face{face(q, .95)}, w: 1000, h: 1000},
	}
	labels := map[string]string{"a1:0": "Anna", "a2:0": "Anna", "a3:0": "Anna", "a4:0": "Anna", "a5:0": "Anna", "b1:0": "Ben"}
	people, _ := buildPeople(snap, labels, .48, .42, .75, false)
	who := func(people []*Person, k string) string {
		for _, p := range people {
			for _, r := range p.refs {
				if r.key() == k {
					return p.Name
				}
			}
		}
		return ""
	}
	// Anna's centroid is about 0.75 like q, Ben's about 0.8: by the centroid q would be Ben
	if got := who(people, "q:0"); got != "Anna" {
		t.Fatalf("q is %q, want Anna", got)
	}
	// weak (unsure detector): about 0.5 alike is not enough
	mid := normalize([]float32{-0.3, -0.3, 0.5, 0.8}) // like Anna old (about 0.48), not like Ben
	if s := knnLikeness(mid, [][]float32{young, young, old, old, old}); s < .42 || s >= .42+weakExtra {
		t.Fatalf("setup: likeness %.2f", s)
	}
	snap[6].faces = []Face{face(mid, .85)}
	people, _ = buildPeople(snap, labels, .48, .42, .75, false)
	if got := who(people, "q:0"); got != "" {
		t.Errorf("weak face joined %q", got)
	}
	snap[6].faces = []Face{face(mid, .95)}
	people, _ = buildPeople(snap, labels, .48, .42, .75, false)
	if got := who(people, "q:0"); got != "Anna" {
		t.Errorf("strong face: %q", got)
	}
	// small (short side under 80 px) counts as weak too
	snap[6].faces = []Face{{Emb: mid, Score: .95, W: .05, H: .05}}
	people, _ = buildPeople(snap, labels, .48, .42, .75, false)
	if got := who(people, "q:0"); got != "" {
		t.Errorf("small face joined %q", got)
	}
}
