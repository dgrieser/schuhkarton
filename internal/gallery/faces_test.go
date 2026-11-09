package gallery

import (
	"fmt"
	"testing"
)

// A person is in a picture once: the most alike face gets the person, the
// others the next best one, unless they are very alike (a collage). Faces
// named by hand and faces at other moments of a video are not limited.
func TestClusterOncePerPicture(t *testing.T) {
	a := normalize([]float32{1, 0, 0, 0})
	a2 := normalize([]float32{0.95, 0.1, 0, 0})    // Anna, very alike
	aCol := normalize([]float32{0.9, 0.2, 0, 0.1}) // Anna, alike enough for a collage
	an := normalize([]float32{0.6, 0, 0, 0.8})     // somewhat like Anna, like nobody else
	ab := normalize([]float32{0.6, 0.5, 0, 0.62})  // somewhat like Anna, a bit less like Ben
	b := normalize([]float32{0, 1, 0, 0})
	face := func(emb []float32) Face { return Face{Emb: emb, Score: 0.9, W: .2, H: .2} }

	s := newPeopleStore("", 0.48, 0.42, 0.75)
	s.labels["i1:0"] = "Anna"
	s.labels["i9:0"] = "Ben"
	s.labels["p4:0"] = "Anna" // two faces named Anna by hand in one picture
	s.labels["p4:1"] = "Anna"
	s.labels["p5:0"] = "Anna"
	snap := []faceSnap{
		{id: "i1", faces: []Face{face(a)}},
		{id: "i9", faces: []Face{face(b)}},
		{id: "p1", faces: []Face{face(ab), face(a2)}},   // the second is more like Anna: the first goes to Ben
		{id: "p2", faces: []Face{face(a2), face(an)}},   // the second has nobody else
		{id: "p3", faces: []Face{face(a2), face(aCol)}}, // a collage
		{id: "p4", faces: []Face{face(a), face(a2)}},    // named by hand
		{id: "p5", faces: []Face{face(a), face(an)}},    // Anna named by hand, an automatic second face
		{id: "v1", faces: []Face{{Emb: an, Score: 0.9, W: .2, H: .2, T: 1}, {Emb: an, Score: 0.9, W: .2, H: .2, T: 5}}}, // a video
	}
	apart, changed := s.cluster(snap)
	want := map[string]string{
		"p1:1": "n-anna", "p1:0": "n-ben",
		"p2:0": "n-anna", "p2:1": "",
		"p3:0": "n-anna", "p3:1": "n-anna",
		"p4:0": "n-anna", "p4:1": "n-anna",
		"p5:0": "n-anna", "p5:1": "",
		"v1:0": "n-anna", "v1:1": "n-anna",
	}
	for k, w := range want {
		if got := s.byFace[k]; got != w {
			t.Errorf("%s: person %q, want %q", k, got, w)
		}
	}
	if apart != 3 || !changed {
		t.Errorf("apart %d (changed %v), want 3 (p1:0, p2:1, p5:1)", apart, changed)
	}
	if apart, changed = s.cluster(snap); apart != 3 || changed {
		t.Errorf("again: apart %d, changed %v", apart, changed)
	}

	// unnamed groups: the face in the picture first (the better one) keeps
	// the group, a second one starts its own; a collage stays together
	c := normalize([]float32{0, 0, 1, 0})
	cc := normalize([]float32{0, 0, 0.6, 0.8})
	big := func(emb []float32) Face { return Face{Emb: emb, Score: 0.9, W: .3, H: .3} }
	small := func(emb []float32) Face { return Face{Emb: emb, Score: 0.9, W: .1, H: .1} }
	s = newPeopleStore("", 0.48, 0.42, 0.75)
	apart, _ = s.cluster([]faceSnap{
		{id: "q1", faces: []Face{big(c), small(cc)}},
		{id: "q2", faces: []Face{big(c)}},
		{id: "q3", faces: []Face{big(c)}},
		{id: "q4", faces: []Face{big(c), small(c)}},
	})
	group := s.byFace["q1:0"]
	if group == "" || s.byFace["q2:0"] != group || s.byFace["q4:0"] != group {
		t.Fatalf("the c faces should form one group: %v", s.byFace)
	}
	if s.byFace["q1:1"] == group {
		t.Error("a second, less alike face of q1 joined the group of the first")
	}
	if s.byFace["q4:1"] != group {
		t.Error("two very alike faces in one picture (a collage) should stay together")
	}
	if apart != 1 {
		t.Errorf("unnamed apart %d, want 1", apart)
	}
}

// The gate uses the group's centroid as it is when the face comes, not as
// it was when the chunk of faces started.
func TestClusterOncePerPictureFreshCentroid(t *testing.T) {
	var snap []faceSnap
	for i := range 512 { // one chunk that makes the group
		snap = append(snap, faceSnap{id: fmt.Sprintf("g%d", i), faces: []Face{{Emb: normalize([]float32{1, 0}), Score: 0.9, W: .3, H: .3}}})
	}
	// two faces of one picture in the next chunk: the first one moves the
	// centroid, so the second is no longer alike enough for a collage
	snap = append(snap, faceSnap{id: "p", faces: []Face{
		{Emb: normalize([]float32{0.6, -0.8}), Score: 0.9, W: .2, H: .2},
		{Emb: normalize([]float32{0.7501, 0.6613}), Score: 0.9, W: .1, H: .1},
	}})
	s := newPeopleStore("", 0.48, 0.42, 0.75)
	s.cluster(snap)
	group := s.byFace["g0:0"]
	if group == "" || s.byFace["p:0"] != group {
		t.Fatalf("p:0 should join the group: %q, %q", s.byFace["p:0"], group)
	}
	if s.byFace["p:1"] == group {
		t.Error("p:1 joined the group although it is less alike than threshSame to its centroid now")
	}
}
