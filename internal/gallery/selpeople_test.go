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
