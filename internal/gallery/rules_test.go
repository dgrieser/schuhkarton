package gallery

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// the layout the library has today, as rules
var layoutRules = RuleSet{Locale: "de", Auto: true, List: []Rule{
	{Name: "Dokumente", When: "typ:dokument", Path: "Dokumente/{jahr}/{datum:MM MMMM} {kategorie}"},
	{Name: "Fotos", Path: "{jahr}/{datum:MM MMMM} {ort|Kamera}"},
}}

func TestRulesKeepMatchingLayout(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg", "2019/06 Juni Kamera/b.jpg", "Dokumente/2021/03 März Rechnung/c.jpg")
	if err := l.SetRules(layoutRules); err != nil {
		t.Fatal(err)
	}
	p := l.PreviewRules(layoutRules)
	if p.Moves != 0 || p.Total != 3 || p.Rules[0].Matches != 1 || p.Rules[1].Matches != 2 {
		t.Fatalf("the current layout should stay: %+v", p)
	}
	// a rule without place moves the photos; the place from the folder stays
	flat := RuleSet{Locale: "en", Auto: true, List: []Rule{{When: "-typ:dokument", Path: "{year}/{date:MMMM}"}}}
	p = l.PreviewRules(flat)
	if p.Moves != 2 || p.Unmatched != 1 || p.Rules[0].Examples[0].To == "" {
		t.Fatalf("flat preview: %+v", p)
	}
	if err := l.SetRules(flat); err != nil {
		t.Fatal(err)
	}
	n, err := l.ApplyRules("Anna")
	if err != nil || n != 2 {
		t.Fatalf("apply: %d %v", n, err)
	}
	waitRules(t, l)
	a := byRel(l, "2019/June/a.jpg")
	if a == nil || a.Place != "Lübbecke" || a.TakenSrc != "edit" {
		t.Fatalf("moved photo lost its place or date: %+v", a)
	}
	if byRel(l, "2019/June/b.jpg") == nil {
		t.Fatal("second photo not moved")
	}
	// the emptied folders are gone, the document stayed
	if _, err := os.Stat(filepath.Join(l.opt.Roots[0].Path, "2019/06 Juni Lübbecke")); !os.IsNotExist(err) {
		t.Fatalf("empty folder left: %v", err)
	}
	if byRel(l, "Dokumente/2021/03 März Rechnung/c.jpg") == nil {
		t.Fatal("document moved")
	}
	// back to the old layout
	if err := l.SetRules(layoutRules); err != nil {
		t.Fatal(err)
	}
	if n, err := l.ApplyRules("Anna"); err != nil || n != 2 {
		t.Fatalf("apply back: %d %v", n, err)
	}
	waitRules(t, l)
	if byRel(l, "2019/06 Juni Lübbecke/a.jpg") == nil || byRel(l, "2019/06 Juni Kamera/b.jpg") == nil {
		t.Fatalf("not back in place: %v", rels(l))
	}
}

func TestRulesAfterEdit(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg", "2019/06 Juni Lübbecke/b.jpg", "2019/06 Juni Lübbecke/c.jpg")
	if err := l.SetRules(layoutRules); err != nil {
		t.Fatal(err)
	}
	// one file: moved right away, the new id comes back
	aID := byRel(l, "2019/06 Juni Lübbecke/a.jpg").ID
	place := "Berlin"
	res, err := l.EditItems([]string{aID}, Edit{Place: &place, By: "Anna"})
	if err != nil || res.Moved != 1 {
		t.Fatalf("edit: %+v %v", res, err)
	}
	moved := byRel(l, "2019/06 Juni Berlin/a.jpg")
	if moved == nil || res.IDs[aID] != moved.ID {
		t.Fatalf("not filed by place: %v ids %v", rels(l), res.IDs)
	}
	// two files: in the background
	b, c := byRel(l, "2019/06 Juni Lübbecke/b.jpg"), byRel(l, "2019/06 Juni Lübbecke/c.jpg")
	when := time.Date(2020, 1, 2, 10, 0, 0, 0, time.UTC).Unix()
	res, err = l.EditItems([]string{b.ID, c.ID}, Edit{Taken: &when, By: "Anna"})
	if err != nil || res.Queued != 2 {
		t.Fatalf("edit two: %+v %v", res, err)
	}
	waitRules(t, l)
	if byRel(l, "2020/01 Januar Lübbecke/b.jpg") == nil || byRel(l, "2020/01 Januar Lübbecke/c.jpg") == nil {
		t.Fatalf("not filed by date: %v", rels(l))
	}
	// without auto nothing moves on edits
	off := layoutRules
	off.Auto = false
	l.SetRules(off)
	moved = byRel(l, "2019/06 Juni Berlin/a.jpg")
	place = "Hamburg"
	if res, _ := l.EditItems([]string{moved.ID}, Edit{Place: &place}); res.Moved+res.Queued != 0 {
		t.Fatalf("moved without auto: %+v", res)
	}
}

func TestRulesBadTemplate(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	bad := RuleSet{Locale: "de", List: []Rule{{Path: "{jahr"}}}
	if err := l.SetRules(bad); err == nil {
		t.Fatal("bad template accepted")
	}
	if p := l.PreviewRules(bad); p.Rules[0].Error == "" {
		t.Fatalf("no error in preview: %+v", p)
	}
	if _, err := l.ApplyRules(""); err == nil {
		t.Fatal("applied without rules")
	}
}

func waitRules(t *testing.T, l *Library) {
	t.Helper()
	for range 500 {
		if !l.rulesJob.running.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("rules still running")
}

func rels(l *Library) []string {
	var out []string
	for _, it := range l.Items() {
		out = append(out, it.Rel)
	}
	return out
}

func TestRulesKeepDocuments(t *testing.T) {
	l, _ := testLib(t, "Dokumente/2021/03 März Rechnung/c.jpg", "2019/06 Juni Lübbecke/a.jpg")
	// one rule for everything would take the document out of Dokumente
	all := RuleSet{Locale: "de", Auto: true, List: []Rule{{Path: "{jahr}/{datum:MM MMMM} {ort|Kamera}"}}}
	p := l.PreviewRules(all)
	if p.Blocked != 1 || p.Rules[0].Blocked != 1 || p.Moves != 0 {
		t.Fatalf("document not protected in preview: %+v", p)
	}
	l.SetRules(all)
	if n, _ := l.ApplyRules(""); n != 0 {
		t.Fatalf("planned %d moves", n)
	}
	if byRel(l, "Dokumente/2021/03 März Rechnung/c.jpg") == nil {
		t.Fatalf("document moved: %v", rels(l))
	}
}

func TestRulesReplanAfterEdit(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg")
	rs := RuleSet{Locale: "de", Auto: false, List: []Rule{{Path: "{ort}"}}}
	l.SetRules(rs)
	c := l.rules.Load()
	gen := l.editGen.Load()
	stale := l.plan(c, nil) // to "Lübbecke"
	if len(stale) != 1 || stale[0].dir != "Lübbecke" {
		t.Fatalf("plan: %+v", stale)
	}
	// the place changes before the job gets to the file
	place := "Berlin"
	if _, err := l.EditItems([]string{stale[0].ID}, Edit{Place: &place}); err != nil {
		t.Fatal(err)
	}
	l.rulesJob.running.Store(true)
	l.bg.Add(1)
	l.rulesLoop(c, stale, gen, "")
	if byRel(l, "Berlin/a.jpg") == nil {
		t.Fatalf("stale destination used: %v", rels(l))
	}
}

func TestRulesReplanMidRun(t *testing.T) {
	var files []string
	for i := range 2*rulesChunk + 10 {
		files = append(files, fmt.Sprintf("2019/06 Juni Lübbecke/%03d.jpg", i))
	}
	l, _ := testLib(t, files...)
	l.SetRules(RuleSet{Locale: "de", List: []Rule{{Path: "{ort}"}}})
	// the place of a file still to come changes while the run is going
	var edited string
	l.rulesChunkHook = func(start int) {
		if start != rulesChunk {
			return
		}
		for _, it := range l.Items() {
			if strings.HasPrefix(it.Rel, "2019/") {
				edited = path.Base(it.Rel)
				place := "Berlin"
				if _, err := l.EditItems([]string{it.ID}, Edit{Place: &place}); err != nil {
					t.Error(err)
				}
				return
			}
		}
	}
	if n, err := l.ApplyRules(""); err != nil || n != len(files) {
		t.Fatalf("apply: %d %v", n, err)
	}
	waitRules(t, l)
	n := 0
	for _, it := range l.Items() {
		if strings.HasPrefix(it.Rel, "Lübbecke/") {
			n++
		}
	}
	if edited == "" || n != len(files)-1 || byRel(l, "Berlin/"+edited) == nil {
		t.Fatalf("files skipped or misplaced after replanning: %d in Lübbecke, %v", n, rels(l)[:3])
	}
	if d, todo := l.rulesJob.done.Load(), l.rulesJob.todo.Load(); d != todo {
		t.Fatalf("progress %d/%d", d, todo)
	}
}

func TestRulesSkipExcludedFolders(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	l.opt.Exclude = []string{"#recycle"}
	p := l.PreviewRules(RuleSet{Locale: "de", List: []Rule{{Path: "#recycle/{jahr}"}}})
	if p.Moves != 0 || p.Rules[0].Excluded != 1 {
		t.Fatalf("excluded folder not refused: %+v", p)
	}
}

func TestRulesStopOnClose(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg")
	l.SetRules(RuleSet{Locale: "de", List: []Rule{{Path: "{ort}"}}})
	l.Close()
	if _, err := l.ApplyRules(""); err == nil {
		t.Fatal("filing started on a closed library")
	}
}

func TestRulesPreviewRenames(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg", "2019/07 Juli Lübbecke/a.jpg")
	p := l.PreviewRules(RuleSet{Locale: "de", List: []Rule{{Path: "{ort}"}}})
	got := map[string]bool{}
	for _, ex := range p.Rules[0].Examples {
		got[ex.To] = true
	}
	if p.Rules[0].Renamed != 1 || !got["Lübbecke/a.jpg"] || !got["Lübbecke/a (2).jpg"] {
		t.Fatalf("preview of a taken name: %+v", p.Rules[0])
	}
}

func TestRulesIgnoreUnrelatedEdits(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg")
	l.SetRules(RuleSet{Locale: "de", Auto: true, List: []Rule{{Path: "{ort}"}}})
	desc := "Ein Text"
	res, err := l.EditItems([]string{byRel(l, "2019/06 Juni Lübbecke/a.jpg").ID}, Edit{Desc: &desc})
	if err != nil || res.Moved+res.Queued != 0 || byRel(l, "2019/06 Juni Lübbecke/a.jpg") == nil {
		t.Fatalf("a text edit filed the photo: %+v %v %v", res, err, rels(l))
	}
}

func TestRulesChangeStopsRun(t *testing.T) {
	var files []string
	for i := range 2*rulesChunk + 10 {
		files = append(files, fmt.Sprintf("2019/06 Juni Lübbecke/%03d.jpg", i))
	}
	l, _ := testLib(t, files...)
	l.SetRules(RuleSet{Locale: "de", List: []Rule{{Path: "{ort}"}}})
	l.rulesChunkHook = func(start int) {
		if start == rulesChunk {
			l.SetRules(RuleSet{Locale: "de", List: []Rule{{Path: "Neu/{ort}"}}})
		}
	}
	if _, err := l.ApplyRules(""); err != nil {
		t.Fatal(err)
	}
	waitRules(t, l)
	n := 0
	for _, it := range l.Items() {
		if strings.HasPrefix(it.Rel, "Lübbecke/") {
			n++
		}
		if strings.HasPrefix(it.Rel, "Neu/") {
			t.Fatalf("new rules applied without a new run: %s", it.Rel)
		}
	}
	if n != rulesChunk {
		t.Fatalf("%d files moved before the change, want %d", n, rulesChunk)
	}
	if d, todo := l.rulesJob.done.Load(), l.rulesJob.todo.Load(); d != todo {
		t.Fatalf("progress %d/%d", d, todo)
	}
}
