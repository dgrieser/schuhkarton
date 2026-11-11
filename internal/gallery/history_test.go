package gallery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An edit, a move and a deletion are kept with what undoing needs, and undone.
func TestHistoryUndo(t *testing.T) {
	l, root := testLib(t, "2019/06 Juni Lübbecke/a.jpg", "x/b.jpg", "Neu/c.jpg")
	a := byRel(l, "2019/06 Juni Lübbecke/a.jpg")

	// an edit: the place before comes back
	place := "Berlin"
	e := Edit{Place: &place, By: "admin"}
	before := l.EditBefores([]string{a.ID}, e)
	if _, err := l.EditItems([]string{a.ID}, e); err != nil {
		t.Fatal(err)
	}
	l.Record(Change{By: "admin", Kind: ChEdit, Items: []string{a.ID}}, before)

	// a move: the item is followed to its new id, and moved back
	froms := l.MoveFroms([]string{a.ID})
	res, err := l.Move([]string{a.ID}, "B", "Neu")
	if err != nil {
		t.Fatal(err)
	}
	back, ids := MovedTo(froms, res.IDs)
	if len(ids) != 1 {
		t.Fatalf("moved: %v %v", res.IDs, froms)
	}
	l.Record(Change{By: "admin", Kind: ChMove, Items: ids}, back)

	list, _, _ := l.History("", 10, "", "")
	if len(list) != 2 || list[0].Kind != ChMove || list[1].Kind != ChEdit || !list[0].CanUndo || !list[1].CanUndo {
		t.Fatalf("history: %+v", list)
	}
	if list[1].Items[0] != ids[0] {
		t.Errorf("the edit did not follow the move: %v, now %s", list[1].Items, ids[0])
	}
	if list[0].Undo != nil {
		t.Error("what undoing needs is sent to the list")
	}

	// undo the edit (on the moved item), then the move
	if _, err := l.UndoChange(list[1].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	moved := l.Get(ids[0])
	if moved == nil || moved.Place != "Lübbecke" {
		t.Fatalf("place not back: %+v", moved)
	}
	if _, err := l.UndoChange(list[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if byRel(l, "2019/06 Juni Lübbecke/a.jpg") == nil || !exists(filepath.Join(root, "2019/06 Juni Lübbecke/a.jpg")) {
		t.Fatalf("not moved back: %v", relsOf(l))
	}
	if _, err := l.UndoChange(list[0].ID, "admin"); err == nil {
		t.Error("a change was undone twice")
	}

	// a deletion: undone from the trash, no copy of the file
	b := byRel(l, "x/b.jpg")
	del, err := l.Trash([]string{b.ID}, "admin")
	if err != nil || len(del.Entries) != 1 {
		t.Fatalf("trash: %v %+v", err, del)
	}
	l.Record(Change{By: "admin", Kind: ChDelete, Names: []string{"b.jpg"}}, del.Entries)
	list, _, _ = l.History("", 1, ChDelete, "admin")
	if len(list) != 1 || !list[0].CanUndo || len(list[0].Trash) != 1 {
		t.Fatalf("delete in the list: %+v", list)
	}
	if _, err := l.UndoChange(list[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if byRel(l, "x/b.jpg") == nil {
		t.Fatal("not restored")
	}

	// a rotation: turned back
	b = byRel(l, "x/b.jpg")
	w0, h0 := b.W, b.H
	if _, err := l.Rotate([]string{b.ID}, 90, "admin"); err != nil {
		t.Fatal(err)
	}
	l.Record(Change{By: "admin", Kind: ChRotate, Items: []string{b.ID}, Info: map[string]any{"deg": 90}}, map[string]int{"deg": 90})
	if b = byRel(l, "x/b.jpg"); b.W != h0 || b.H != w0 {
		t.Fatalf("not rotated: %dx%d", b.W, b.H)
	}
	list, _, _ = l.History("", 1, ChRotate, "")
	if _, err := l.UndoChange(list[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if b = byRel(l, "x/b.jpg"); b.W != w0 || b.H != h0 {
		t.Errorf("not turned back: %dx%d", b.W, b.H)
	}

	// marks: undone for the account that set them
	c := byRel(l, "Neu/c.jpg")
	if _, err := l.SetMarks("anna", []string{c.ID}, true); err != nil {
		t.Fatal(err)
	}
	l.Record(Change{By: "anna", Kind: ChMark, Items: []string{c.ID}}, ToggleUndo{User: "anna", On: true})
	list, _, _ = l.History("", 1, ChMark, "")
	if _, err := l.UndoChange(list[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if l.IsMarked("anna", c) {
		t.Error("mark not removed")
	}

	// the filter by account: "-" is Schuhkarton itself
	if got, _, _ := l.History("", 10, "", "-"); len(got) != 0 {
		t.Errorf("changes by Schuhkarton: %+v", got)
	}
}

// The file keeps changes, undos and new ids; what is older than the days kept
// is forgotten.
func TestHistoryFile(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 30)
	h.add(&Change{Kind: ChRotate, By: "admin", Items: []string{"i1"}, Info: map[string]any{"deg": 90}}, nil)
	h.mu.Lock()
	keep := h.list[0]
	h.write(histEvent{C: &Change{ID: "old", At: nowUnix() - 40*86400, Kind: ChEdit, Items: []string{"i2"}}}) // older than the days kept
	h.write(histEvent{U: keep.ID, At: nowUnix(), By: "bob"})
	h.mu.Unlock()
	h.rename(map[string]string{"i1": "i9"})

	h2 := newHistory(dir, 30)
	if len(h2.list) != 1 {
		t.Fatalf("kept: %d", len(h2.list))
	}
	c := h2.list[0]
	if c.ID != keep.ID || c.UndoneBy != "bob" || c.Items[0] != "i9" {
		t.Fatalf("reloaded: %+v", c)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "history.jsonl"))
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Errorf("not written anew at start: %d lines", n)
	}
}

func TestRenameInJSON(t *testing.T) {
	in := json.RawMessage(`{"a:1":"Anna","list":[{"id":"a","root":"B"}],"b":["a","x"]}`)
	var out map[string]any
	json.Unmarshal(renameInJSON(in, map[string]string{"a": "z"}), &out)
	if _, ok := out["z:1"]; !ok {
		t.Errorf("face key not renamed: %v", out)
	}
	if out["list"].([]any)[0].(map[string]any)["id"] != "z" || out["b"].([]any)[0] != "z" {
		t.Errorf("ids not renamed: %v", out)
	}
	// a face of a file in the trash, and back
	var faces map[string]string
	json.Unmarshal(renameInJSON(json.RawMessage(`{"trash:e1:2":"Anna"}`), map[string]string{"trash:e1": "x"}), &faces)
	if faces["x:2"] != "Anna" {
		t.Errorf("trash face key not renamed: %v", faces)
	}
}

// Pages follow the order of the changes, also within one second.
func TestHistoryPages(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	for i := 0; i < 5; i++ {
		l.Record(Change{By: "admin", Kind: ChFav}, nil)
	}
	first, _, more := l.History("", 2, "", "")
	if len(first) != 2 || !more {
		t.Fatalf("first page: %d %v", len(first), more)
	}
	seen := map[string]bool{first[0].ID: true, first[1].ID: true}
	next, _, more := l.History(first[1].ID, 2, "", "")
	last, _, end := l.History(next[1].ID, 2, "", "")
	for _, c := range append(next, last...) {
		seen[c.ID] = true
	}
	if len(seen) != 5 || !more || end {
		t.Errorf("pages: %d changes seen, more %v, end %v", len(seen), more, end)
	}
	if got, _, _ := l.History("forgotten", 2, "", ""); len(got) != 0 {
		t.Errorf("after a forgotten change: %d", len(got))
	}
}

// After a restart the undo data follows a move too.
func TestHistoryRenameAfterRestart(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 30)
	b, _ := json.Marshal([]EditBefore{{ID: "old"}})
	h.add(&Change{Kind: ChEdit, Items: []string{"old"}, Undo: b}, nil)
	h.rename(map[string]string{"old": "new"})
	h2 := newHistory(dir, 30)
	var got []EditBefore
	json.Unmarshal(h2.list[0].Undo, &got)
	if h2.list[0].Items[0] != "new" || len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("after restart: %v %s", h2.list[0].Items, h2.list[0].Undo)
	}
}

// A deleted note comes back with its id, so its earlier edit can be undone too.
func TestHistoryNoteKeepsID(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	it := byRel(l, "a.jpg")
	n, err := l.AddNote("anna", it.ID, "eins")
	if err != nil {
		t.Fatal(err)
	}
	l.Record(Change{By: "anna", Kind: ChNote, Items: []string{it.ID}}, NoteUndo{Op: "add", Item: it.ID, Note: n.ID, User: "anna"})
	if _, err := l.UpdateNote("anna", it.ID, n.ID, "zwei"); err != nil {
		t.Fatal(err)
	}
	l.Record(Change{By: "anna", Kind: ChNote, Items: []string{it.ID}}, NoteUndo{Op: "edit", Item: it.ID, Note: n.ID, User: "anna", Text: "eins"})
	was, _ := l.NoteOf(it.ID, n.ID)
	if err := l.DeleteNote("anna", false, it.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	l.Record(Change{By: "anna", Kind: ChNote, Items: []string{it.ID}}, NoteUndo{Op: "delete", Item: it.ID, User: "anna", Text: was.Text, Was: &was})
	list, _, _ := l.History("", 3, "", "")
	for _, c := range list { // newest first: delete, edit, add
		if _, err := l.UndoChange(c.ID, "admin"); err != nil {
			t.Fatalf("undo %v: %v", c.Info, err)
		}
		if c.ID == list[0].ID {
			if got, ok := l.NoteOf(it.ID, n.ID); !ok || got.Text != "zwei" {
				t.Fatalf("deleted note not back with its id: %+v %v", got, ok)
			}
		}
	}
	if len(l.NotesOf(it)) != 0 {
		t.Errorf("notes left after undoing all: %+v", l.NotesOf(it))
	}
}

// A move into a folder where the name is taken goes back under the old name;
// a file restored under another name keeps its changes.
func TestHistoryNames(t *testing.T) {
	l, root := testLib(t, "a/x.jpg", "b/x.jpg")
	x := byRel(l, "a/x.jpg")
	froms := l.MoveFroms([]string{x.ID})
	res, err := l.Move([]string{x.ID}, "B", "b")
	if err != nil {
		t.Fatal(err)
	}
	back, ids := MovedTo(froms, res.IDs)
	if byRel(l, "b/x (2).jpg") == nil {
		t.Fatalf("not renamed: %v", relsOf(l))
	}
	l.Record(Change{Kind: ChMove, Items: ids}, back)
	list, _, _ := l.History("", 1, ChMove, "")
	if _, err := l.UndoChange(list[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if byRel(l, "a/x.jpg") == nil || !exists(filepath.Join(root, "a/x.jpg")) {
		t.Fatalf("not back under its name: %v", relsOf(l))
	}

	// an edit, then the file goes to the trash and its place is taken
	x = byRel(l, "a/x.jpg")
	place := "Rom"
	e := Edit{Place: &place}
	before := l.EditBefores([]string{x.ID}, e)
	l.EditItems([]string{x.ID}, e)
	l.Record(Change{Kind: ChEdit, Items: []string{x.ID}}, before)
	del, err := l.Trash([]string{x.ID}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	writeTestJPEG(t, filepath.Join(root, "a/x.jpg"), 20, 20)
	r, err := l.Restore(del.Entries)
	if err != nil || len(r.IDs) != 1 {
		t.Fatalf("restore: %v %+v", err, r)
	}
	newID := r.IDs[del.Entries[0]]
	list, _, _ = l.History("", 1, ChEdit, "")
	if list[0].Items[0] != newID || !list[0].CanUndo {
		t.Fatalf("the edit did not follow the restore: %v (now %s) %v", list[0].Items, newID, list[0].Why)
	}
}

// An edit keeps itself in the history; a description put back by an undo is
// not replaced by a job that ran meanwhile, only after it is asked for again.
func TestHistoryEditRecordsAndPins(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	it := byRel(l, "a.jpg")
	place := "Rom"
	if _, err := l.EditItems([]string{it.ID}, Edit{Place: &place, By: "anna", Record: map[string]any{"fields": []string{"place"}}}); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := l.History("", 5, ChEdit, "anna"); len(list) != 1 || !list[0].CanUndo || len(list[0].Names) != 1 {
		t.Fatalf("edit not kept: %+v", list)
	}

	l.setAI(it, &AIInfo{De: "alt", Model: "m", At: 1})
	before := l.RecognizedBefore(ChAI, []string{it.ID})
	l.Redescribe([]string{it.ID})
	l.Record(Change{Kind: ChAI, Items: []string{it.ID}}, before)
	list, _, _ := l.History("", 1, ChAI, "")
	if _, err := l.UndoChange(list[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	// the file moves, then the job that was running finishes
	res, err := l.Move([]string{it.ID}, "B", "neu")
	if err != nil || len(res.IDs) != 1 {
		t.Fatalf("move: %v %+v", err, res)
	}
	if it = byRel(l, "neu/a.jpg"); it == nil {
		t.Fatalf("not moved: %v", relsOf(l))
	}
	if l.setAI(it, &AIInfo{De: "neu", Model: "m", At: 2}) {
		t.Error("a late result was taken")
	}
	if a := l.AIOf(it); a == nil || a.De != "alt" {
		t.Fatalf("undo replaced: %+v", a)
	}
	l.Redescribe([]string{it.ID})
	l.setAI(it, &AIInfo{De: "neu", Model: "m", At: 3})
	if a := l.AIOf(it); a == nil || a.De != "neu" {
		t.Fatalf("a new request is not taken: %+v", a)
	}
}

// What becomes too old while running is forgotten, in the list and the file.
func TestHistoryExpires(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 30)
	h.add(&Change{Kind: ChFav}, nil)
	h.mu.Lock()
	h.list[0].At = nowUnix() - 31*86400
	h.mu.Unlock()
	h.add(&Change{Kind: ChMark}, nil)
	if len(h.list) != 1 || h.list[0].Kind != ChMark {
		t.Fatalf("kept: %+v", h.list)
	}
	if n := len(newHistory(dir, 30).list); n != 1 {
		t.Errorf("file keeps %d", n)
	}
}

// A move keeps itself in the history with where the file was.
func TestHistoryMoveAs(t *testing.T) {
	l, _ := testLib(t, "a/x.jpg")
	x := byRel(l, "a/x.jpg")
	if _, err := l.MoveAs([]string{x.ID}, "B", "b", &Change{By: "admin", Info: map[string]any{"to": "B/b"}}); err != nil {
		t.Fatal(err)
	}
	list, _, _ := l.History("", 5, ChMove, "admin")
	if len(list) != 1 || !list[0].CanUndo || list[0].Info["n"] != 1 {
		t.Fatalf("move not kept: %+v", list)
	}
	if _, err := l.UndoChange(list[0].ID, "admin"); err != nil || byRel(l, "a/x.jpg") == nil {
		t.Fatalf("not moved back: %v %v", err, relsOf(l))
	}
}

// After a file is deleted for good, its old changes do not reach a new file
// at the same path.
func TestHistoryPurgeForgetsItems(t *testing.T) {
	l, root := testLib(t, "a.jpg")
	a := byRel(l, "a.jpg")
	l.Rotate([]string{a.ID}, 90, "admin")
	l.Record(Change{Kind: ChRotate, Items: []string{a.ID}}, map[string]int{"deg": 90})
	del, err := l.Trash([]string{a.ID}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.EmptyTrash(del.Entries); err != nil {
		t.Fatal(err)
	}
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 20, 20)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if byRel(l, "a.jpg") == nil {
		t.Fatal("new file not found")
	}
	list, _, _ := l.History("", 1, ChRotate, "")
	if list[0].CanUndo {
		t.Errorf("an old rotation would turn the new file: %+v", list[0].Items)
	}
}

// A file in the trash keeps its changes apart from a new file at its path:
// restored under another name, each file keeps its own.
func TestHistoryTrashKeepsChangesApart(t *testing.T) {
	l, root := testLib(t, "a.jpg")
	old := byRel(l, "a.jpg")
	oldID := old.ID
	l.Record(Change{Kind: ChFav, Items: []string{oldID}, Info: map[string]any{"who": "old"}}, ToggleUndo{User: "anna", On: true})
	del, err := l.Trash([]string{oldID}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 20, 20)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	repl := byRel(l, "a.jpg")
	if repl == nil || repl.ID != oldID {
		t.Fatalf("the new file should have the same id: %+v", repl)
	}
	l.Record(Change{Kind: ChFav, Items: []string{repl.ID}, Info: map[string]any{"who": "new"}}, ToggleUndo{User: "anna", On: true})
	res, err := l.Restore(del.Entries)
	if err != nil {
		t.Fatal(err)
	}
	back := res.IDs[del.Entries[0]]
	if back == oldID {
		t.Fatal("restored over the new file")
	}
	list, _, _ := l.History("", 5, ChFav, "")
	for _, c := range list {
		want := back
		if c.Info["who"] == "new" {
			want = oldID
		}
		if len(c.Items) != 1 || c.Items[0] != want {
			t.Errorf("%v: items %v, want %s", c.Info["who"], c.Items, want)
		}
	}
}

// A change recorded just after a move that slipped in between follows it.
func TestHistoryRecentMoves(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 30)
	h.rename(map[string]string{"old": "new"})
	b, _ := json.Marshal(map[string]string{"old:1": "Anna"})
	h.add(&Change{Kind: ChFaces, Items: []string{"old"}, Undo: b}, nil)
	if h.list[0].Items[0] != "new" || !strings.Contains(string(h.list[0].Undo), "new:1") {
		t.Fatalf("not followed: %v %s", h.list[0].Items, h.list[0].Undo)
	}
}
