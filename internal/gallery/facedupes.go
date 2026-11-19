package gallery

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Duplicate faces: the same face found twice in one picture, by the two
// searches (the second one's box is taller and shifted), by a search and a
// box drawn by hand, or twice by hand. Faces close to each other are real
// (cheek to cheek, a child on a lap), so overlapping boxes alone do not
// decide. Measured on a real library (16,000 faces, 231 overlapping pairs):
// two different people named by hand covered at most 55 % of the smaller
// box and were at most 0.33 alike (both systems), while the same face found
// twice covered 78-100 % of it, mostly 0.43-0.99 alike; a box drawn by hand
// around a detected face is sometimes much less alike (another crop).
//
// So: mostly inside each other at a similar size is the same face; partly
// overlapping only when also clearly alike, barely overlapping only when
// very alike (a close-up, where the second search put two boxes on the
// halves of one face: 11 % of the box, 0.44 alike).
const (
	dupInside  = 0.7  // share of the smaller box inside the other: the same face
	dupSize    = 6    // ... when the larger box is at most this many times its size
	dupOverlap = 0.35 // share of the smaller box: the same face when also alike
	dupAlike1  = 0.45 // ... this alike (first system)
	dupAlike2  = 0.38 // ... or this alike (second system)
	// barely overlapping (two boxes on the halves of one face, a close-up):
	// the same face only when very alike (different people were at most 0.33)
	dupMinShare = 0.1
	dupClose1   = 0.5
	dupClose2   = 0.42
	// two found boxes inside each other that both systems find this little
	// alike are two faces (a child in front of a grown-up)
	dupApart = 0.2
)

// boxShare is how much of the smaller box lies inside the other (0..1), and
// how many times larger the other one is.
func boxShare(a, b Face) (share, ratio float32) {
	x1, y1 := max(a.X, b.X), max(a.Y, b.Y)
	x2, y2 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	aa, ab := a.W*a.H, b.W*b.H
	if aa <= 0 || ab <= 0 {
		return 0, 0
	}
	if x2 > x1 && y2 > y1 {
		share = (x2 - x1) * (y2 - y1) / min(aa, ab)
	}
	return share, max(aa, ab) / min(aa, ab)
}

// dupFace tells whether two faces are the same face found twice.
func dupFace(a, b Face) bool {
	if !sameFrame(a, b) || a.NotDup || b.NotDup {
		return false
	}
	share, ratio := boxShare(a, b)
	if share < dupMinShare {
		return false
	}
	c1, c2 := float32(-2), float32(-2)
	if len(a.Emb) > 0 && len(a.Emb) == len(b.Emb) {
		c1 = dot(normalize(a.Emb), normalize(b.Emb))
	}
	if len(a.Emb2) > 0 && len(b.Emb2) > 0 {
		c2 = cos2(a.Emb2, b.Emb2)
	}
	if share >= dupInside && ratio <= dupSize {
		// two found boxes the recognition clearly tells apart are two faces (a box
		// drawn by hand is another crop: its likeness says little)
		if !a.Manual && !b.Manual && c1 > -2 && c1 < dupApart && (c2 <= -2 || c2 < dupApart) {
			return false
		}
		return true
	}
	need1, need2 := float32(dupAlike1), float32(dupAlike2)
	if share < dupOverlap {
		need1, need2 = dupClose1, dupClose2
	}
	return c1 >= need1 || c2 >= need2
}

// notTwin clears that a face was put aside automatically (somebody labeled
// it now).
func (l *Library) notTwin(item string, idx int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it := l.byID[item]
	if it == nil || idx < 0 || idx >= len(it.Faces) || !it.Faces[idx].Twin {
		return
	}
	faces := append([]Face(nil), it.Faces...) // copy on write: grouping reads snapshots without the lock
	faces[idx].Twin, faces[idx].TwinID = false, ""
	it.Faces = faces
	l.saveSoon()
}

// onlyTwinsKept tells whether the faces kept when found again are all faces
// put aside as found twice (kept only by their no face, noFace: still so).
func onlyTwinsKept(faces []Face, labeled map[int]bool, noFace func(int) bool) bool {
	twins := false
	for i, f := range faces {
		if !rescanKept(f, labeled[i]) {
			continue
		}
		if !f.Twin || !noFace(i) || f.OK || f.Manual || f.NotDup {
			return false
		}
		twins = true
	}
	return twins
}

// lonelyTwins returns the faces put aside as found twice that a search
// found again (found) and whose twin is not there any more; the caller
// holds l.mu.
func lonelyTwins(faces []Face, missing map[int]bool, found func(int) bool) []int {
	// its twin, also put aside by somebody since (hidden, no face): it stays;
	// one not found again by this search (missing) is no twin any more
	inSight := func(i int) bool { return !faces[i].Twin && !faces[i].Missing && !missing[i] }
	var out []int
	for t, f := range faces {
		if !f.Twin || !found(t) {
			continue
		}
		alone := true
		for v := range faces {
			if v != t && inSight(v) && dupFace(faces[v], f) {
				alone = false
				break
			}
		}
		if alone {
			out = append(out, t)
		}
	}
	return out
}

// dupRank is how much a face is worth keeping of two that are the same:
// named by hand, approved, drawn by hand, found by the main search, sure.
func dupRank(f Face, label string) float32 {
	r := f.Score
	if approvedLabel(label) {
		r += 16
	}
	if f.OK {
		r += 8
	}
	if f.Manual {
		r += 4
	}
	if f.Src == "" {
		r += 2
	}
	return r
}

// duplicates returns the faces of an item to put aside as no face: of
// faces found twice the one less worth keeping. The faces worth keeping are
// chosen first, so each face put aside has its twin still there. Faces put
// aside already (no face, hidden) take no part; a face named by hand
// otherwise than its twin stays (somebody decided), and so does one
// approved against a twin neither named nor approved.
func duplicates(faces []Face, labels map[int]string) []int {
	out := func(i int) bool { l := labels[i]; return l == labelNoFace || l == labelHidden }
	order := make([]int, 0, len(faces))
	for i := range faces {
		if !out(i) {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		return dupRank(faces[order[a]], labels[order[a]]) > dupRank(faces[order[b]], labels[order[b]])
	})
	var kept, drop []int
	for _, i := range order {
		lose := false
		for _, k := range kept {
			if !dupFace(faces[k], faces[i]) {
				continue
			}
			lk, li := labels[k], labels[i]
			if approvedLabel(li) && li != lk {
				continue // named by hand otherwise: somebody decided
			}
			if strings.HasPrefix(li, "!") {
				continue // said not to be somebody: somebody looked at it, it stays
			}
			if approvedLabel(lk) && rejects(li, lk) {
				continue // said not to be the twin's person: somebody decided
			}
			if faces[i].OK && !approvedLabel(lk) && !faces[k].OK {
				continue
			}
			lose = true
			break
		}
		if lose {
			drop = append(drop, i)
		} else {
			kept = append(kept, i)
		}
	}
	sort.Ints(drop)
	return drop
}

// dropDuplicateFaces puts aside the faces found twice in all items (as no
// face: removing them would move the indexes after them), except in items
// approved as a whole. Decided and changed in one go under the locks, so no
// scan or name comes in between. The change goes into the history, so it
// can be undone (and then stays undone, see undoTwins). It returns how many.
func (l *Library) dropDuplicateFaces() int {
	s := l.people
	before := map[string]map[string]string{} // per photo: one change in the history each, undone on its own
	type wasFaces struct {
		it    *Item
		faces []Face
	}
	var was []wasFaces // to set back if saving fails
	var items []string
	id := strconv.FormatInt(time.Now().UnixNano(), 36) // this putting aside
	l.mu.Lock()
	s.mu.Lock()
	byItem := map[string]map[int]string{}
	for k, v := range s.labels {
		id, rest, ok := strings.Cut(k, ":")
		if !ok {
			continue
		}
		i, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		if byItem[id] == nil {
			byItem[id] = map[int]string{}
		}
		byItem[id][i] = v
	}
	for _, it := range l.items {
		if len(it.Faces) < 2 || it.FacesOK {
			continue
		}
		d := duplicates(it.Faces, byItem[it.ID])
		if len(d) == 0 {
			continue
		}
		items = append(items, it.ID)
		b := map[string]string{}
		before[it.ID] = b
		was = append(was, wasFaces{it, it.Faces})
		faces := append([]Face(nil), it.Faces...) // copy on write: grouping reads snapshots without the lock
		for _, i := range d {
			k := FaceRef{it.ID, i}.key()
			b[k] = s.labels[k]
			b[TwinUndoPrefix+k] = id // undone only while the face is still put aside by this
			s.labels[k] = labelNoFace
			faces[i].Twin, faces[i].TwinID = true, id
		}
		it.Faces = faces
	}
	s.mu.Unlock()
	l.mu.Unlock()
	n := 0
	for _, b := range before {
		n += len(b) / 2
	}
	if n == 0 {
		return 0
	}
	// in an order a stop between the steps leaves right (setItems puts a face
	// marked Twin without its no face aside again at the start): the history,
	// the faces (Twin), the labels. A failed save sets back what is still this
	// putting aside's (tried again at the next grouping).
	for _, item := range items {
		l.Record(Change{Kind: ChFaces, Items: []string{item}, Info: map[string]any{"op": "dupes", "n": len(before[item]) / 2}}, before[item])
	}
	err := l.saveIndexErr()
	if err == nil {
		err = s.save()
	}
	if err != nil {
		l.logf("faces: faces found twice not put aside: %v", err)
		l.mu.Lock()
		s.mu.Lock()
		for _, w := range was {
			if l.byID[w.it.ID] != w.it {
				continue
			}
			faces := append([]Face(nil), w.it.Faces...)
			for i := range faces {
				k := FaceRef{w.it.ID, i}.key()
				if faces[i].TwinID != id {
					continue // not this putting aside's (any more)
				}
				faces[i].Twin, faces[i].TwinID = false, ""
				if s.labels[k] == labelNoFace {
					if v := before[w.it.ID][k]; v == "" {
						delete(s.labels, k)
					} else {
						s.labels[k] = v
					}
				}
			}
			w.it.Faces = faces
		}
		s.mu.Unlock()
		l.mu.Unlock()
		l.markDirty()
		return 0
	}
	l.logf("faces: %d faces found twice put aside as no face", n)
	return n
}

// TwinUndoPrefix marks, in the undo of putting faces aside as found twice
// (history), which putting aside it was: "twin:item:idx" -> Face.TwinID.
const TwinUndoPrefix = "twin:"

// undoTwins brings back faces put aside as found twice (keys "item:idx" ->
// label before), those still put aside so by the same putting aside (ids:
// key -> Face.TwinID), still no face. They are marked as not
// found twice (NotDup), so grouping leaves them. Checked and changed under
// the locks in one go; it returns how many.
func (l *Library) undoTwins(labels, ids map[string]string) (int, error) {
	s := l.people
	type was struct {
		it    *Item
		faces []Face
	}
	var undo []was                    // to set back if saving fails
	prevLabels := map[string]string{} // the same
	n := 0
	l.mu.Lock()
	s.mu.Lock()
	byItem := map[*Item][]int{}
	for k, prev := range labels {
		id, rest, ok := strings.Cut(k, ":")
		i, err := strconv.Atoi(rest)
		it := l.byID[id]
		if !ok || err != nil || it == nil || i < 0 || i >= len(it.Faces) || !it.Faces[i].Twin || s.labels[k] != labelNoFace ||
			it.Faces[i].TwinID == "" || it.Faces[i].TwinID != ids[k] {
			continue
		}
		prevLabels[k] = s.labels[k]
		if prev = strings.TrimSpace(prev); prev == "" {
			delete(s.labels, k)
		} else {
			s.labels[k] = prev
		}
		byItem[it] = append(byItem[it], i)
		n++
	}
	for it, idx := range byItem {
		undo = append(undo, was{it, it.Faces})
		faces := append([]Face(nil), it.Faces...) // copy on write: grouping reads snapshots without the lock
		for _, i := range idx {
			faces[i].NotDup, faces[i].Twin, faces[i].TwinID = true, false, ""
		}
		it.Faces = faces
	}
	s.mu.Unlock()
	l.mu.Unlock()
	if n == 0 {
		return 0, nil
	}
	// back to before, for what is still this undo's (not changed since)
	setBack := func() {
		l.mu.Lock()
		s.mu.Lock()
		for _, w := range undo {
			if l.byID[w.it.ID] != w.it {
				continue
			}
			faces := append([]Face(nil), w.it.Faces...)
			for i := range faces {
				k := FaceRef{w.it.ID, i}.key()
				old := w.faces
				if i >= len(old) || !old[i].Twin || !faces[i].NotDup || faces[i].Twin {
					continue // not this undo's (any more)
				}
				faces[i].Twin, faces[i].TwinID, faces[i].NotDup = true, old[i].TwinID, false
				if pl, ok := prevLabels[k]; ok && s.labels[k] == strings.TrimSpace(labels[k]) {
					s.labels[k] = pl
				}
			}
			w.it.Faces = faces
		}
		s.mu.Unlock()
		l.mu.Unlock()
	}
	// the labels, then the index. A stop between them leaves the face marked
	// Twin without its no face, and setItems puts it aside again at the start:
	// the undo is not done and can be tried again. Either failing sets back
	// what is still this undo's, and the undo is not done either.
	if err := s.save(); err != nil {
		setBack()
		return 0, err
	}
	if err := l.saveIndexErr(); err != nil {
		setBack()
		if serr := s.save(); serr != nil { // the labels as before again (failing: setItems puts it aside at the start)
			l.logf("faces: %v", serr)
		}
		return 0, err
	}
	l.regroup()
	return n, nil
}
