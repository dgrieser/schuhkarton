package gallery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The queue of what accounts ask for that takes a while: deleting,
// restoring, moving, rotating, editing (which writes into the files) and
// resolving duplicates. It runs right away in the background, one after the
// other (not in the background windows: somebody waits for it), so the page
// answers at once. ops.jsonl in the cache keeps it: what was not done when
// Schuhkarton stopped or was killed goes on at the next start. Every kind is
// made so that doing it again does no harm: deleted, restored and moved
// files are skipped, an edit sets the same values again (a time shift counts
// from the time before), and a rotation remembers which files it turned and
// how the one it was turning looked before. Work queued behind a move follows
// the items to their new ids. What was left at a stop runs once the folders
// are read again (the index from before may be behind).

// Op is one piece of work in the queue.
type Op struct {
	ID    string          `json:"id"`
	Kind  string          `json:"kind"`
	By    string          `json:"by,omitempty"`
	Admin bool            `json:"admin,omitempty"`
	At    int64           `json:"at"`
	Args  json.RawMessage `json:"args"`
	State string          `json:"state"` // queued, running, done, failed
	Res   *EditResult     `json:"res,omitempty"`
	Err   string          `json:"err,omitempty"`
	End   int64           `json:"end,omitempty"`
	// Prog is what a run that goes on after a restart needs (rotate: how the
	// photo it turns looked before; move: where the items were; edit: the
	// values before), Steps what it did (rotate: the items turned; move: old
	// id -> new id), noted one by one.
	Prog  json.RawMessage   `json:"prog,omitempty"`
	Steps map[string]string `json:"steps,omitempty"`
}

// The kinds of work.
const (
	OpDelete  = "delete"
	OpRestore = "restore"
	OpMove    = "move"
	OpRotate  = "rotate"
	OpEdit    = "edit"
	OpDupes   = "dupes"
)

// What each kind is asked with.
type (
	DeleteArgs struct {
		IDs   []string `json:"ids"`
		Names []string `json:"names"` // for the history, taken when asked
	}
	RestoreArgs struct {
		Entries []string `json:"entries"`
	}
	MoveArgs struct {
		IDs  []string `json:"ids"`
		Root string   `json:"root"`
		Dir  string   `json:"dir"`
	}
	RotateArgs struct {
		IDs []string `json:"ids"`
		Deg int      `json:"deg"`
	}
	EditArgs struct {
		IDs  []string       `json:"ids"`
		Edit Edit           `json:"edit"`
		Info map[string]any `json:"info"` // for the history
	}
	DupesArgs struct {
		Groups []DupeResolution `json:"groups"`
	}
)

// opKeep: finished work is shown for a while (its result is asked for).
const opKeep = time.Hour

type opQueue struct {
	mu     sync.Mutex
	list   []*Op // in order
	byID   map[string]*Op
	path   string
	wake   chan struct{}
	done   map[string]chan struct{} // closed when the op ends
	replay bool                     // work left from before the start
	moved  map[string]movedTo       // old id -> where a move put the item (any move: the queue, the rules, undo)
	seq    int64                    // counts the moves since the start
}

type movedTo struct {
	To  string `json:"to"`
	At  int64  `json:"at"`
	Seq int64  `json:"-"` // which move (since the start; 0: from before)
}

type opEvent struct {
	Op    *Op                `json:"op,omitempty"`
	ID    string             `json:"id,omitempty"`
	State string             `json:"state,omitempty"`
	Res   *EditResult        `json:"res,omitempty"`
	Err   string             `json:"err,omitempty"`
	At    int64              `json:"at,omitempty"`
	Prog  json.RawMessage    `json:"prog,omitempty"`
	Args  json.RawMessage    `json:"args,omitempty"` // the ids followed a move
	Step  map[string]string  `json:"step,omitempty"`
	Moved map[string]movedTo `json:"moved,omitempty"`
}

func newOpQueue(dir string) *opQueue {
	q := &opQueue{byID: map[string]*Op{}, path: filepath.Join(dir, "ops.jsonl"), wake: make(chan struct{}, 1),
		done: map[string]chan struct{}{}, moved: map[string]movedTo{}}
	q.load()
	return q
}

func (q *opQueue) load() {
	f, err := os.Open(q.path)
	if err != nil {
		return
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64<<20)
	for sc.Scan() {
		var ev opEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		for from, m := range ev.Moved {
			q.applyMove(from, m.To, m.At, 0)
		}
		if ev.Op != nil {
			if q.byID[ev.Op.ID] == nil {
				q.list = append(q.list, ev.Op)
				q.byID[ev.Op.ID] = ev.Op
			}
			continue
		}
		op := q.byID[ev.ID]
		if op == nil {
			continue
		}
		if ev.State != "" {
			op.State, op.Res, op.Err, op.End = ev.State, ev.Res, ev.Err, ev.At
		}
		if len(ev.Prog) > 0 {
			op.Prog = ev.Prog
		}
		if len(ev.Args) > 0 {
			op.Args = ev.Args
		}
		if len(ev.Step) > 0 {
			if op.Steps == nil {
				op.Steps = map[string]string{}
			}
			for k, v := range ev.Step {
				op.Steps[k] = v
			}
		}
	}
	f.Close()
	// what was not finished goes on; what ended long ago is forgotten
	keep := q.list[:0]
	for _, op := range q.list {
		switch {
		case op.State == "queued" || op.State == "running":
			op.State = "queued"
			q.replay = true
		case time.Since(time.Unix(op.End, 0)) > opKeep:
			delete(q.byID, op.ID)
			continue
		}
		q.done[op.ID] = make(chan struct{})
		if op.State == "done" || op.State == "failed" {
			close(q.done[op.ID])
		}
		keep = append(keep, op)
	}
	q.list = keep
	q.pruneMoved()
	q.compact()
}

// pruneMoved forgets old moves, but none while work is open (it may have
// been asked for with the ids from before); the caller holds mu.
func (q *opQueue) pruneMoved() {
	for _, op := range q.list {
		if op.State == "queued" || op.State == "running" {
			return
		}
	}
	for id, m := range q.moved {
		if time.Since(time.Unix(m.At, 0)) > opKeep {
			delete(q.moved, id)
		}
	}
}

// compact writes the file anew with what is kept; the caller holds mu. When
// it fails the old file stays (it holds the same and more).
func (q *opQueue) compact() {
	var b strings.Builder
	if len(q.moved) > 0 {
		if line, err := json.Marshal(opEvent{Moved: q.moved}); err == nil {
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	for _, op := range q.list {
		if line, err := json.Marshal(opEvent{Op: op}); err == nil {
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	writeFileAtomic(q.path, []byte(b.String()))
}

// write appends an event, synced: it must survive a kill. The caller holds mu.
func (q *opQueue) write(ev opEvent) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(q.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// applyMove remembers that a move took an item from one id to another; the
// caller holds mu (or q is not shared yet). Older ways to the same item lead
// to its new place; its new id is no old one any more.
func (q *opQueue) applyMove(from, to string, at, seq int64) {
	if from == to {
		return
	}
	for old, m := range q.moved {
		if m.To == from {
			if old == to {
				delete(q.moved, old) // back where it was once
			} else {
				q.moved[old] = movedTo{to, at, seq}
			}
		}
	}
	delete(q.moved, to)
	q.moved[from] = movedTo{to, at, seq}
}

// noteMoves keeps where moves put items (every move calls it), so work
// queued or asked for with the ids from before reaches them, also after a
// restart.
func (l *Library) noteMoves(idMap map[string]string) {
	if len(idMap) == 0 || l.ops == nil {
		return
	}
	at := nowUnix()
	ev := map[string]movedTo{}
	q := l.ops
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	for from, to := range idMap {
		if from != to {
			q.applyMove(from, to, at, q.seq)
			ev[from] = movedTo{to, at, q.seq}
		}
	}
	if len(ev) > 0 {
		if err := q.write(opEvent{Moved: ev}); err != nil {
			l.logf("queue: %v", err)
		}
	}
}

// follow returns where an item is now after the moves the queue did. An id
// that is in the library (another file at the old place, or the item back
// there) stays.
func (q *opQueue) follow(id string, have func(string) bool) string {
	return q.followSince(id, have, -1)
}

// followSince follows only moves after move number since (-1: all).
func (q *opQueue) followSince(id string, have func(string) bool, since int64) string {
	for range 20 { // a chain of moves; never a loop
		if have(id) {
			break
		}
		m, ok := q.moved[id]
		if !ok || m.To == id || m.Seq <= since {
			break
		}
		id = m.To
	}
	return id
}

// followArgs points the ids in args at where the items are now; nil: none moved.
func (q *opQueue) followArgs(kind string, args json.RawMessage, have func(string) bool) json.RawMessage {
	return q.followArgsSince(kind, args, have, -1)
}

// followArgsSince follows only moves after move number since (-1: all).
func (q *opQueue) followArgsSince(kind string, args json.RawMessage, have func(string) bool, since int64) json.RawMessage {
	if len(q.moved) == 0 {
		return nil
	}
	changed := false
	fix := func(ids []string) {
		for i, id := range ids {
			if to := q.followSince(id, have, since); to != id {
				ids[i], changed = to, true
			}
		}
	}
	var v any
	switch kind {
	case OpDelete:
		var a DeleteArgs
		if json.Unmarshal(args, &a) != nil {
			return nil
		}
		fix(a.IDs)
		v = a
	case OpMove:
		var a MoveArgs
		if json.Unmarshal(args, &a) != nil {
			return nil
		}
		fix(a.IDs)
		v = a
	case OpRotate:
		var a RotateArgs
		if json.Unmarshal(args, &a) != nil {
			return nil
		}
		fix(a.IDs)
		v = a
	case OpEdit:
		var a EditArgs
		if json.Unmarshal(args, &a) != nil {
			return nil
		}
		fix(a.IDs)
		v = a
	case OpDupes:
		var a DupesArgs
		if json.Unmarshal(args, &a) != nil {
			return nil
		}
		for _, g := range a.Groups {
			fix(g.Keep)
			fix(g.Remove)
		}
		v = a
	default:
		return nil
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// Enqueue puts work in the queue; it starts right away when nothing else runs.
func (l *Library) Enqueue(kind, by string, admin bool, args any) (*Op, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	op := &Op{ID: newChangeID(), Kind: kind, By: by, Admin: admin, At: nowUnix(), Args: b, State: "queued"}
	q := l.ops
	q.mu.Lock()
	// forget what ended long ago (the file is written anew at the next start)
	keep := q.list[:0]
	for _, o := range q.list {
		if (o.State == "done" || o.State == "failed") && time.Since(time.Unix(o.End, 0)) > opKeep {
			delete(q.byID, o.ID)
			delete(q.done, o.ID)
			continue
		}
		keep = append(keep, o)
	}
	q.list = keep
	q.pruneMoved()
	// asked for with the ids the page knew: a move may have changed them since
	if nb := q.followArgs(kind, op.Args, l.has); nb != nil {
		op.Args = nb
	}
	if err := q.write(opEvent{Op: op}); err != nil {
		q.mu.Unlock()
		return nil, fmt.Errorf("Auftrag nicht gespeichert: %w", err)
	}
	q.list = append(q.list, op)
	q.byID[op.ID] = op
	q.done[op.ID] = make(chan struct{})
	cp := *op
	q.mu.Unlock()
	if !l.opsOn.Load() { // no queue running (tools, tests): done right here
		l.runQueued()
		return l.OpStatus(op.ID), nil
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return &cp, nil
}

// runQueued does what is queued now, without the background loop.
func (l *Library) runQueued() {
	for l.opStep() {
	}
}

// OpStatus returns a copy of an op (nil: unknown).
func (l *Library) OpStatus(id string) *Op {
	q := l.ops
	q.mu.Lock()
	defer q.mu.Unlock()
	op := q.byID[id]
	if op == nil {
		return nil
	}
	cp := *op
	return &cp
}

// WaitOp waits until an op ends or d passes (or ctx ends) and returns it.
func (l *Library) WaitOp(ctx context.Context, id string, d time.Duration) *Op {
	l.ops.mu.Lock()
	ch := l.ops.done[id]
	l.ops.mu.Unlock()
	if ch != nil {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
		}
	}
	return l.OpStatus(id)
}

// OpsWaiting counts the work not done yet (for the status).
func (l *Library) OpsWaiting() int {
	q := l.ops
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, op := range q.list {
		if op.State == "queued" || op.State == "running" {
			n++
		}
	}
	return n
}

// opLoop does the work in the queue, one after the other, until ctx ends.
// Work left from before the start waits until the folders are read again.
func (l *Library) opLoop(ctx context.Context) {
	defer l.bg.Done()
	l.ops.mu.Lock()
	replay := l.ops.replay
	l.ops.mu.Unlock()
	if replay && !l.waitFirstScan(ctx) {
		return
	}
	for {
		if ctx.Err() != nil {
			return // the one running is done; the rest goes on at the next start
		}
		if l.opStep() {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-l.ops.wake:
		}
	}
}

// opStep does the next piece of work; false: nothing queued.
func (l *Library) opStep() bool {
	q := l.ops
	q.mu.Lock()
	var op *Op
	for _, o := range q.list {
		if o.State == "queued" {
			op = o
			break
		}
	}
	var seq int64
	if op != nil {
		// asked for before a move that ran since (also before a restart)
		if nb := q.followArgs(op.Kind, op.Args, l.has); nb != nil {
			op.Args = nb
			if err := q.write(opEvent{ID: op.ID, Args: nb}); err != nil {
				l.logf("queue: %v", err)
			}
		}
		if nb := q.followProg(op, l.has); nb != nil {
			op.Prog = nb
			if err := q.write(opEvent{ID: op.ID, Prog: nb}); err != nil {
				l.logf("queue: %v", err)
			}
		}
		if steps := q.followSteps(op, l.has); steps != nil {
			for k, v := range steps {
				op.Steps[k] = v
			}
			if err := q.write(opEvent{ID: op.ID, Step: steps}); err != nil {
				l.logf("queue: %v", err)
			}
		}
		seq = q.seq
		op.State = "running"
		if err := q.write(opEvent{ID: op.ID, State: "running"}); err != nil {
			l.logf("queue: %v", err) // runs anyway: queued in the file, it runs again (no harm)
		}
	}
	q.mu.Unlock()
	if op == nil {
		return false
	}
	res, err := l.runOp(op)
	// a move in the background (the rules) between finding the items and
	// changing them: done again with their new ids (it does no harm twice;
	// rotations and moves look the items up in the same locked step). Only
	// moves while it ran count: its own items gone (deleted) stay gone.
	for range 3 {
		if op.Kind != OpDelete && op.Kind != OpEdit && op.Kind != OpDupes {
			break
		}
		// the items it moved itself (an edit the rules filed at once) are done
		own := func(id string) bool { _, ok := res.IDs[id]; return ok || l.has(id) }
		q.mu.Lock()
		nb := q.followArgsSince(op.Kind, op.Args, own, seq)
		if nb != nil {
			op.Args = nb
			if err := q.write(opEvent{ID: op.ID, Args: nb}); err != nil {
				l.logf("queue: %v", err)
			}
			if np := q.followProgSince(op, own, seq); np != nil {
				op.Prog = np
				if err := q.write(opEvent{ID: op.ID, Prog: np}); err != nil {
					l.logf("queue: %v", err)
				}
			}
		}
		seq = q.seq
		q.mu.Unlock()
		if nb == nil {
			break
		}
		again, aerr := l.runOp(op)
		res, err = mergeResults(res, again), aerr
	}
	q.mu.Lock()
	op.State, op.End = "done", nowUnix()
	if err != nil {
		op.State, op.Err = "failed", err.Error()
	}
	op.Res = &res
	if err := q.write(opEvent{ID: op.ID, State: op.State, Res: op.Res, Err: op.Err, At: op.End}); err != nil {
		l.logf("queue: %v", err)
	}
	if ch := q.done[op.ID]; ch != nil {
		close(ch)
	}
	q.mu.Unlock()
	return true
}

// followProg points what a run before a stop kept (an edit: the values
// before; a rotation: how the photos looked) at where the items are now (the
// rules filed them meanwhile); nil: none moved.
func (q *opQueue) followProg(op *Op, have func(string) bool) json.RawMessage {
	return q.followProgSince(op, have, -1)
}

// followProgSince follows only moves after move number since (-1: all).
func (q *opQueue) followProgSince(op *Op, have func(string) bool, since int64) json.RawMessage {
	if len(op.Prog) == 0 || len(q.moved) == 0 {
		return nil
	}
	changed := false
	fix := func(id *string) {
		if to := q.followSince(*id, have, since); to != *id {
			*id, changed = to, true
		}
	}
	var v any
	switch op.Kind {
	case OpEdit:
		var before []EditBefore
		if json.Unmarshal(op.Prog, &before) != nil {
			return nil
		}
		for i := range before {
			fix(&before[i].ID)
		}
		v = before
	case OpRotate:
		var prog rotateProg
		if json.Unmarshal(op.Prog, &prog) != nil {
			return nil
		}
		for i := range prog.Cur {
			fix(&prog.Cur[i].ID)
			if prog.Cur[i].LV != "" {
				fix(&prog.Cur[i].LV)
			}
		}
		v = prog
	default:
		return nil
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// followSteps gives the items a rotation turned before a stop under the ids
// they have now (the old ones stay too); nil: none moved.
func (q *opQueue) followSteps(op *Op, have func(string) bool) map[string]string {
	if op.Kind != OpRotate || len(op.Steps) == 0 || len(q.moved) == 0 {
		return nil
	}
	var out map[string]string
	for id, v := range op.Steps {
		if to := q.follow(id, have); to != id && op.Steps[to] == "" {
			if out == nil {
				out = map[string]string{}
			}
			out[to] = v
		}
	}
	return out
}

// opDid notes steps of an op (one line each time, not all of them again).
func (l *Library) opDid(op *Op, steps map[string]string) error {
	q := l.ops
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.write(opEvent{ID: op.ID, Step: steps}); err != nil {
		return fmt.Errorf("Fortschritt nicht gespeichert: %w", err)
	}
	if op.Steps == nil {
		op.Steps = map[string]string{}
	}
	for k, v := range steps {
		op.Steps[k] = v
	}
	return nil
}

// opSteps returns a copy of the steps an op did.
func (l *Library) opSteps(op *Op) map[string]string {
	l.ops.mu.Lock()
	defer l.ops.mu.Unlock()
	out := map[string]string{}
	for k, v := range op.Steps {
		out[k] = v
	}
	return out
}

// mergeResults joins the result of work done again after a move to the one
// before: the new ids of both (a viewer follows its photo), counts of the
// last run (it saw all the items).
func mergeResults(first, again EditResult) EditResult {
	out := again
	if len(first.IDs) > 0 {
		out.IDs = map[string]string{}
		for k, v := range first.IDs {
			out.IDs[k] = v
		}
		for k, v := range again.IDs {
			out.IDs[k] = v
		}
	}
	out.Warnings = append(append([]string{}, first.Warnings...), again.Warnings...)
	out.Moved += first.Moved
	out.Queued += first.Queued
	out.Written += first.Written
	if len(again.Entries) < len(first.Entries) {
		out.Entries = first.Entries
	}
	return out
}

// opProgress keeps what a run that goes on after a restart needs; an error:
// it is not kept, so the work must not go on.
func (l *Library) opProgress(op *Op, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	q := l.ops
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.write(opEvent{ID: op.ID, Prog: b}); err != nil {
		return fmt.Errorf("Fortschritt nicht gespeichert: %w", err)
	}
	op.Prog = b
	return nil
}

// runOp does one piece of work (a panic fails it, the queue goes on).
func (l *Library) runOp(op *Op) (res EditResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	switch op.Kind {
	case OpDelete:
		var a DeleteArgs
		if err = json.Unmarshal(op.Args, &a); err != nil {
			return res, err
		}
		res, err = l.Trash(a.IDs, op.By)
		// also what a run before a restart put in the trash
		entries := l.trashEntriesOf(a.IDs, op.At)
		if len(entries) > 0 {
			res.Entries = entries
			if err != nil && strings.Contains(err.Error(), "keine Elemente") {
				err = nil // all of them were deleted before the restart
			}
			l.Record(Change{Op: op.ID, By: op.By, Kind: ChDelete, Names: a.Names, Info: map[string]any{"n": len(entries)}}, entries)
		}
	case OpRestore:
		var a RestoreArgs
		if err = json.Unmarshal(op.Args, &a); err != nil {
			return res, err
		}
		if len(a.Entries) == 0 { // Restore(nil) would take everything
			return res, errors.New("nichts wiederherzustellen")
		}
		res, err = l.Restore(a.Entries)
		if len(res.IDs) > 0 {
			var back []string
			for _, id := range res.IDs {
				back = append(back, id)
			}
			l.Record(Change{Op: op.ID, By: op.By, Kind: ChRestore, Items: back, Names: l.Names(back), Info: map[string]any{"n": len(back)}}, nil)
		}
	case OpMove:
		var a MoveArgs
		if err = json.Unmarshal(op.Args, &a); err != nil {
			return res, err
		}
		res, err = l.runMove(op, a)
	case OpRotate:
		var a RotateArgs
		if err = json.Unmarshal(op.Args, &a); err != nil {
			return res, err
		}
		res, err = l.runRotate(op, a)
	case OpEdit:
		var a EditArgs
		if err = json.Unmarshal(op.Args, &a); err != nil {
			return res, err
		}
		e := a.Edit
		e.By, e.Record, e.Op = op.By, a.Info, op.ID
		// the values before, kept when the edit first ran: a run after a restart
		// sees the new ones already
		if e.Record == nil {
			e.Record = map[string]any{} // the values before are taken only for the history
		}
		var before []EditBefore
		if json.Unmarshal(op.Prog, &before) == nil && len(before) > 0 {
			e.Before = before
		} else {
			e.OnBefore = func(b []EditBefore) error { return l.opProgress(op, b) }
		}
		res, err = l.EditItems(a.IDs, e)
	case OpDupes:
		var a DupesArgs
		if err = json.Unmarshal(op.Args, &a); err != nil {
			return res, err
		}
		var freed int64
		res, freed, err = l.ResolveDupes(a.Groups, op.By)
		res.Freed = freed
		// also the copies a run before a restart put in the trash
		var removed []string
		for _, g := range a.Groups {
			removed = append(removed, g.Remove...)
		}
		if entries := l.trashEntriesOf(removed, op.At); len(entries) > len(res.Entries) {
			res.Entries = entries
			if err != nil && strings.Contains(err.Error(), "nichts zu löschen") {
				err = nil
			}
		}
		if len(res.Entries) > 0 {
			var names []string
			for _, e := range res.Entries {
				if te := l.TrashEntryByID(e); te != nil {
					names = append(names, pathBase(te.Rel))
				}
			}
			l.Record(Change{Op: op.ID, By: op.By, Kind: ChDelete, Names: names, Info: map[string]any{"n": len(res.Entries), "dupes": true}}, res.Entries)
		}
	default:
		err = fmt.Errorf("unbekannte Arbeit %q", op.Kind)
	}
	return res, err
}

// has tells whether an item is in the library now.
func (l *Library) has(id string) bool { return l.Get(id) != nil }

// moveProg is what a move keeps for a restart: where the items were (taken
// before the first file moved), and the new ids of those moved.
type moveProg struct {
	From []moveSrc `json:"from"`
	// Were: files in the target folder before, that a moved one could be
	// taken for (same type and size)
	Were []string `json:"were,omitempty"`
}

type moveSrc struct {
	MoveFrom
	Ext  string `json:"ext"`
	Size int64  `json:"size"`
}

// moveChunk: items moved between two notes of the progress.
const moveChunk = 25

// runMove moves the items a few at a time and keeps their new ids, so work
// queued behind it finds them also when a stop came in between. One moved
// just before a stop, before its new id was kept, is found in the folder by
// its name, type and size.
func (l *Library) runMove(op *Op, a MoveArgs) (res EditResult, err error) {
	ri, d, err := l.cleanDir(a.Root, a.Dir)
	if err != nil {
		return res, err
	}
	var prog moveProg
	json.Unmarshal(op.Prog, &prog)
	done := l.opSteps(op) // id when asked -> new id
	if prog.From == nil {
		l.opMu.Lock()
		for _, id := range a.IDs {
			if it := l.Get(id); it != nil {
				l.mu.RLock()
				src := moveSrc{MoveFrom: l.moveFrom(it), Ext: path.Ext(it.Rel), Size: it.Size}
				l.mu.RUnlock()
				prog.From = append(prog.From, src)
			}
		}
		prog.Were = l.lookalikes(ri, d, prog.From)
		err = l.opProgress(op, prog)
		l.opMu.Unlock()
		if err != nil {
			return res, err
		}
	}
	var todo []moveSrc
	for _, f := range prog.From {
		if _, ok := done[f.ID]; !ok {
			todo = append(todo, f)
		}
	}
	for len(todo) > 0 {
		n := min(moveChunk, len(todo))
		chunk := todo[:n]
		todo = todo[n:]
		if err = l.moveStep(op, chunk, ri, d, prog.Were, done, &res); err != nil {
			break
		}
	}
	res.IDs = done
	var froms []MoveFrom
	for _, f := range prog.From {
		froms = append(froms, f.MoveFrom)
	}
	if back, moved := MovedTo(froms, done); len(moved) > 0 {
		l.Record(Change{Op: op.ID, By: op.By, Kind: ChMove, Items: moved, Names: l.Names(moved),
			Info: map[string]any{"to": strings.Trim(a.Root+"/"+a.Dir, "/"), "n": len(moved)}}, back)
	}
	return res, err
}

// moveStep moves a few items in one step locked against other moves (the
// rules filing in the background): each is looked up where it is now, or,
// gone without a move known (moved by this op just before a stop), found in
// the target folder.
func (l *Library) moveStep(op *Op, chunk []moveSrc, ri int, d string, were []string, done map[string]string, res *EditResult) error {
	l.opMu.Lock()
	defer l.opMu.Unlock()
	asked := map[string]string{} // id now -> id when asked
	var ids []string
	did := map[string]string{}
	for _, f := range chunk {
		if _, ok := done[f.ID]; ok { // a Live Photo video moved with its photo
			continue
		}
		cur := l.followID(f.ID)
		switch {
		case cur != f.ID && l.inFolder(cur, ri, d):
			// moved by this op before a stop (its new id was noted with the
			// move, not yet here)
			did[f.ID], done[f.ID] = cur, cur
			res.Changed++
		case l.has(cur):
			asked[cur] = f.ID
			ids = append(ids, cur)
		case cur == f.ID:
			if n := l.findMoved(ri, d, f, done, were); n != "" {
				did[f.ID], done[f.ID] = n, n
				res.Changed++
				l.noteMoves(map[string]string{f.ID: n})
			}
		}
	}
	var err error
	if len(ids) > 0 {
		var r EditResult
		r, err = l.moveLocked(ids, ri, d, nil)
		res.Changed += r.Changed
		res.Written += r.Written
		res.Warnings = append(res.Warnings, r.Warnings...)
		for from, to := range r.IDs {
			if a, ok := asked[from]; ok {
				from = a
			}
			did[from], done[from] = to, to
		}
	}
	if len(did) > 0 {
		if derr := l.opDid(op, did); derr != nil {
			return derr
		}
	}
	return err
}

// inFolder tells whether an item is in folder d of root ri.
func (l *Library) inFolder(id string, ri int, d string) bool {
	it := l.Get(id)
	if it == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return it.Root == ri && relDir(it.Rel) == d
}

// lookalikes lists the items in folder d of root ri a moved one could be
// taken for (same type and size).
func (l *Library) lookalikes(ri int, d string, from []moveSrc) []string {
	type sig struct {
		ext  string
		size int64
	}
	want := map[sig]bool{}
	for _, f := range from {
		want[sig{f.Ext, f.Size}] = true
	}
	var out []string
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, it := range l.items {
		if it.Root == ri && relDir(it.Rel) == d && want[sig{path.Ext(it.Rel), it.Size}] {
			out = append(out, it.ID)
		}
	}
	return out
}

func relDir(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}

// findMoved finds an item a move put into folder d of root ri before a stop:
// same type and size, its name or its name with a number ("IMG (2)"); not
// one of taken, nor one that was there before. "": not found.
func (l *Library) findMoved(ri int, d string, f moveSrc, taken map[string]string, were []string) string {
	used := map[string]bool{}
	for _, to := range taken {
		used[to] = true
	}
	for _, id := range were {
		used[id] = true
	}
	best := ""
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, it := range l.items {
		if it.Root != ri || used[it.ID] || path.Ext(it.Rel) != f.Ext || it.Size != f.Size || relDir(it.Rel) != d {
			continue
		}
		stem := strings.TrimSuffix(path.Base(it.Rel), f.Ext)
		if stem == f.Name {
			return it.ID
		}
		if strings.HasPrefix(stem, f.Name+" (") && best == "" {
			best = it.ID
		}
	}
	return best
}

// rotateProg is what a rotation keeps for a restart (besides the items
// turned, in its steps): how the photos it is turning looked before (a kill
// while turning them is told apart from one before by comparing).
type rotateProg struct {
	Cur []rotateSig `json:"cur,omitempty"`
}

type rotateSig struct {
	ID     string `json:"id"`
	Orient int    `json:"orient"`
	Rot    int    `json:"rot"`
	LV     string `json:"lv,omitempty"` // the Live Photo video, turned after the photo
	LVSize int64  `json:"lvSize,omitempty"`
	LVTime int64  `json:"lvTime,omitempty"`
}

func (l *Library) rotateSigOf(it *Item) rotateSig {
	l.mu.RLock()
	sig := rotateSig{ID: it.ID, Orient: it.Orient, Rot: it.Rot, LV: it.LiveVideo}
	l.mu.RUnlock()
	if sig.LV != "" {
		if lv := l.Get(sig.LV); lv != nil {
			l.mu.RLock()
			sig.LVSize, sig.LVTime = lv.Size, lv.MTime
			l.mu.RUnlock()
		} else {
			sig.LV = ""
		}
	}
	return sig
}

// turned tells whether the photo looks other than in sig (it was turned); the
// caller holds opMu.
func (l *Library) turned(sig rotateSig) bool {
	it := l.Get(sig.ID)
	if it == nil {
		return false
	}
	now := l.rotateSigOf(it)
	return now.Orient != sig.Orient || now.Rot != sig.Rot
}

// rotateChunk: photos turned in one go (the index is written once for them).
const rotateChunk = 25

// followID returns where an item is now after moves (its id when it is in
// the library or did not move).
func (l *Library) followID(id string) string {
	l.ops.mu.Lock()
	defer l.ops.mu.Unlock()
	return l.ops.follow(id, l.has)
}

// runRotate turns the items a few at a time; after a restart it skips what it
// turned, and finishes the ones it was turning. Each step is locked against
// moves (the rules filing in the background), and the ids follow moves made
// between the steps.
func (l *Library) runRotate(op *Op, a RotateArgs) (res EditResult, err error) {
	deg := ((a.Deg % 360) + 360) % 360
	if deg == 0 || deg%90 != 0 {
		return res, errors.New("nur 90, 180 oder 270 Grad")
	}
	var prog rotateProg
	json.Unmarshal(op.Prog, &prog)
	done := l.opSteps(op)

	// the ones being turned at the stop: turned (maybe without their Live
	// Photo video), or still to do
	var redo []string
	l.opMu.Lock()
	finished := map[string]string{}
	for _, c := range prog.Cur {
		if done[c.ID] != "" || l.Get(c.ID) == nil {
			continue
		}
		if !l.turned(c) {
			redo = append(redo, c.ID)
			continue
		}
		if c.LV != "" {
			if lv := l.Get(c.LV); lv != nil && lv.Size == c.LVSize && lv.MTime == c.LVTime {
				if e := l.rotateVideo(lv, deg); e != nil {
					res.warn("%s: %v", pathBase(lv.Rel), e)
				} else {
					l.refresh([]*Item{lv})
				}
			}
		}
		res.Changed++
		finished[c.ID] = "1"
	}
	if len(finished) > 0 {
		err = l.opDid(op, finished)
		for id := range finished {
			done[id] = "1"
		}
	}
	l.opMu.Unlock()
	if err != nil {
		return res, err
	}

	todo := append(redo, a.IDs...)
	for len(todo) > 0 {
		n := min(rotateChunk, len(todo))
		chunk := todo[:n]
		todo = todo[n:]
		if err = l.rotateStep(op, chunk, deg, &prog, done, &res); err != nil {
			return res, err
		}
	}

	// the history names the items as they are now
	l.opMu.Lock()
	defer l.opMu.Unlock()
	var turned []string // in the order asked
	seen := map[string]bool{}
	for _, id := range a.IDs {
		if done[id] != "" {
			now := l.followID(id)
			if !seen[now] { // an id asked for twice counts once
				seen[now] = true
				turned = append(turned, now)
			}
		}
	}
	if len(turned) > 0 {
		var undo any
		if len(res.Warnings) == 0 {
			undo = map[string]int{"deg": deg}
		}
		l.Record(Change{Op: op.ID, By: op.By, Kind: ChRotate, Items: turned, Names: l.Names(turned), Info: map[string]any{"deg": deg}}, undo)
	}
	if res.Changed == 0 && len(res.Warnings) > 0 && len(turned) == 0 {
		err = errors.New(res.Warnings[0])
	}
	return res, err
}

// rotateStep turns a few items in one step locked against moves: how they
// look is kept first, the ones that turned are noted after.
func (l *Library) rotateStep(op *Op, chunk []string, deg int, prog *rotateProg, done map[string]string, res *EditResult) error {
	l.opMu.Lock()
	defer l.opMu.Unlock()
	prog.Cur = nil
	var ids []string
	seen := map[string]bool{}
	for _, asked := range chunk {
		if done[asked] != "" {
			continue
		}
		id := l.followID(asked) // moved since it was asked for
		it := l.Get(id)
		if it == nil || seen[id] || done[id] != "" {
			continue
		}
		seen[id] = true
		prog.Cur = append(prog.Cur, l.rotateSigOf(it))
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := l.opProgress(op, prog); err != nil {
		return err
	}
	r, e := l.rotateLocked(ids, deg, op.By, nil)
	res.Warnings = append(res.Warnings, r.Warnings...)
	if e != nil {
		return e // not kept: not done (a restart has them as before)
	}
	res.Changed += r.Changed
	did := map[string]string{}
	for _, c := range prog.Cur {
		if l.turned(c) {
			did[c.ID] = "1"
			done[c.ID] = "1"
		}
	}
	// the ids asked for, too (when they moved): the history and a restart
	// find them under either
	for _, asked := range chunk {
		if done[asked] == "" {
			if now := l.followID(asked); did[now] != "" {
				did[asked] = "1"
				done[asked] = "1"
			}
		}
	}
	if len(did) == 0 {
		return nil
	}
	return l.opDid(op, did)
}

// trashEntriesOf lists the trash entries of items (by their ids before the
// deletion) deleted since t.
func (l *Library) trashEntriesOf(ids []string, since int64) []string {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	l.trashMu.Lock()
	defer l.trashMu.Unlock()
	var out []string
	for _, e := range l.trash {
		if e.Item != nil && want[e.Item.ID] && e.Deleted >= since {
			out = append(out, e.ID)
		}
	}
	return out
}

func pathBase(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[i+1:]
	}
	return rel
}
