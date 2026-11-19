package gallery

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// The history keeps every change of the last days (Options.TrashDays, the
// same days the trash keeps deleted files; never emptied: a year), so an
// admin sees who changed what and can undo it while the old state is still
// there. A change keeps only what undoing needs — the values before, the
// trash entries of a deletion, the folders of a move — never a copy of a
// file: a deletion is undone from the trash, a rotation by turning back.
//
// The file history.jsonl in the cache directory has one event per line: a
// change ({"c":…}), an undo ({"u":…}) or new ids after a move ({"rn":…}).
// It is written anew without the old events at start and now and then.

// Change is one change, by an account or by Schuhkarton itself.
type Change struct {
	ID       string          `json:"id"`
	At       int64           `json:"at"`
	By       string          `json:"by,omitempty"` // account; empty: Schuhkarton itself (rules, the trash)
	Kind     string          `json:"kind"`
	Items    []string        `json:"items,omitempty"` // item ids, followed through moves
	Names    []string        `json:"names,omitempty"` // file names, also of items that are gone
	Info     map[string]any  `json:"info,omitempty"`  // what it was about, for the list
	Undo     json.RawMessage `json:"undo,omitempty"`  // what undoing needs (by kind)
	Undone   int64           `json:"undone,omitempty"`
	UndoneBy string          `json:"undoneBy,omitempty"`
	// Op is the queued work that made it: the same work done again after a
	// restart replaces it, not adds another one
	Op string `json:"op,omitempty"`
}

// The kinds of changes.
const (
	ChEdit      = "edit"      // date, place, category, text, position
	ChRotate    = "rotate"    // Info deg
	ChDelete    = "delete"    // into the trash; Undo: trash entry ids
	ChRestore   = "restore"   // out of the trash
	ChPurge     = "purge"     // trash emptied: gone for good
	ChMove      = "move"      // Undo: []MoveFrom
	ChFaces     = "faces"     // face names; Undo: labels before (RestoreFaceLabels), and hints before (HintUndoPrefix)
	ChHide      = "hide"      // a person hidden; Undo: {"hid"}
	ChShow      = "show"      // a hidden person shown again
	ChNote      = "note"      // Undo: noteUndo
	ChMark      = "mark"      // marked for deletion; Undo: {"user","on"}
	ChUnmarkAll = "unmarkall" // everybody's marks removed; Undo: item id -> accounts
	ChFav       = "fav"       // Undo: {"user","on"}
	ChAI        = "ai"        // description made again; Undo: item id -> description before
	ChOCR       = "ocr"
	ChSpeech    = "speech"
	ChBinClear  = "binclear" // a trash emptied (only hidden); Undo: {"user","entries"}
)

// historyMax keeps the file small also when nothing is ever forgotten.
const historyMax = 50000

type history struct {
	mu      sync.Mutex
	undoMu  sync.Mutex // one undo at a time
	list    []*Change  // oldest first
	byID    map[string]*Change
	path    string
	days    int
	appends int
	// recent moves (old id -> new id): a change recorded right after its
	// operation takes along a move that slipped in between
	recent []recentRename
}

type recentRename struct {
	at  int64
	ids map[string]string
}

func newHistory(dir string, days int) *history {
	h := &history{byID: map[string]*Change{}, path: filepath.Join(dir, "history.jsonl"), days: days}
	h.load()
	return h
}

type histEvent struct {
	C   *Change           `json:"c,omitempty"`
	U   string            `json:"u,omitempty"`
	At  int64             `json:"at,omitempty"`
	By  string            `json:"by,omitempty"`
	Ren map[string]string `json:"rn,omitempty"`
}

// keepFrom is the oldest time still kept.
func (h *history) keepFrom() int64 {
	d := h.days
	if d <= 0 {
		d = 365
	}
	return nowUnix() - int64(d)*86400
}

func (h *history) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64<<20)
	for sc.Scan() {
		var ev histEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		h.apply(ev)
	}
	f.Close()
	h.prune()
	h.compact()
}

// apply takes one event into the list; the caller holds mu (or loads).
func (h *history) apply(ev histEvent) {
	switch {
	case ev.C != nil:
		if old := h.byID[ev.C.ID]; old != nil {
			*old = *ev.C // the same work done again after a restart
		} else {
			h.list = append(h.list, ev.C)
			h.byID[ev.C.ID] = ev.C
		}
	case ev.U != "":
		if c := h.byID[ev.U]; c != nil {
			c.Undone, c.UndoneBy = ev.At, ev.By
		}
	case len(ev.Ren) > 0:
		h.renameItems(ev.Ren)
	}
}

// prune drops what is older than the days kept; the caller holds mu.
func (h *history) prune() int {
	from := h.keepFrom()
	sort.SliceStable(h.list, func(a, b int) bool { return h.list[a].At < h.list[b].At })
	kept := h.list[:0:0]
	for i, c := range h.list {
		if c.At < from || len(h.list)-i > historyMax {
			delete(h.byID, c.ID)
			continue
		}
		kept = append(kept, c)
	}
	n := len(h.list) - len(kept)
	h.list = kept
	return n
}

// compact writes the file anew with what is kept; the caller holds mu.
func (h *history) compact() {
	var b strings.Builder
	for _, c := range h.list {
		if line, err := json.Marshal(histEvent{C: c}); err == nil {
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	if err := writeFileAtomic(h.path, []byte(b.String())); err == nil {
		h.appends = 0
	}
}

// write appends one event; now and then the file is written anew instead.
// The caller holds mu.
func (h *history) write(ev histEvent) {
	h.appends++
	if h.appends > 500 {
		h.prune()
		h.compact()
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(h.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	f.Write(append(line, '\n'))
	f.Close()
}

func newChangeID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// add records a change. Moves of the last minute that slipped in between its
// operation and now are taken along, for ids that name no file any more
// (stale: alive tells; nil: all).
func (h *history) add(c *Change, alive func(string) bool) {
	if h == nil || c == nil {
		return
	}
	c.ID, c.At = newChangeID(), nowUnix()
	h.mu.Lock()
	var same *Change
	if c.Op != "" {
		for i := len(h.list) - 1; i >= 0; i-- {
			if h.list[i].Op == c.Op {
				same = h.list[i]
				break
			}
		}
	}
	if same != nil && same.Undone != 0 {
		h.mu.Unlock() // undone meanwhile: stays undone
		return
	}
	if same != nil {
		c.ID, c.At = same.ID, same.At
	}
	for _, r := range h.recent { // oldest first
		if r.at < c.At-60 {
			continue
		}
		ids := r.ids
		if alive != nil {
			ids = map[string]string{}
			for o, n := range r.ids {
				if !alive(o) { // a new file at the old path keeps its own changes
					ids[o] = n
				}
			}
		}
		h.renameChange(c, ids)
	}
	if same != nil {
		*same = *c
	} else {
		h.list = append(h.list, c)
		h.byID[c.ID] = c
	}
	h.write(histEvent{C: c})
	h.expire()
	h.mu.Unlock()
}

// expire forgets what has become too old (and writes the file anew then);
// the caller holds mu.
func (h *history) expire() {
	if len(h.list) > 0 && (h.list[0].At < h.keepFrom() || len(h.list) > historyMax) && h.prune() > 0 {
		h.compact()
	}
}

// renameItems follows items to their new ids after a move, also in what
// undoing needs (on load too); the caller holds mu.
func (h *history) renameItems(ids map[string]string) {
	for _, c := range h.list {
		h.renameChange(c, ids)
	}
}

func (h *history) renameChange(c *Change, ids map[string]string) {
	for i, id := range c.Items {
		if n, ok := ids[id]; ok {
			c.Items[i] = n
		}
	}
	if len(c.Undo) > 0 {
		c.Undo = renameInJSON(c.Undo, ids)
	}
}

// rename follows moved items (old id -> new id) in the changes kept.
func (h *history) rename(ids map[string]string) {
	if h == nil || len(ids) == 0 {
		return
	}
	h.mu.Lock()
	h.renameItems(ids)
	h.write(histEvent{Ren: ids})
	now := nowUnix()
	keep := h.recent[:0]
	for _, r := range h.recent {
		if r.at >= now-60 {
			keep = append(keep, r)
		}
	}
	h.recent = append(keep, recentRename{at: now, ids: ids})
	h.mu.Unlock()
}

// renameInJSON replaces item ids in what undoing needs: values that are ids,
// and keys of the forms "id" and "id:index".
func renameInJSON(raw json.RawMessage, ids map[string]string) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			if n, ok := ids[t]; ok {
				return n
			}
			return t
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
			return t
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, val := range t {
				nk := k
				pre, rest := "", k
				for _, p := range []string{HintUndoPrefix, TwinUndoPrefix} { // "hint:id:index", "twin:id:index"
					if r, ok := strings.CutPrefix(k, p); ok {
						pre, rest = p, r
					}
				}
				if n, ok := ids[rest]; ok {
					nk = pre + n
				} else if i := strings.LastIndex(rest, ":"); i > 0 { // "id:index" (the id may be "trash:entry")
					if n, ok := ids[rest[:i]]; ok {
						nk = pre + n + rest[i:]
					}
				}
				out[nk] = walk(val)
			}
			return out
		}
		return x
	}
	b, err := json.Marshal(walk(v))
	if err != nil {
		return raw
	}
	return b
}

// --- recording ------------------------------------------------------------------

// Record keeps a change; undo is marshalled as what undoing needs.
func (l *Library) Record(c Change, undo any) {
	if l.hist == nil {
		return
	}
	if undo != nil {
		if b, err := json.Marshal(undo); err == nil {
			c.Undo = b
		}
	}
	l.hist.add(&c, func(id string) bool { return l.Get(id) != nil })
}

// MoveFrom is where a moved item was: undoing moves it back there, under
// its name then if that is free.
type MoveFrom struct {
	ID   string `json:"id"` // the item, followed to its new id
	Root string `json:"root"`
	Dir  string `json:"dir"`
	Name string `json:"name,omitempty"` // the file name without extension
}

// MoveFroms lists where items are now, for undoing a move.
func (l *Library) MoveFroms(ids []string) []MoveFrom {
	var out []MoveFrom
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			out = append(out, l.moveFrom(it))
		}
	}
	return out
}

func (l *Library) moveFrom(it *Item) MoveFrom {
	d := path.Dir(it.Rel)
	if d == "." {
		d = ""
	}
	base := path.Base(it.Rel)
	return MoveFrom{ID: it.ID, Root: l.opt.Roots[it.Root].Name, Dir: d, Name: strings.TrimSuffix(base, path.Ext(base))}
}

// MovedTo keeps the moves that happened: ids are the new ones.
func MovedTo(froms []MoveFrom, idMap map[string]string) (moved []MoveFrom, ids []string) {
	for _, f := range froms {
		if n, ok := idMap[f.ID]; ok {
			f.ID = n
			moved = append(moved, f)
			ids = append(ids, n)
		}
	}
	return moved, ids
}

// Names returns the file names of items.
func (l *Library) Names(ids []string) []string {
	var out []string
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			out = append(out, path.Base(it.Rel))
		}
	}
	return out
}

// EditBefore is what an edit changed on an item, before it: only the fields
// the edit set are there.
type EditBefore struct {
	ID       string   `json:"id"`
	Taken    *int64   `json:"taken,omitempty"`
	Place    *string  `json:"place,omitempty"`
	Category *string  `json:"category,omitempty"`
	Desc     *string  `json:"desc,omitempty"`
	GPS      *GPSEdit `json:"gps,omitempty"`
}

// EditBefores keeps the values an edit of ids is about to change (with
// their Live Photo videos, which the edit changes too).
func (l *Library) EditBefores(ids []string, e Edit) []EditBefore {
	items, companion := l.withLive(ids)
	return l.editBefores(items, companion, e)
}

// editBefores keeps, for each item, the fields the edit sets: a Live Photo
// video (companion) gets no text.
func (l *Library) editBefores(items []*Item, companion map[string]bool, e Edit) []EditBefore {
	var out []EditBefore
	for _, it := range items {
		b := EditBefore{ID: it.ID}
		if e.Taken != nil || e.Shift != 0 {
			t := it.Taken
			b.Taken = &t
		}
		if e.Place != nil {
			v := it.Place
			b.Place = &v
		}
		if e.Category != nil {
			v := it.Category
			b.Category = &v
		}
		if e.Desc != nil && !companion[it.ID] {
			v := it.Desc
			b.Desc = &v
		}
		if e.GPS != nil {
			if it.GPS {
				b.GPS = &GPSEdit{Lat: it.Lat, Lon: it.Lon}
			} else {
				b.GPS = &GPSEdit{Clear: true}
			}
		}
		out = append(out, b)
	}
	return out
}

// PersonLabels returns the labels of a person's faces, for undoing a change
// of all of them.
func (l *Library) PersonLabels(id string) map[string]string {
	s := l.people
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	if p := s.byID[id]; p != nil {
		for _, r := range p.refs {
			out[r.key()] = s.labels[r.key()]
		}
	}
	return out
}

// FaceLabels returns the labels of faces ("item:index").
func (l *Library) FaceLabels(keys []string) map[string]string {
	s := l.people
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	for _, k := range keys {
		out[k] = s.labels[k]
	}
	return out
}

// NoteOf returns one note of an item.
func (l *Library) NoteOf(itemID, noteID string) (Note, bool) {
	l.notes.mu.Lock()
	defer l.notes.mu.Unlock()
	k, i := l.findNote(itemID, noteID)
	if i < 0 {
		return Note{}, false
	}
	return *l.notes.items[k][i], true
}

// NoteUndo is what undoing a note change needs.
type NoteUndo struct {
	Op   string `json:"op"` // add, edit, delete
	Item string `json:"item"`
	Note string `json:"note,omitempty"`
	User string `json:"user"`
	Text string `json:"text,omitempty"` // the text before (edit, delete)
	Was  *Note  `json:"was,omitempty"`  // delete: the note as it was (it comes back with its id)
}

// restoreNote puts a deleted note back as it was: same id, author and time,
// so earlier changes of it can still be undone.
func (l *Library) restoreNote(itemID string, n Note) error {
	l.notes.mu.Lock()
	k, i := l.findNote(itemID, n.ID)
	if k == "" {
		l.notes.mu.Unlock()
		return ErrNoteMissing
	}
	if i < 0 {
		cp := n
		list := append(l.notes.items[k], &cp)
		sort.SliceStable(list, func(a, b int) bool { return list[a].At < list[b].At })
		l.notes.items[k] = list
	}
	err := l.saveNotes()
	l.notes.mu.Unlock()
	l.notesChanged()
	return err
}

// BinClearUndo is what undoing an emptied trash needs: whose, and which entries.
type BinClearUndo struct {
	User    string   `json:"user"`
	Entries []string `json:"entries"`
}

// ToggleUndo is what undoing a mark or a favorite needs.
type ToggleUndo struct {
	User string `json:"user"`
	On   bool   `json:"on"`
}

// MarksOf lists the accounts that marked each item.
func (l *Library) MarksOf(ids []string) map[string][]string {
	out := map[string][]string{}
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			for _, m := range l.MarkedBy(it) {
				out[id] = append(out[id], m.User)
			}
		}
	}
	return out
}

// Changing returns the items whose mark or favorite changes when set to on.
func (l *Library) Changing(ids []string, on bool, is func(*Item) bool) []string {
	var out []string
	for _, id := range ids {
		if it := l.Get(id); it != nil && is(it) != on {
			out = append(out, id)
		}
	}
	return out
}

// RecognizedBefore keeps the descriptions, texts or words of items before
// they are made again (kind ChAI, ChOCR or ChSpeech).
func (l *Library) RecognizedBefore(kind string, ids []string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, id := range ids {
		it := l.Get(id)
		if it == nil {
			continue
		}
		var v any
		switch kind {
		case ChAI:
			if a := l.AIOf(it); a != nil {
				v = a
			}
		case ChOCR:
			if o := l.OCROf(it); o != nil {
				v = o
			}
		case ChSpeech:
			if s := l.speechOf(it); s != nil {
				v = s
			}
		}
		if v == nil {
			continue
		}
		if b, err := json.Marshal(v); err == nil {
			out[id] = b
		}
	}
	return out
}

// --- the list and undo -------------------------------------------------------------

// ChangeView is a change for the list, with whether it can still be undone.
type ChangeView struct {
	*Change
	Undo    json.RawMessage `json:"undo,omitempty"` // not sent
	CanUndo bool            `json:"canUndo"`
	Why     string          `json:"why,omitempty"` // why not
	Trash   []string        `json:"trash,omitempty"`
}

// History lists the changes, newest first: after the change with id before
// ("": from the newest), at most limit, of a kind and an account ("-":
// Schuhkarton itself) if given.
func (l *Library) History(before string, limit int, kind, by string) (out []ChangeView, days int, more bool) {
	if l.hist == nil {
		return nil, 0, false
	}
	// copies: moves and undos change the kept ones meanwhile
	l.hist.mu.Lock()
	l.hist.expire()
	list := make([]*Change, len(l.hist.list))
	for i, c := range l.hist.list {
		cp := *c
		cp.Items = append([]string(nil), c.Items...)
		list[i] = &cp
	}
	days = l.hist.days
	l.hist.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	start := len(list) - 1
	if before != "" {
		start = -1 // forgotten meanwhile: nothing older to show
		for i, c := range list {
			if c.ID == before {
				start = i - 1
				break
			}
		}
	}
	inTrash := map[string]bool{}
	for _, e := range l.TrashList() {
		inTrash[e.ID] = true
	}
	for i := start; i >= 0; i-- {
		c := list[i]
		if kind != "" && !kindIn(c.Kind, kind) {
			continue
		}
		if by != "" && !(by == "-" && c.By == "") && c.By != by {
			continue
		}
		if len(out) == limit {
			more = true
			break
		}
		v := ChangeView{Change: c}
		v.CanUndo, v.Why = l.canUndo(c, inTrash)
		if c.Kind == ChDelete {
			var entries []string
			json.Unmarshal(c.Undo, &entries)
			for _, e := range entries {
				if inTrash[e] {
					v.Trash = append(v.Trash, e)
				}
			}
		}
		out = append(out, v)
	}
	return out, days, more
}

// kindIn: kinds is a comma separated list.
func kindIn(k, kinds string) bool {
	for _, x := range strings.Split(kinds, ",") {
		if strings.TrimSpace(x) == k {
			return true
		}
	}
	return false
}

func (l *Library) canUndo(c *Change, inTrash map[string]bool) (bool, string) {
	switch {
	case c.Undone != 0:
		return false, "rückgängig gemacht"
	case len(c.Undo) == 0 && c.Kind != ChRestore:
		return false, "lässt sich nicht zurücknehmen"
	case c.At < l.hist.keepFrom():
		return false, "zu alt"
	}
	switch c.Kind {
	case ChDelete:
		var entries []string
		json.Unmarshal(c.Undo, &entries)
		for _, e := range entries {
			if inTrash[e] {
				return true, ""
			}
		}
		return false, "nicht mehr im Papierkorb"
	case ChBinClear:
		var u BinClearUndo
		json.Unmarshal(c.Undo, &u)
		for _, e := range u.Entries {
			if inTrash[e] {
				return true, ""
			}
		}
		return false, "nicht mehr im Papierkorb"
	case ChHide, ChFaces:
		return true, ""
	case ChRotate, ChMark, ChUnmarkAll, ChFav, ChNote, ChAI, ChOCR, ChSpeech: // undone only all together
		for _, id := range c.Items {
			if l.Get(id) == nil {
				return false, "nicht mehr alle da"
			}
		}
		return true, ""
	}
	for _, id := range c.Items {
		if l.Get(id) != nil {
			return true, ""
		}
	}
	if len(c.Items) == 0 {
		return true, ""
	}
	return false, "nicht mehr da"
}

// ErrNotUndoable: the change cannot be undone (any more).
var ErrNotUndoable = errors.New("lässt sich nicht mehr zurücknehmen")

// UndoChange undoes a change by an admin (by) and marks it as undone.
func (l *Library) UndoChange(id, by string) (EditResult, error) {
	var res EditResult
	if l.hist == nil {
		return res, ErrNotUndoable
	}
	l.hist.undoMu.Lock()
	defer l.hist.undoMu.Unlock()
	l.hist.mu.Lock()
	c := l.hist.byID[id]
	var cp Change
	if c != nil {
		cp = *c
		cp.Items = append([]string(nil), c.Items...)
	}
	l.hist.mu.Unlock()
	if c == nil {
		return res, errors.New("unbekannte Änderung")
	}
	inTrash := map[string]bool{}
	for _, e := range l.TrashList() {
		inTrash[e.ID] = true
	}
	if ok, why := l.canUndo(&cp, inTrash); !ok {
		return res, fmt.Errorf("%w: %s", ErrNotUndoable, why)
	}
	var err error
	switch cp.Kind {
	case ChEdit:
		var before []EditBefore
		if err = json.Unmarshal(cp.Undo, &before); err != nil {
			break
		}
		moved := map[string]string{} // filing right away gives files new ids meanwhile
		for _, b := range before {
			if n, ok := moved[b.ID]; ok {
				b.ID = n
			}
			e := Edit{Taken: b.Taken, Place: b.Place, Category: b.Category, Desc: b.Desc, GPS: b.GPS, By: by, Alone: true}
			if e.Taken != nil && *e.Taken == 0 {
				e.Taken = nil // an unknown date stays as the edit left it
			}
			if e.Taken == nil && e.Place == nil && e.Category == nil && e.Desc == nil && e.GPS == nil {
				continue
			}
			r, e2 := l.EditItems([]string{b.ID}, e)
			if e2 != nil {
				res.warn("%s: %v", b.ID, e2)
				continue
			}
			for o, n := range r.IDs {
				moved[o] = n
				for k, v := range moved { // a file moved twice
					if v == o {
						moved[k] = n
					}
				}
			}
			res.Changed += r.Changed
			res.Warnings = append(res.Warnings, r.Warnings...)
		}
	case ChRotate:
		var u struct {
			Deg int `json:"deg"`
		}
		if err = json.Unmarshal(cp.Undo, &u); err == nil {
			res, err = l.Rotate(cp.Items, 360-u.Deg, by)
		}
	case ChDelete:
		var entries []string
		if err = json.Unmarshal(cp.Undo, &entries); err == nil {
			var still []string
			for _, e := range entries {
				if inTrash[e] {
					still = append(still, e)
				}
			}
			res, err = l.Restore(still)
		}
	case ChRestore:
		res, err = l.Trash(cp.Items, by)
	case ChMove:
		var froms []MoveFrom
		if err = json.Unmarshal(cp.Undo, &froms); err == nil {
			res, err = l.MoveBack(froms)
		}
	case ChFaces:
		var labels map[string]string
		if err = json.Unmarshal(cp.Undo, &labels); err != nil {
			break
		}
		// hints the name gave the rest of a group (a quick round): "hint:item:idx"
		hints, twins := map[string]string{}, map[string]string{}
		for k, v := range labels {
			if h, ok := strings.CutPrefix(k, HintUndoPrefix); ok {
				hints[h] = v
				delete(labels, k)
			} else if b, ok := strings.CutPrefix(k, TwinUndoPrefix); ok {
				twins[b] = v
				delete(labels, k)
			}
		}
		// faces of photos that are gone for good (or in the trash) are left out
		for k := range labels {
			if i := strings.LastIndex(k, ":"); i <= 0 || l.Get(k[:i]) == nil {
				delete(labels, k)
			}
		}
		if cp.Info["op"] == "dupes" {
			// only the faces still put aside as found twice, as they were (not one
			// named since, or another face at that place now), checked and changed
			// in one go
			var n int
			if n, err = l.undoTwins(labels, twins); err == nil && n == 0 {
				err = errors.New("die Gesichter haben sich seitdem geändert")
			}
			res.Changed = n
			break
		}
		restored := l.RestoreHints(hints) // also when the face named is gone: the group's hints are its own
		if len(labels) == 0 {
			if restored > 0 {
				res.Changed = restored
				break
			}
			err = errors.New("die Fotos sind nicht mehr da")
			break
		}
		err = l.RestoreFaceLabels(labels)
		res.Changed = len(labels)
	case ChBinClear:
		var u BinClearUndo
		if err = json.Unmarshal(cp.Undo, &u); err == nil {
			err = l.UnhideTrash(u.User, u.Entries)
			res.Changed = len(u.Entries)
		}
	case ChHide:
		var u struct {
			Hid string `json:"hid"`
		}
		if err = json.Unmarshal(cp.Undo, &u); err == nil {
			err = l.ShowHidden(u.Hid)
		}
	case ChNote:
		var u NoteUndo
		if err = json.Unmarshal(cp.Undo, &u); err != nil {
			break
		}
		switch u.Op {
		case "add":
			err = l.DeleteNote(u.User, true, u.Item, u.Note)
		case "edit":
			_, err = l.UpdateNote(u.User, u.Item, u.Note, u.Text)
		case "delete":
			if u.Was != nil {
				err = l.restoreNote(u.Item, *u.Was)
			} else {
				_, err = l.AddNote(u.User, u.Item, u.Text)
			}
		}
	case ChMark, ChFav:
		var u ToggleUndo
		if err = json.Unmarshal(cp.Undo, &u); err != nil {
			break
		}
		if cp.Kind == ChMark {
			res.Changed, err = l.SetMarks(u.User, cp.Items, !u.On)
		} else {
			err = l.SetFav(u.User, cp.Items, !u.On)
		}
	case ChUnmarkAll:
		var marks map[string][]string
		if err = json.Unmarshal(cp.Undo, &marks); err != nil {
			break
		}
		for id, users := range marks {
			for _, u := range users {
				if _, e2 := l.SetMarks(u, []string{id}, true); e2 != nil {
					res.warn("%s: %v", id, e2)
				}
			}
		}
	case ChAI, ChOCR, ChSpeech:
		var before map[string]json.RawMessage
		if err = json.Unmarshal(cp.Undo, &before); err == nil {
			res.Changed = l.restoreRecognized(cp.Kind, before)
		}
	default:
		err = ErrNotUndoable
	}
	if err == nil && res.Changed == 0 && len(res.Warnings) > 0 {
		err = errors.New(res.Warnings[0]) // nothing went back: it can be tried again
	}
	if err == nil && res.Changed == 0 && (cp.Kind == ChEdit || cp.Kind == ChRotate || cp.Kind == ChDelete || cp.Kind == ChRestore) {
		// nothing happened (the file moved meanwhile): stays open to be tried again
		err = errors.New("nichts geändert, bitte noch einmal versuchen")
	}
	if err != nil {
		return res, err
	}
	// what can be done again without harm (edits, moves, the trash both ways) stays open
	// while a part failed; a rotation that turned some is done
	if len(res.Warnings) > 0 && (cp.Kind == ChEdit || cp.Kind == ChMove || cp.Kind == ChDelete || cp.Kind == ChRestore) {
		return res, nil
	}
	l.hist.mu.Lock()
	defer l.hist.mu.Unlock()
	// a file moved while this ran: the undo may have missed it; it stays open
	// to be tried again with the new ids. Only for undos that move no files
	// themselves (those count what they did); a rotation that turned some is done.
	movesFiles := cp.Kind == ChEdit || cp.Kind == ChMove || cp.Kind == ChDelete || cp.Kind == ChRestore
	if !movesFiles && (!slices.Equal(c.Items, cp.Items) || string(c.Undo) != string(cp.Undo)) && !(cp.Kind == ChRotate && res.Changed > 0) {
		return res, errors.New("eine Datei wurde gerade verschoben, bitte noch einmal versuchen")
	}
	c.Undone, c.UndoneBy = nowUnix(), by
	l.hist.write(histEvent{U: c.ID, At: c.Undone, By: by})
	return res, nil
}

// restoreRecognized puts descriptions, texts or words back.
func (l *Library) restoreRecognized(kind string, before map[string]json.RawMessage) int {
	n := 0
	ids := make([]string, 0, len(before))
	for id := range before {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		it := l.Get(id)
		if it == nil {
			continue
		}
		// pinned first: a job finishing meanwhile then drops its result; one that
		// stored its result already has published it to the search before this
		l.recogPub.Lock()
		l.pinRecognized(kind, it)
		switch kind {
		case ChAI:
			var a AIInfo
			if json.Unmarshal(before[id], &a) == nil {
				l.storeAI(it, &a, true)
				l.search.updateAI(it.ID, &a) // searched for at once, not the text it replaced
				n++
			}
		case ChOCR:
			var o OCRInfo
			if json.Unmarshal(before[id], &o) == nil {
				l.storeOCR(it, &o, true)
				l.search.updateText(it.ID, it.Desc, l.ocrSearchText(it), l.SpeechText(it)) // with its translations
				n++
			}
		case ChSpeech:
			var s SpeechInfo
			if json.Unmarshal(before[id], &s) == nil {
				l.storeSpeech(it, &s, true)
				l.search.updateText(it.ID, it.Desc, l.ocrSearchText(it), s.Text)
				n++
			}
		}
		l.recogPub.Unlock()
	}
	if n > 0 {
		l.kickXMP()
		l.bump()
	}
	return n
}

// recogPins remember results an undo put back (kind + key -> when): a job
// that was already describing or recognizing that file drops its result for
// an hour, until it is asked for again.
type recogPins struct {
	mu sync.Mutex
	m  map[string]int64
}

func (l *Library) pinRecognized(kind string, it *Item) {
	l.pins.mu.Lock()
	if l.pins.m == nil {
		l.pins.m = map[string]int64{}
	}
	l.pins.m[kind+"\x00"+l.favKey(it)] = nowUnix()
	l.pins.mu.Unlock()
}

func (l *Library) unpinRecognized(kind string, ids []string) {
	l.pins.mu.Lock()
	defer l.pins.mu.Unlock()
	if len(l.pins.m) == 0 {
		return
	}
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			delete(l.pins.m, kind+"\x00"+l.favKey(it))
		}
	}
}

func (l *Library) recognizedPinned(kind string, it *Item) bool {
	l.pins.mu.Lock()
	defer l.pins.mu.Unlock()
	at, ok := l.pins.m[kind+"\x00"+l.favKey(it)]
	return ok && nowUnix()-at < 3600
}

// renamePins carries pins along when files move (old key -> new key).
func (l *Library) renamePins(keys map[string]string) {
	l.pins.mu.Lock()
	defer l.pins.mu.Unlock()
	for k, at := range l.pins.m {
		kind, key, _ := strings.Cut(k, "\x00")
		if n, ok := keys[key]; ok {
			delete(l.pins.m, k) // gone for good (""): another file may come to that path
			if n != "" {
				l.pins.m[kind+"\x00"+n] = at
			}
		}
	}
}

// RecognizeAgain drops the descriptions (ChAI), texts (ChOCR) or words
// (ChSpeech) of ids so they are made again, and keeps the ones before in the
// history (by: who asked), in one step locked against moves: the change names
// the files as they are.
func (l *Library) RecognizeAgain(kind string, ids []string, by string) int {
	l.opMu.Lock()
	defer l.opMu.Unlock()
	before := l.RecognizedBefore(kind, ids)
	n := 0
	switch kind {
	case ChAI:
		n = l.Redescribe(ids)
	case ChOCR:
		n = l.Rerecognize(ids)
	case ChSpeech:
		n = l.Retranscribe(ids)
	}
	if n > 0 {
		var undo any
		if len(before) > 0 {
			undo = before
		}
		l.Record(Change{By: by, Kind: kind, Items: ids, Names: l.Names(ids), Info: map[string]any{"n": n}}, undo)
		// the old results gone on disk first: a request kept over a restart
		// must not find them and count as done
		var err error
		switch kind {
		case ChAI:
			err = l.saveAI()
		case ChOCR:
			err = l.saveOCR()
		case ChSpeech:
			if err = l.saveSpeech(); err == nil {
				err = l.saveAI() // video descriptions made with the old words
			}
		}
		if err != nil {
			l.logf("%s: %v", kind, err)
		}
		l.markUrgent(kind, ids) // asked for by hand: not waiting for the background window
	}
	return n
}
