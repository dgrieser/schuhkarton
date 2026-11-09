package gallery

import (
	"sort"
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
	// the root itself is a year folder
	l2 := &Library{opt: Options{Roots: []Root{{Name: "2025", Path: "/x/Bilder/2025"}}}}
	it = &Item{Rel: "11 November Lübbecke/DSC1.JPG", Kind: Photo, MetaTaken: time.Date(2000, 1, 1, 9, 0, 0, 0, time.UTC).Unix()}
	l2.finishItem(it)
	if it.Place != "Lübbecke" || time.Unix(it.Taken, 0).UTC().Year() != 2025 {
		t.Errorf("year root: place %q taken %v", it.Place, time.Unix(it.Taken, 0).UTC())
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
	s := newPeopleStore("", 0.48, 0.42, 0.75)
	s.labels["i1:0"] = "Anna"
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

func TestDateWords(t *testing.T) {
	l := &Library{opt: Options{Roots: []Root{{Name: "B", Path: "/x"}}}}
	it := &Item{Rel: "2019/07 Juli Lübbecke/a.jpg", Kind: Photo, Taken: time.Date(2019, 7, 6, 19, 30, 0, 0, time.UTC).Unix(), TakenSrc: "exif"}
	d := l.buildDoc(it)
	for _, want := range []string{"samstag", "wochenende", "sommer", "abends", "juli"} {
		if !strings.Contains(d.date, want) {
			t.Errorf("date words %q lack %q", d.date, want)
		}
	}
	for _, q := range []string{"tag:wochenende", "jahreszeit:sommer", "tageszeit:abends", "wochentag:samstag"} {
		terms := parseQuery(q)
		if s, _ := matchTerm(d, "", it, terms[0]); s == 0 {
			t.Errorf("%s does not match", q)
		}
	}
	if s, _ := matchTerm(d, "", it, parseQuery("tag:werktag")[0]); s != 0 {
		t.Error("werktag must not match a Saturday")
	}
	// English syntax and words
	for _, q := range []string{"day:weekend", "season:summer", "time:evening", "weekday:saturday", "month:july"} {
		terms := parseQuery(q)
		if s, _ := matchTerm(d, "", it, terms[0]); s == 0 {
			t.Errorf("%s does not match", q)
		}
	}
	if s, _ := matchTerm(d, "", it, parseQuery("day:workday")[0]); s != 0 {
		t.Error("workday must not match a Saturday")
	}
}

func TestLocalizeQuery(t *testing.T) {
	if got := localizeQuery(`ort:"Porta Westfalica"`, "en"); got != `place:"Porta Westfalica"` {
		t.Errorf("en: %q", got)
	}
	if got := localizeQuery("ort:Lübbecke", "de"); got != "ort:Lübbecke" {
		t.Errorf("de: %q", got)
	}
}

func TestSearchOrWithinField(t *testing.T) {
	l := &Library{opt: Options{Roots: []Root{{Name: "B", Path: "/x"}}}, byID: map[string]*Item{}, favs: map[string]map[string]bool{}}
	l.people = newPeopleStore("", 0.48, 0.42, 0.75)
	mk := func(id, rel string, y int) *Item {
		it := &Item{ID: id, Rel: rel, Kind: Photo, MetaTaken: time.Date(y, 6, 1, 10, 0, 0, 0, time.UTC).Unix()}
		l.finishItem(it)
		return it
	}
	l.setItems([]*Item{mk("a", "2018/06 Juni Lübbecke/a.jpg", 2018), mk("b", "2019/06 Juni Dresden/b.jpg", 2019), mk("c", "2019/06 Juni Lübbecke/c.jpg", 2019)})
	ids := func(q string) string {
		var out []string
		for _, h := range l.Search(q, "", "") {
			out = append(out, h.ID)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := ids("ort:Lübbecke ort:Dresden"); got != "a,b,c" {
		t.Errorf("or within field: %q", got)
	}
	if got := ids("ort:Lübbecke jahr:2019"); got != "c" {
		t.Errorf("and across fields: %q", got)
	}
	if got := ids("lübbecke -jahr:2018"); got != "c" {
		t.Errorf("exclusion: %q", got)
	}
}

func TestIsDocument(t *testing.T) {
	l := &Library{opt: Options{Roots: []Root{{Name: "Bilder", Path: "/x"}, {Name: "Scans", Path: "/y"}}}}
	cases := map[*Item]bool{
		{Root: 0, Rel: "Dokumente/2019/02 Februar Rechnung/a.jpg"}: true,
		{Root: 0, Rel: "2019/06 Juni Lübbecke/a.jpg"}:              false,
		{Root: 1, Rel: "a.jpg"}:                                    true, // the root itself
		{Root: 0, Rel: "dokumente/b.png"}:                          true, // case does not matter
	}
	for it, want := range cases {
		if got := l.IsDocument(it); got != want {
			t.Errorf("%s: %v, want %v", it.Rel, got, want)
		}
	}
	l.opt.DocFolders = []string{}
	if l.IsDocument(&Item{Rel: "Dokumente/a.jpg"}) {
		t.Error("an empty list must switch documents off")
	}
}
