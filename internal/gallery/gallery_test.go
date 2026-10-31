package gallery

import (
	"strings"
	"testing"
	"time"
)

func TestDateFromName(t *testing.T) {
	cases := map[string]string{
		"IMG_20200813_102822.jpg":                            "2020-08-13 10:28:22",
		"VID-20200110-WA0028.mp4":                            "2020-01-10 12:00:00",
		"Screenshot_2025-06-06-11-58-34-829_org.mozilla.jpg": "2025-06-06 11:58:34",
		"PXL_20210101_101010123.jpg":                         "2021-01-01 10:10:10",
		"IMG_0371.MOV":                                       "",
		"2019-05-28 12.10.20.jpg":                            "2019-05-28 12:10:20",
	}
	for name, want := range cases {
		got := ""
		if ts := dateFromName(name); ts != 0 {
			got = time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05")
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestFinishItemFolder(t *testing.T) {
	l := &Library{opt: Options{Roots: []Root{{Name: "Bilder", Path: "/x"}}}}
	// camera clock reset: the folder wins, the day and time are kept when the month agrees
	it := &Item{Rel: "2025/11 November Lübbecke/DSC00001.JPG", Kind: Photo,
		MetaTaken: time.Date(2000, 1, 1, 9, 0, 0, 0, time.UTC).Unix()}
	l.finishItem(it)
	if it.Place != "Lübbecke" || it.TakenSrc != "folder" || time.Unix(it.Taken, 0).UTC().Month() != time.November {
		t.Errorf("reset clock: place %q src %q taken %v", it.Place, it.TakenSrc, time.Unix(it.Taken, 0).UTC())
	}
	// matching metadata is kept
	it = &Item{Rel: "2018/06 Juni Kamera/a.jpg", Kind: Photo, MetaTaken: time.Date(2018, 6, 4, 17, 13, 0, 0, time.UTC).Unix()}
	l.finishItem(it)
	if it.Place != "" || it.TakenSrc != "exif" {
		t.Errorf("camera folder: place %q src %q", it.Place, it.TakenSrc)
	}
	// collections: the tail is a category, not a place
	it = &Item{Rel: "Dokumente/2016/03 März Rezepte/IMG_1.jpg", Kind: Photo}
	l.finishItem(it)
	if it.Category != "Rezepte" || it.Place != "" {
		t.Errorf("collection: category %q place %q", it.Category, it.Place)
	}
}

func TestFoldAndSnippet(t *testing.T) {
	if f := fold("Lübbecke Straße"); f != "lubbecke strasse" {
		t.Errorf("fold: %q", f)
	}
	if v := germanVariants("luebbecke"); len(v) != 2 || v[1] != "lubbecke" {
		t.Errorf("variants: %v", v)
	}
	s := snippet("Zutaten: 250 g Butter, 100 g Puderzucker, 1 Prise Salz", "zucker")
	if !strings.Contains(s, "Puder\x01zucker\x02") {
		t.Errorf("snippet: %q", s)
	}
}

func TestParseQuery(t *testing.T) {
	terms := parseQuery(`ort:"Porta Westfalica" -video monat:6 kuchen`)
	if len(terms) != 4 {
		t.Fatalf("terms: %+v", terms)
	}
	if terms[0].field != "place" || terms[0].words[0] != "porta westfalica" {
		t.Errorf("place term: %+v", terms[0])
	}
	if !terms[1].neg || terms[2].words[0] != "juni" || terms[3].field != "" {
		t.Errorf("terms: %+v", terms)
	}
}

func TestClusterNamed(t *testing.T) {
	a := normalize([]float32{1, 0, 0, 0})
	a2 := normalize([]float32{0.95, 0.1, 0, 0})
	b := normalize([]float32{0, 1, 0, 0})
	s := &peopleStore{labels: map[string]string{"i1:0": "Anna"}, byID: map[string]*Person{}, byFace: map[string]string{}}
	snap := []faceSnap{
		{id: "i1", faces: []Face{{Emb: a, Score: 0.9, W: .2, H: .2}}},
		{id: "i2", faces: []Face{{Emb: a2, Score: 0.9, W: .2, H: .2}}},
		{id: "i3", faces: []Face{{Emb: b, Score: 0.9, W: .2, H: .2}}},
	}
	s.cluster(snap)
	if s.byFace["i2:0"] != "n-anna" {
		t.Errorf("similar face should join Anna, got %q", s.byFace["i2:0"])
	}
	if s.byFace["i3:0"] != "" {
		t.Errorf("single unrelated face should stay alone, got %q", s.byFace["i3:0"])
	}
}
