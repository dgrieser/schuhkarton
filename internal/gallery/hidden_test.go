package gallery

import (
	"path/filepath"
	"testing"
)

func TestHideAndShowPerson(t *testing.T) {
	a := normalize([]float32{1, 0, 0, 0})
	a2 := normalize([]float32{0.95, 0.1, 0, 0})
	b := normalize([]float32{0, 1, 0, 0})
	b2 := normalize([]float32{0.1, 0.95, 0, 0})
	c := normalize([]float32{0, 0, 1, 0})
	c2 := normalize([]float32{0, 0.1, 0.95, 0})
	face := func(e []float32) Face { return Face{Emb: e, Score: 0.9, W: .2, H: .2} }
	snap := []faceSnap{
		{id: "i1", taken: 1, faces: []Face{face(a), face(b)}},
		{id: "i2", taken: 2, faces: []Face{face(a2), face(b2)}},
		{id: "i3", taken: 3, faces: []Face{face(a), face(c)}},
		{id: "i4", taken: 4, faces: []Face{face(b), face(c2)}},
		{id: "i5", taken: 5, faces: []Face{face(c)}},
	}
	path := filepath.Join(t.TempDir(), "people.json")
	s := newPeopleStore(path, 0.48, 0.42)
	s.labels["i1:0"] = "Anna"
	s.labels["i2:1"] = "!Anna"     // remembered "not Anna", must survive hide and show
	s.labels["i5:0"] = labelHidden // hidden before groups were remembered
	s.cluster(snap)
	anna := s.byFace["i1:0"]
	group := s.byFace["i1:1"]
	if anna != "n-anna" || group == "" || s.byFace["i5:0"] != "" {
		t.Fatalf("setup: anna=%q group=%q i5=%q", anna, group, s.byFace["i5:0"])
	}
	before := len(s.people)

	hid, err := s.hide(anna)
	if err != nil {
		t.Fatal(err)
	}
	s.cluster(snap)
	if s.byID[anna] != nil || len(s.people) != before-1 {
		t.Fatalf("Anna still shown after hiding: %v", s.byID[anna])
	}
	hidden := s.hiddenGroups(snap)
	if len(hidden) == 0 || hidden[0].ID != hid || hidden[0].Name != "Anna" || hidden[0].Faces != 3 {
		t.Fatalf("hidden list: %+v", hidden)
	}

	// the record survives a restart
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	s = newPeopleStore(path, 0.48, 0.42)
	if err := s.show(snap, hid); err != nil {
		t.Fatal(err)
	}
	s.cluster(snap)
	if s.byFace["i1:0"] != anna || s.labels["i1:0"] != "Anna" || s.labels["i2:1"] != "!Anna" {
		t.Fatalf("Anna not restored: %q labels %v", s.byFace["i1:0"], s.labels)
	}
	if len(s.hidden) != 0 {
		t.Fatalf("record not removed: %+v", s.hidden)
	}
	// the single old hidden face is listed with the loose faces and can come back
	hidden = s.hiddenGroups(snap)
	if len(hidden) != 1 || !hidden[0].Loose || hidden[0].ID != looseID || hidden[0].Faces != 1 {
		t.Fatalf("loose faces: %+v", hidden)
	}
	if err := s.show(snap, looseID); err != nil || s.labels["i5:0"] != "" {
		t.Fatalf("loose face not shown: %v %q", err, s.labels["i5:0"])
	}
	if err := s.show(snap, "h-nope"); err == nil {
		t.Fatal("unknown id accepted")
	}
}

func TestShowHiddenWhilePhotoAway(t *testing.T) {
	a := normalize([]float32{1, 0, 0, 0})
	face := Face{Emb: a, Score: 0.9, W: .2, H: .2}
	snap := []faceSnap{{id: "i1", faces: []Face{face}}, {id: "i2", faces: []Face{face}}, {id: "i3", faces: []Face{face}}}
	s := newPeopleStore("", 0.48, 0.42)
	s.labels["i1:0"] = "Anna"
	s.labels["i3:0"] = "Anna"
	s.cluster(snap)
	hid, err := s.hide("n-anna")
	if err != nil {
		t.Fatal(err)
	}
	// i3 is in the trash while the group is shown again
	if err := s.show(snap[:2], hid); err != nil {
		t.Fatal(err)
	}
	if s.labels["i1:0"] != "Anna" || s.labels["i3:0"] != labelHidden || len(s.hidden) != 1 {
		t.Fatalf("after partial show: labels %v records %+v", s.labels, s.hidden)
	}
	// back from the trash: its face can be shown again with its name
	if err := s.show(snap, hid); err != nil {
		t.Fatal(err)
	}
	if s.labels["i3:0"] != "Anna" || len(s.hidden) != 0 {
		t.Fatalf("after full show: labels %v records %+v", s.labels, s.hidden)
	}
}

func TestShowOldHiddenGroup(t *testing.T) {
	a := normalize([]float32{1, 0, 0, 0})
	face := Face{Emb: a, Score: 0.9, W: .2, H: .2}
	snap := []faceSnap{{id: "i1", faces: []Face{face}}, {id: "i2", faces: []Face{face}}, {id: "i3", faces: []Face{face}}}
	s := newPeopleStore("", 0.48, 0.42)
	for _, k := range []string{"i1:0", "i2:0", "i3:0"} {
		s.labels[k] = labelHidden
	}
	s.cluster(snap)
	if len(s.people) != 0 {
		t.Fatalf("hidden faces shown: %d", len(s.people))
	}
	hidden := s.hiddenGroups(snap)
	if len(hidden) != 1 || hidden[0].Faces != 3 || hidden[0].At != 0 || len(hidden[0].Sample) != 2 {
		t.Fatalf("hidden list: %+v", hidden)
	}
	if err := s.show(snap, hidden[0].ID); err != nil {
		t.Fatal(err)
	}
	s.cluster(snap)
	if len(s.people) != 1 || len(s.labels) != 0 {
		t.Fatalf("not shown again: people %d labels %v", len(s.people), s.labels)
	}
}
