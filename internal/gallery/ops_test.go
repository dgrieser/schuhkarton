package gallery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// reopen opens the library of l again over the same folders and cache (a
// restart), without its background jobs.
func reopen(t *testing.T, l *Library) *Library {
	t.Helper()
	opt := l.opt
	l.Close()
	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l2.Close)
	if err := l2.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return l2
}

// Work in the queue runs right away without a background loop (tools,
// tests) and is kept with its result.
func TestOpsInline(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	a := byRel(l, "a.jpg")
	op, err := l.Enqueue(OpDelete, "anna", false, DeleteArgs{IDs: []string{a.ID}, Names: []string{"a.jpg"}})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "done" || op.Res == nil || len(op.Res.Entries) != 1 || l.Get(a.ID) != nil {
		t.Fatalf("not done: %+v", op)
	}
	if got := l.WaitOp(context.Background(), op.ID, time.Second); got.State != "done" {
		t.Errorf("wait: %+v", got)
	}
	if list, _, _ := l.History("", 1, ChDelete, "anna"); len(list) != 1 {
		t.Error("deletion not kept in the history")
	}
}

// What was queued or running when Schuhkarton stopped goes on at the next
// start; a rotation does not turn a file twice.
func TestOpsGoOnAfterRestart(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	a, b := byRel(l, "a.jpg"), byRel(l, "b.jpg")
	// a rotation that turned a before the stop, and a deletion not started
	rot, _ := json.Marshal(RotateArgs{IDs: []string{a.ID, b.ID}, Deg: 90})
	del, _ := json.Marshal(DeleteArgs{IDs: []string{b.ID}})
	if _, err := l.Rotate([]string{a.ID}, 90, "anna"); err != nil {
		t.Fatal(err)
	}
	q := l.ops
	q.mu.Lock()
	q.write(opEvent{Op: &Op{ID: "r1", Kind: OpRotate, By: "anna", At: nowUnix(), Args: rot, State: "running"}})
	q.write(opEvent{ID: "r1", Step: map[string]string{a.ID: "1"}})
	q.write(opEvent{Op: &Op{ID: "d1", Kind: OpDelete, By: "anna", At: nowUnix(), Args: del, State: "queued"}})
	q.mu.Unlock()

	l2 := reopen(t, l)
	if got := l2.OpStatus("r1"); got == nil || got.State != "queued" {
		t.Fatalf("rotation not queued again: %+v", got)
	}
	l2.runQueued()
	a2 := byRel(l2, "a.jpg")
	if a2.Rot != 90 {
		t.Errorf("a turned twice or not at all: %d°", a2.Rot)
	}
	if r := l2.OpStatus("r1"); r.State != "done" || r.Res.Changed != 1 {
		t.Errorf("rotation: %+v %+v", r, r.Res)
	}
	if d := l2.OpStatus("d1"); d.State != "done" || len(d.Res.Entries) != 1 || byRel(l2, "b.jpg") != nil {
		t.Errorf("deletion: %+v", d)
	}
	if n := l2.OpsWaiting(); n != 0 {
		t.Errorf("still waiting: %d", n)
	}
}

// A deletion that went partly through before a kill reports all its trash
// entries, also those of the run before.
func TestOpsDeleteAcrossRestart(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	a, b := byRel(l, "a.jpg"), byRel(l, "b.jpg")
	at := nowUnix()
	if _, err := l.Trash([]string{a.ID}, "anna"); err != nil { // the part done before the kill
		t.Fatal(err)
	}
	args, _ := json.Marshal(DeleteArgs{IDs: []string{a.ID, b.ID}})
	op := &Op{ID: "d1", Kind: OpDelete, By: "anna", At: at, Args: args, State: "running"}
	res, err := l.runOp(op)
	if err != nil || len(res.Entries) != 2 {
		t.Fatalf("entries: %v %+v", err, res)
	}
}

// An edit that goes on after a restart keeps the values from before it first
// ran, so undo still goes back to them.
func TestOpsEditKeepsBefore(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	a := byRel(l, "a.jpg")
	place := "Rom"
	args, _ := json.Marshal(EditArgs{IDs: []string{a.ID}, Edit: Edit{Place: &place}, Info: map[string]any{"fields": []string{"place"}}})
	op := &Op{ID: "e1", Kind: OpEdit, By: "anna", At: nowUnix(), Args: args, State: "running"}
	l.ops.mu.Lock()
	l.ops.list = append(l.ops.list, op)
	l.ops.byID[op.ID] = op
	l.ops.mu.Unlock()
	if _, err := l.runOp(op); err != nil {
		t.Fatal(err)
	}
	var before []EditBefore
	if json.Unmarshal(op.Prog, &before) != nil || len(before) != 1 || before[0].Place == nil || *before[0].Place != "" {
		t.Fatalf("values before not kept: %s", op.Prog)
	}
	// the same edit again (a restart): the history keeps the old values
	if _, err := l.runOp(op); err != nil {
		t.Fatal(err)
	}
	list, _, _ := l.History("", 1, ChEdit, "")
	var got []EditBefore
	json.Unmarshal(l.hist.byID[list[0].ID].Undo, &got)
	if len(got) != 1 || *got[0].Place != "" {
		t.Errorf("undo would keep the new place: %+v", got)
	}
}

// Filing by rules and urgent recognition are kept for a restart.
func TestIntentsKept(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	l.saveRulesIntent(false, []string{"x"}, "anna")
	l.saveRulesIntent(false, []string{"y"}, "anna")
	if in := l.loadRulesIntent(); in.All || len(in.IDs) != 2 || in.By != "anna" {
		t.Errorf("rules: %+v", in)
	}
	l.saveRulesIntent(true, nil, "bob")
	if in := l.loadRulesIntent(); !in.All || len(in.IDs) != 0 {
		t.Errorf("rules all: %+v", in)
	}
	l.clearRulesIntent()
	if _, err := os.Stat(filepath.Join(l.opt.CacheDir, "rules-pending.json")); !os.IsNotExist(err) {
		t.Error("intent not cleared")
	}

	l.aiJob.on = true // a job that takes work (no service in tests)
	l.aiJob.urgentWake = make(chan struct{}, 1)
	l.aiJob.wake = make(chan struct{}, 1)
	a := byRel(l, "a.jpg")
	l.markUrgent(ChAI, []string{a.ID})
	l2 := reopen(t, l)
	if !l2.aiJob.hasUrgent() {
		t.Error("urgent work lost in a restart")
	}
}

// A rotation killed while it turned a photo does not turn it again after the
// restart; one killed just before turning it does.
func TestOpsRotateKilledMidway(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	a, b := byRel(l, "a.jpg"), byRel(l, "b.jpg")
	sigA, sigB := l.rotateSigOf(a), l.rotateSigOf(b)
	if _, err := l.Rotate([]string{a.ID}, 90, "anna"); err != nil { // a turned, the kill came before the progress
		t.Fatal(err)
	}
	args, _ := json.Marshal(RotateArgs{IDs: []string{a.ID}, Deg: 90})
	prog, _ := json.Marshal(rotateProg{Cur: []rotateSig{sigA}})
	op := &Op{ID: "r1", Kind: OpRotate, By: "anna", At: nowUnix(), Args: args, Prog: prog}
	if res, err := l.runOp(op); err != nil || res.Changed != 1 {
		t.Fatalf("a: %v %+v", err, res)
	}
	if a2 := byRel(l, "a.jpg"); a2.Rot != 90 {
		t.Errorf("a turned twice: %d°", a2.Rot)
	}
	// b: the kill came before it turned
	args, _ = json.Marshal(RotateArgs{IDs: []string{b.ID}, Deg: 90})
	prog, _ = json.Marshal(rotateProg{Cur: []rotateSig{sigB}})
	op = &Op{ID: "r2", Kind: OpRotate, By: "anna", At: nowUnix(), Args: args, Prog: prog}
	if res, err := l.runOp(op); err != nil || res.Changed != 1 {
		t.Fatalf("b: %v %+v", err, res)
	}
	if b2 := byRel(l, "b.jpg"); b2.Rot != 90 {
		t.Errorf("b not turned: %d°", b2.Rot)
	}
}

// A time shift done again after a restart moves the photo once.
func TestOpsShiftOnce(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	a := byRel(l, "a.jpg")
	t0 := a.Taken
	args, _ := json.Marshal(EditArgs{IDs: []string{a.ID}, Edit: Edit{Shift: 3600}, Info: map[string]any{"fields": []string{"taken"}}})
	op := &Op{ID: "e1", Kind: OpEdit, By: "anna", At: nowUnix(), Args: args}
	for range 2 {
		if _, err := l.runOp(op); err != nil {
			t.Fatal(err)
		}
	}
	if got := byRel(l, "a.jpg").Taken; got != t0+3600 {
		t.Errorf("taken %d, want %d", got, t0+3600)
	}
}

// Work asked for with the ids from before a move reaches the moved items: when
// queued behind the move, and when asked for after it.
func TestOpsFollowMove(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	aID, bID := byRel(l, "a.jpg").ID, byRel(l, "b.jpg").ID // a move changes the ids in the items
	q := l.ops
	add := func(kind string, args any) *Op {
		raw, _ := json.Marshal(args)
		op := &Op{ID: newChangeID(), Kind: kind, By: "anna", At: nowUnix(), Args: raw, State: "queued"}
		q.mu.Lock()
		q.list = append(q.list, op)
		q.byID[op.ID] = op
		q.done[op.ID] = make(chan struct{})
		q.write(opEvent{Op: op})
		q.mu.Unlock()
		return op
	}
	mv := add(OpMove, MoveArgs{IDs: []string{aID, bID}, Root: "B", Dir: "dst"})
	rot := add(OpRotate, RotateArgs{IDs: []string{aID}, Deg: 90})
	l.runQueued()
	if m := l.OpStatus(mv.ID); m.State != "done" || len(m.Res.IDs) != 2 {
		t.Fatalf("move: %+v", m)
	}
	if r := l.OpStatus(rot.ID); r.State != "done" || r.Res.Changed != 1 {
		t.Errorf("rotation queued behind the move missed: %+v %s", r.Res, r.Args)
	}
	// asked for later with the old id
	op, err := l.Enqueue(OpRotate, "anna", false, RotateArgs{IDs: []string{bID}, Deg: 90})
	if err != nil || op.State != "done" || op.Res.Changed != 1 {
		t.Errorf("rotation after the move missed: %v %+v", err, op)
	}
	// the new ids are kept for a restart
	l2 := reopen(t, l)
	if to := l2.ops.follow(aID, l2.has); to == aID || l2.Get(to) == nil {
		t.Errorf("moves forgotten: %s", to)
	}
}

// Work that cannot be kept for a restart is refused, not run unkept.
func TestOpsNotKept(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	l.ops.path = t.TempDir() // a folder: not writable as a file
	if _, err := l.Enqueue(OpDelete, "anna", false, DeleteArgs{IDs: []string{byRel(l, "a.jpg").ID}}); err == nil {
		t.Error("no error")
	}
	if byRel(l, "a.jpg") == nil {
		t.Error("deleted although not kept")
	}
}

// Work left from before a start waits until the folders are read again (the
// index from before may be behind); then it runs on its own.
func TestOpsReplayAfterScan(t *testing.T) {
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 40, 30)
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := byRel(l, "a.jpg").ID
	args, _ := json.Marshal(DeleteArgs{IDs: []string{id}})
	l.ops.mu.Lock()
	l.ops.write(opEvent{Op: &Op{ID: "d1", Kind: OpDelete, By: "anna", At: nowUnix(), Args: args, State: "queued"}})
	l.ops.mu.Unlock()
	l.Close()

	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l2.opsOn.Store(true) // the queue as Start runs it (without the other background work)
	l2.bg.Add(1)
	go l2.opLoop(ctx)
	time.Sleep(200 * time.Millisecond)
	if op := l2.OpStatus("d1"); op.State != "queued" {
		t.Fatalf("ran before the scan: %+v", op)
	}
	if err := l2.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if op := l2.WaitOp(ctx, "d1", 5*time.Second); op.State != "done" || len(op.Res.Entries) != 1 {
		t.Errorf("not done after the scan: %+v", op)
	}
}

// An id that is in use again (a new file at the old place) is not sent after
// the moved item; an item moved back is found under its old id.
func TestOpsFollowOnlyGoneIDs(t *testing.T) {
	l, root := testLib(t, "a.jpg")
	aID := byRel(l, "a.jpg").ID
	if _, err := l.Enqueue(OpMove, "anna", false, MoveArgs{IDs: []string{aID}, Root: "B", Dir: "dst"}); err != nil {
		t.Fatal(err)
	}
	movedID := byRel(l, "dst/a.jpg").ID
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 50, 30) // another photo where it was
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if byRel(l, "a.jpg").ID != aID {
		t.Fatal("the new file has another id")
	}
	op, err := l.Enqueue(OpDelete, "anna", false, DeleteArgs{IDs: []string{aID}})
	if err != nil || op.State != "done" {
		t.Fatal(err, op)
	}
	if byRel(l, "a.jpg") != nil || byRel(l, "dst/a.jpg") == nil {
		t.Error("the moved photo was deleted instead of the new one")
	}
	// moved back: the id it had in dst leads to it
	if _, err := l.Enqueue(OpMove, "anna", false, MoveArgs{IDs: []string{movedID}, Root: "B", Dir: ""}); err != nil {
		t.Fatal(err)
	}
	back := byRel(l, "a.jpg")
	if back == nil {
		t.Fatal("not moved back")
	}
	l.ops.mu.Lock()
	to := l.ops.follow(movedID, l.has)
	l.ops.mu.Unlock()
	if to != back.ID {
		t.Errorf("follow %s = %s, want %s", movedID, to, back.ID)
	}
}

// A move killed after files moved but before their new ids were kept finds
// them again, and work behind it reaches them.
func TestOpsMoveKilledMidway(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	aID, bID := byRel(l, "a.jpg").ID, byRel(l, "b.jpg").ID
	args, _ := json.Marshal(MoveArgs{IDs: []string{aID, bID}, Root: "B", Dir: "dst"})
	mv := &Op{ID: "m1", Kind: OpMove, By: "anna", At: nowUnix(), Args: args, State: "queued"}
	q := l.ops
	q.mu.Lock()
	q.list = append(q.list, mv)
	q.byID[mv.ID] = mv
	q.done[mv.ID] = make(chan struct{})
	q.mu.Unlock()
	// the first run: where they were is kept, a moves, then the kill
	var prog moveProg
	for _, id := range []string{aID, bID} {
		it := l.Get(id)
		prog.From = append(prog.From, moveSrc{MoveFrom: l.moveFrom(it), Ext: ".jpg", Size: it.Size})
	}
	l.opProgress(mv, prog)
	if _, err := l.MoveAs([]string{aID}, "B", "dst", nil); err != nil {
		t.Fatal(err)
	}
	rotArgs, _ := json.Marshal(RotateArgs{IDs: []string{aID}, Deg: 90})
	rot := &Op{ID: "r1", Kind: OpRotate, By: "anna", At: nowUnix(), Args: rotArgs, State: "queued"}
	q.mu.Lock()
	q.list = append(q.list, rot)
	q.byID[rot.ID] = rot
	q.done[rot.ID] = make(chan struct{})
	q.mu.Unlock()
	l.runQueued()
	m := l.OpStatus("m1")
	if m.State != "done" || len(m.Res.IDs) != 2 || m.Res.IDs[aID] != byRel(l, "dst/a.jpg").ID {
		t.Fatalf("move: %+v %+v", m, m.Res)
	}
	if r := l.OpStatus("r1"); r.Res.Changed != 1 {
		t.Errorf("rotation missed a: %+v", r.Res)
	}
	if list, _, _ := l.History("", 5, ChMove, ""); len(list) != 1 || len(list[0].Items) != 2 {
		t.Errorf("history: %+v", list)
	}
}

// A move killed midway does not take a file that was in the target folder
// before for the one it moved.
func TestOpsMoveKilledLookalike(t *testing.T) {
	l, _ := testLib(t, "src/a.jpg", "dst/a.jpg")
	aID := byRel(l, "src/a.jpg").ID
	if byRel(l, "src/a.jpg").Size != byRel(l, "dst/a.jpg").Size {
		t.Skip("the test files differ in size")
	}
	args, _ := json.Marshal(MoveArgs{IDs: []string{aID}, Root: "B", Dir: "dst"})
	mv := &Op{ID: "m1", Kind: OpMove, By: "anna", At: nowUnix(), Args: args}
	it := l.Get(aID)
	prog := moveProg{From: []moveSrc{{MoveFrom: l.moveFrom(it), Ext: ".jpg", Size: it.Size}}}
	prog.Were = l.lookalikes(0, "dst", prog.From)
	mv.Prog, _ = json.Marshal(prog)
	if _, err := l.MoveAs([]string{aID}, "B", "dst", nil); err != nil { // moved, then the kill
		t.Fatal(err)
	}
	l.ops.mu.Lock()
	clear(l.ops.moved) // the kill came before the move was noted
	l.ops.mu.Unlock()
	res, err := l.runOp(mv)
	if err != nil {
		t.Fatal(err)
	}
	if want := byRel(l, "dst/a (2).jpg"); want == nil || res.IDs[aID] != want.ID {
		t.Errorf("found %s, want dst/a (2).jpg", res.IDs[aID])
	}
}

// Any move (not only one in the queue: the rules, undo) is followed, also
// after a restart; an edit that went on after the rules filed its photo
// shifts it once.
func TestOpsFollowAnyMove(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	aID, bID := byRel(l, "a.jpg").ID, byRel(l, "b.jpg").ID
	t0 := byRel(l, "b.jpg").Taken
	// b: an edit that shifted it, then the rules filed it, then the stop
	e := Edit{Shift: 3600}
	before := l.EditBefores([]string{bID}, e)
	if _, err := l.EditItems([]string{bID}, e); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Move([]string{aID, bID}, "B", "dst"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(EditArgs{IDs: []string{bID}, Edit: e, Info: map[string]any{"fields": []string{"taken"}}})
	prog, _ := json.Marshal(before)
	l.ops.mu.Lock()
	l.ops.write(opEvent{Op: &Op{ID: "e1", Kind: OpEdit, By: "anna", At: nowUnix(), Args: args, State: "running", Prog: prog}})
	l.ops.mu.Unlock()

	l2 := reopen(t, l)
	l2.runQueued()
	if op := l2.OpStatus("e1"); op.State != "done" {
		t.Fatalf("edit: %+v", op)
	}
	if got := byRel(l2, "dst/b.jpg").Taken; got != t0+3600 {
		t.Errorf("b shifted by %d, want 3600", got-t0)
	}
	op, err := l2.Enqueue(OpRotate, "anna", false, RotateArgs{IDs: []string{aID}, Deg: 90})
	if err != nil || op.Res.Changed != 1 {
		t.Errorf("rotation with the id from before the move: %v %+v", err, op.Res)
	}
}

// Resolving duplicates done again after a stop (the notes merged, the copies
// not deleted yet) does not copy notes twice and reports the copies deleted.
func TestOpsDupesAgain(t *testing.T) {
	l, _ := dupeLib(t)
	groups, _ := l.Dupes()
	var g DupeGroup
	for _, x := range groups {
		if x.Class == DupSame {
			g = x
		}
	}
	keep := l.Get(g.Keep[0])
	var removed []string
	for _, id := range g.Items {
		if id != keep.ID {
			removed = append(removed, id)
		}
	}
	if _, err := l.AddNote("ben", removed[0], "Oma am Strand"); err != nil {
		t.Fatal(err)
	}
	var rem []*Item
	for _, id := range removed {
		rem = append(rem, l.Get(id))
	}
	var r0 EditResult
	l.mergeInto(keep, rem, "admin", &r0) // the first run, then the stop
	args, _ := json.Marshal(DupesArgs{Groups: []DupeResolution{{Keep: []string{keep.ID}, Remove: removed}}})
	op := &Op{ID: "x1", Kind: OpDupes, By: "admin", At: nowUnix(), Args: args}
	res, err := l.runOp(op)
	if err != nil || len(res.Entries) != len(removed) {
		t.Fatalf("%v %+v", err, res)
	}
	if ns := l.NotesOf(l.Get(keep.ID)); len(ns) != 1 {
		t.Errorf("notes: %d, want 1", len(ns))
	}
	// and once more (all deleted before the stop)
	if res, err := l.runOp(op); err != nil || len(res.Entries) != len(removed) {
		t.Errorf("again: %v %+v", err, res)
	}
}

// Urgent recognition follows a file that moves before it runs.
func TestUrgentFollowsMove(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	l.aiJob.on = true
	l.aiJob.urgentWake = make(chan struct{}, 1)
	l.aiJob.wake = make(chan struct{}, 1)
	aID := byRel(l, "a.jpg").ID
	l.markUrgent(ChAI, []string{aID})
	if _, err := l.Move([]string{aID}, "B", "dst"); err != nil {
		t.Fatal(err)
	}
	newID := byRel(l, "dst/a.jpg").ID
	l.aiJob.urgent.mu.Lock()
	ok := l.aiJob.urgent.ids[newID] && !l.aiJob.urgent.ids[aID]
	l.aiJob.urgent.mu.Unlock()
	if !ok {
		t.Error("urgent work did not follow the move")
	}
}

// Moves are kept while work is open, however old (it was asked for with the
// ids from before).
func TestOpsMovesKeptWhileWorkOpen(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	aID := byRel(l, "a.jpg").ID
	if _, err := l.Move([]string{aID}, "B", "dst"); err != nil {
		t.Fatal(err)
	}
	q := l.ops
	q.mu.Lock()
	for id, m := range q.moved {
		q.moved[id] = movedTo{m.To, m.At - 2*3600, m.Seq} // long ago
	}
	q.compact()
	args, _ := json.Marshal(RotateArgs{IDs: []string{aID}, Deg: 90})
	q.write(opEvent{Op: &Op{ID: "r1", Kind: OpRotate, By: "anna", At: nowUnix(), Args: args, State: "queued"}})
	q.mu.Unlock()
	l2 := reopen(t, l)
	l2.runQueued()
	if r := l2.OpStatus("r1"); r.State != "done" || r.Res.Changed != 1 {
		t.Errorf("rotation lost its photo: %+v", r.Res)
	}
}

// Many photos turn a few at a time, each once.
func TestOpsRotateMany(t *testing.T) {
	var files []string
	for i := range 30 {
		files = append(files, fmt.Sprintf("p%02d.jpg", i))
	}
	l, _ := testLib(t, files...)
	var ids []string
	for _, f := range files {
		ids = append(ids, byRel(l, f).ID)
	}
	op, err := l.Enqueue(OpRotate, "anna", false, RotateArgs{IDs: append(ids, ids[0]), Deg: 90})
	if err != nil || op.State != "done" || op.Res.Changed != 30 || len(op.Steps) != 30 {
		t.Fatalf("%v %+v steps %d", err, op.Res, len(op.Steps))
	}
	for _, f := range files {
		if it := byRel(l, f); it.Rot != 90 {
			t.Errorf("%s: %d°", f, it.Rot)
		}
	}
}

// While urgent work waits, a wake-up ends the wait for the window; without
// it, not.
func TestUrgentWaiterWakes(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	j := &l.aiJob
	j.wake, j.urgentWake = make(chan struct{}, 1), make(chan struct{}, 1)
	ctx, stop := context.WithCancel(context.Background())
	waiting := make(chan struct{})
	go l.urgentWaiter(ctx, j, waiting, stop)
	j.wake <- struct{}{}
	select {
	case <-ctx.Done():
		t.Fatal("a wake-up without urgent work ended the wait")
	case <-time.After(100 * time.Millisecond):
	}
	j.urgent.mu.Lock()
	j.urgent.ids = map[string]bool{"x": true}
	j.urgent.mu.Unlock()
	j.wake <- struct{}{}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Error("the wait went on with urgent work")
	}
	close(waiting)
}

// A rotation that turned a photo before a stop, which then moved, does not
// turn it again.
func TestOpsRotateFollowsMove(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg")
	aID, bID := byRel(l, "a.jpg").ID, byRel(l, "b.jpg").ID
	if _, err := l.Rotate([]string{aID}, 90, "anna"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(RotateArgs{IDs: []string{aID, bID}, Deg: 90})
	l.ops.mu.Lock()
	l.ops.write(opEvent{Op: &Op{ID: "r1", Kind: OpRotate, By: "anna", At: nowUnix(), Args: args, State: "running"}})
	l.ops.write(opEvent{ID: "r1", Step: map[string]string{aID: "1"}})
	l.ops.mu.Unlock()
	if _, err := l.Move([]string{aID}, "B", "dst"); err != nil { // the rules filed it
		t.Fatal(err)
	}
	l2 := reopen(t, l)
	l2.runQueued()
	if a := byRel(l2, "dst/a.jpg"); a.Rot != 90 {
		t.Errorf("a: %d°, want 90", a.Rot)
	}
	if b := byRel(l2, "b.jpg"); b.Rot != 90 {
		t.Errorf("b: %d°, want 90", b.Rot)
	}
}

// Work done again after a restart keeps one entry in the history.
func TestOpsHistoryOnce(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	aID := byRel(l, "a.jpg").ID
	args, _ := json.Marshal(RotateArgs{IDs: []string{aID}, Deg: 90})
	op := &Op{ID: "r1", Kind: OpRotate, By: "anna", At: nowUnix(), Args: args}
	for range 2 {
		if _, err := l.runOp(op); err != nil {
			t.Fatal(err)
		}
	}
	if list, _, _ := l.History("", 10, ChRotate, ""); len(list) != 1 {
		t.Errorf("%d entries", len(list))
	}
	l2 := reopen(t, l)
	if list, _, _ := l2.History("", 10, ChRotate, ""); len(list) != 1 || len(list[0].Items) != 1 {
		t.Errorf("after a restart: %+v", list)
	}
	if a := byRel(l2, "a.jpg"); a.Rot != 90 {
		t.Errorf("%d°", a.Rot)
	}
}

// A queued edit the rules file at once keeps the new id in its result (the
// viewer follows the photo) and writes once.
func TestOpsEditFiled(t *testing.T) {
	l, _ := testLib(t, "2019/06 Juni Lübbecke/a.jpg")
	if err := l.SetRules(layoutRules); err != nil {
		t.Fatal(err)
	}
	aID := byRel(l, "2019/06 Juni Lübbecke/a.jpg").ID
	place := "Berlin"
	op, err := l.Enqueue(OpEdit, "anna", true, EditArgs{IDs: []string{aID}, Edit: Edit{Place: &place}, Info: map[string]any{"fields": []string{"place"}}})
	if err != nil || op.State != "done" {
		t.Fatal(err, op)
	}
	moved := byRel(l, "2019/06 Juni Berlin/a.jpg")
	if moved == nil || op.Res.IDs[aID] != moved.ID || op.Res.Moved != 1 {
		t.Errorf("result %+v", op.Res)
	}
	var args EditArgs
	json.Unmarshal(op.Args, &args)
	if args.IDs[0] != aID {
		t.Errorf("done again: %v", args.IDs)
	}
}
