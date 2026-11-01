package gallery

import (
	"context"
	"testing"
)

func TestMarksPerUser(t *testing.T) {
	l, _ := testLib(t, "2019/a.jpg", "2019/b.jpg", "2019/c.jpg")
	a, b := byRel(l, "2019/a.jpg"), byRel(l, "2019/b.jpg")
	if _, err := l.SetMarks("", []string{a.ID}, true); err == nil {
		t.Error("marks without an account")
	}
	if n, err := l.SetMarks("anna", []string{a.ID, b.ID}, true); err != nil || n != 2 {
		t.Fatalf("mark: %d %v", n, err)
	}
	l.SetMarks("ben", []string{a.ID}, true)
	if !l.IsMarked("anna", a) || l.IsMarked("ben", b) || l.IsMarked("", a) {
		t.Error("marks must be per account")
	}
	all := l.AllMarks()
	if len(all) != 2 || all[0].ID != a.ID || len(all[0].Marks) != 2 {
		t.Fatalf("all marks (most marked first): %+v", all)
	}
	if ids := l.MarkedIDs("anna"); len(ids) != 2 {
		t.Errorf("anna's marks: %v", ids)
	}
	// marks follow a move
	if _, err := l.Move([]string{a.ID}, "B", "2020"); err != nil {
		t.Fatal(err)
	}
	moved := byRel(l, "2020/a.jpg")
	if moved == nil || !l.IsMarked("anna", moved) || !l.IsMarked("ben", moved) {
		t.Fatal("marks lost on move")
	}
	if n, _ := l.ClearMarks([]string{moved.ID}); n != 2 {
		t.Errorf("clear removed %d marks, want 2", n)
	}
	if l.IsMarked("anna", moved) {
		t.Error("still marked after clear")
	}
	l.SetMarks("anna", []string{b.ID}, false)
	if len(l.AllMarks()) != 0 {
		t.Errorf("unmark: %+v", l.AllMarks())
	}
	// they survive a restart
	l.SetMarks("carl", []string{b.ID}, true)
	l2 := &Library{opt: l.opt}
	l2.loadMarks()
	if len(l2.marks.users["carl"]) != 1 {
		t.Error("marks not saved")
	}
}

func TestNotes(t *testing.T) {
	l, _ := testLib(t, "2019/a.jpg", "2019/b.jpg")
	a := byRel(l, "2019/a.jpg")
	if _, err := l.AddNote("", a.ID, "x"); err == nil {
		t.Error("notes without an account")
	}
	if _, err := l.AddNote("anna", a.ID, "   "); err != ErrNoteEmpty {
		t.Errorf("empty note: %v", err)
	}
	n, err := l.AddNote("anna", a.ID, "Opa Heinz beim Angeln am Mittellandkanal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.UpdateNote("ben", a.ID, n.ID, "falsch"); err != ErrNoteNotYours {
		t.Errorf("someone else's note changed: %v", err)
	}
	if _, err := l.UpdateNote("anna", a.ID, n.ID, "Opa Heinz beim Angeln am Mittellandkanal, 1998"); err != nil {
		t.Fatal(err)
	}
	if err := l.DeleteNote("ben", false, a.ID, n.ID); err != ErrNoteNotYours {
		t.Errorf("someone else's note deleted: %v", err)
	}
	ids := func(q string) []string {
		var out []string
		for _, h := range l.Search(q, "", "de") {
			out = append(out, h.ID)
		}
		return out
	}
	for _, q := range []string{"angeln", "notiz:mittellandkanal", "note:heinz", "typ:notiz", "type:note"} {
		if got := ids(q); len(got) != 1 || got[0] != a.ID {
			t.Errorf("search %q: %v", q, got)
		}
	}
	hits := l.Search("mittellandkanal", "", "de")
	if len(hits) != 1 || hits[0].Snip == "" {
		t.Errorf("no snippet from the note: %+v", hits)
	}
	// notes follow a move and survive a restart
	if _, err := l.Move([]string{a.ID}, "B", "2020"); err != nil {
		t.Fatal(err)
	}
	moved := byRel(l, "2020/a.jpg")
	if ns := l.NotesOf(moved); len(ns) != 1 || ns[0].User != "anna" || ns[0].Edited == 0 {
		t.Fatalf("notes after move: %+v", ns)
	}
	l2 := &Library{opt: l.opt}
	l2.loadNotes()
	if len(l2.notes.items) != 1 {
		t.Error("notes not saved")
	}
	if err := l.DeleteNote("ben", true, moved.ID, n.ID); err != nil {
		t.Errorf("admin delete: %v", err)
	}
	if got := ids("angeln"); len(got) != 0 {
		t.Errorf("deleted note still found: %v", got)
	}
	_ = context.Background()
}
